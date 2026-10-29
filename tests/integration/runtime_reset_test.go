//go:build integration

package integration_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/work"
)

func TestFounderRuntimeResetPreservesIdentityModeAndLegacyEvidence(t *testing.T) {
	for _, scope := range []string{"idle-worker", "proven-session", "legacy-session", "unsafe-legacy-session", "founder-self"} {
		t.Run(scope, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			x := &operationReadOnlyExecutor{}
			engine := newEngine(t, h, &executor{})
			engine.Executor = x
			legacy := scope == "legacy-session" || scope == "unsafe-legacy-session"
			if legacy {
				root, err := os.MkdirTemp("/tmp", "fdn-reset-integration-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(root) })
				state, err := local.Open(ctx, filepath.Join(root, "s"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { state.Close() })
				engine.State, engine.Config.StateDir = state, state.Root
			}
			target := h.workerAgent.ID
			client := h.worker
			if scope == "founder-self" {
				target, client = h.founderAgent.ID, h.founder
				engine.Client = client
			} else {
				_, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", target, "mode"), domain.ModeRequest{
					Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: h.workerAgent.Revision}, Mode: "manual", Reason: "Keep deterministic reset fixture paused"})
				if err != nil {
					t.Fatal(err)
				}
			}
			engine.Config.Mode = "manual"
			before, err := api.Call[domain.AgentIdentity](ctx, client, "GET", "/v1/identity/status", nil)
			if err != nil {
				t.Fatal(err)
			}
			var original remote.Fence
			if scope == "proven-session" || legacy {
				session := closedOperationSession(t, h)
				original = remote.Fence{Active: true, ID: session.ID, Generation: "retained", CleanupProven: scope == "proven-session"}
				if err := engine.State.Put(ctx, "session-fence", original); err != nil {
					t.Fatal(err)
				}
				if legacy {
					directory := filepath.Join(engine.Config.StateDir, "sessions")
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(directory, session.ID+".sock")
					if scope == "unsafe-legacy-session" {
						if err := os.Symlink(filepath.Join(directory, "unrelated.sock"), path); err != nil {
							t.Fatal(err)
						}
					} else {
						listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
						if err != nil {
							t.Fatal(err)
						}
						listener.SetUnlinkOnClose(false)
						if err := os.Chmod(path, 0600); err != nil {
							t.Fatal(err)
						}
						listener.Close()
					}
				}
			}
			req := domain.AgentResetRequest{Command: command(), Reason: "Founder requests an identity-preserving runtime reset"}
			queued, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", target, "reset"), req)
			if err != nil || queued.State != "queued" || queued.Result != nil {
				t.Fatal("reset was not a durable nonexecuted submission", queued, err)
			}
			if legacy {
				runCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				manager := &remote.Manager{Client: engine.Client, State: engine.State, Config: engine.Config, Opener: remote.Native{}, Log: engine.Log}
				go func() { done <- engine.Serve(runCtx, manager) }()
				t.Cleanup(func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				})
			} else {
				startOperationWorker(t, engine)
			}
			result := waitAgentOperation(t, h.founder, queued.ID, engine)
			want := "reset"
			if scope == "unsafe-legacy-session" {
				want = "operator-required"
			}
			if result.State != want || result.Result == nil || result.Result.ModeAtCheck != "manual" || result.Result.AutomationMayResume {
				t.Fatal("reset outcome misrepresented its evidence or mode", result)
			}
			if scope == "unsafe-legacy-session" && result.Result.Code != "cleanup-unproven" {
				t.Fatal("legacy cleanup was silently treated as known ownership", result)
			}
			var fence remote.Fence
			if _, err := engine.State.Get(ctx, "session-fence", &fence); err != nil || (scope == "unsafe-legacy-session" && fence != original) || (scope != "unsafe-legacy-session" && fence.Active) {
				t.Fatal("reset did not preserve or retire the exact expected fence", fence, err)
			}
			if scope == "legacy-session" {
				directory := filepath.Join(engine.Config.StateDir, "sessions")
				if _, err := os.Lstat(filepath.Join(directory, original.ID+".sock")); !os.IsNotExist(err) {
					t.Fatal("reset left the orphan native endpoint in place", err)
				}
				retired, err := os.ReadDir(filepath.Join(directory, "retired"))
				if err != nil || len(retired) != 1 {
					t.Fatal("legacy endpoint was not preserved as private evidence", retired, err)
				}
			}
			after, err := api.Call[domain.AgentIdentity](ctx, client, "GET", "/v1/identity/status", nil)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("runtime reset changed the enrolled identity", before, after, err)
			}
			me, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
			if err != nil || me.Mode != "manual" || x.checks.Load() != 0 || x.invocations.Load() != 0 {
				t.Fatal("reset changed mode, provider authentication or spending", me, err, x.checks.Load(), x.invocations.Load())
			}
		})
	}
}

func TestFounderRuntimeResetCancelsActiveInferenceWithoutRetryAcrossRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &executor{block: make(chan struct{}), entered: make(chan struct{})}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	if _, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Origin: "human", Kind: "direct", ResponseRequest: true, Body: "Deterministic reset cancellation fixture"}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- engine.Serve(runCtx, nil) }()
	running := true
	defer func() {
		if running {
			stop()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("owned fixture provider never started")
	}
	req := domain.AgentResetRequest{Command: command(), Reason: "Cancel and join owned work while retaining uncertain effects"}
	queued, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "reset"), req)
	if err != nil {
		t.Fatal(err)
	}
	result := waitAgentOperation(t, h.founder, queued.ID, engine)
	if result.State != "operator-required" || result.Result == nil || result.Result.Code != "invocation-uncertain" {
		t.Fatal("interrupted inference was silently abandoned or retried", result)
	}
	journals, err := engine.State.Journals(ctx, true)
	if err != nil || len(journals) != 1 || journals[0].State != "uncertain" || x.count("message") != 1 {
		t.Fatal("owned invocation was not joined and durably fenced", journals, err, x.count("message"))
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first runtime did not join")
	}
	running = false
	restarted := work.New(engine.Config, "", h.worker, engine.State, x)
	restarted.Log = engine.Log
	startOperationWorker(t, restarted)
	replayed, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "reset"), req)
	if err != nil || replayed.ID != result.ID || replayed.State != result.State || !reflect.DeepEqual(replayed.Result, result.Result) {
		t.Fatal("reset submission lost its immutable result after restart", replayed, err)
	}
	refresh, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "diagnostics"), domain.DiagnosticRefreshRequest{
		Command: command(), Reason: "Prove the restarted control loop is responsive without retrying inference"})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitAgentOperation(t, h.founder, refresh.ID, restarted); got.State != "refreshed" {
		t.Fatal("restarted control loop did not run", got)
	}
	after, err := engine.State.Journals(ctx, true)
	if err != nil || !reflect.DeepEqual(journals, after) || x.count("message") != 1 {
		t.Fatal("restart or reset replay repeated uncertain inference", after, err, x.count("message"))
	}
}

func TestFounderRuntimeResetClosesActualOwnedInteractiveSession(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	x := &executor{}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	opener := &echoEndpoint{}
	manager := &remote.Manager{Client: h.worker, State: engine.State, Config: engine.Config, Opener: opener, Log: engine.Log}
	done := make(chan error, 1)
	go func() { done <- engine.Serve(ctx, manager) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	grant := grantFor(t, h, h.founderAgent.ID, h.workerAgent.ID, "tmux", "terminal", "")
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", domain.SessionRequest{
		Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, Mode: "tmux", Purpose: "terminal", Reason: "Owned loopback reset endpoint", ExpiresAt: time.Now().Unix() + 120})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: session.Revision}, Action: "open"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for opener.opened.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("owned interactive endpoint never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	reset, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "reset"), domain.AgentResetRequest{
		Command: command(), Reason: "Founder closes the owned interactive endpoint and resets the runtime"})
	if err != nil {
		t.Fatal(err)
	}
	result := waitAgentOperation(t, h.founder, reset.ID, engine)
	if result.State != "reset" || result.Result == nil || result.Result.ModeAtCheck != "ai" {
		t.Fatal("owned session reset did not complete", result)
	}
	current, err := api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
	if err != nil || current.State != "closed" {
		t.Fatal("reset abandoned rather than closed its actual session", current, err)
	}
	var fence remote.Fence
	if _, err := engine.State.Get(ctx, "session-fence", &fence); err != nil || fence.Active || opener.opened.Load() != 1 || x.count("message") != 0 || x.count("onboarding") != 0 {
		t.Fatal("reset lost cleanup proof, reopened the session or started inference", fence, err, opener.opened.Load())
	}
}
