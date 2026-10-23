//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/work"
)

type operationReadOnlyExecutor struct{ checks, invocations atomic.Int64 }

func (x *operationReadOnlyExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	x.checks.Add(1)
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/PRIVATE_CONTROL_PROVIDER_PATH", Authentication: "credentials-present-unverified"}, nil
}
func (x *operationReadOnlyExecutor) Invoke(context.Context, provider.Invocation) (provider.Reply, error) {
	x.invocations.Add(1)
	return provider.Reply{}, domain.Fail(domain.Provider, "remote control must not start inference")
}

func waitAgentOperation(t *testing.T, client *api.Client, id string, engines ...*work.Engine) domain.AgentOperation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		operation, err := api.Call[domain.AgentOperation](context.Background(), client, "GET", api.Path("agent-operations", id), nil)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State != "queued" && operation.State != "checking" {
			return operation
		}
		if time.Now().After(deadline) {
			for _, engine := range engines {
				var problem domain.Fault
				_, _ = engine.State.Get(context.Background(), "agent-control-problem", &problem)
				pending, _ := engine.State.PendingAgentOperationResults(context.Background(), 20)
				t.Log("retained control diagnostic", problem, "pending results", pending)
			}
			t.Fatal("independent agent operation did not finish", operation)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startOperationWorker(t *testing.T, engine *work.Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Serve(ctx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("independent control loop did not join")
		}
	})
}

func closedOperationSession(t *testing.T, h *harness) domain.Session {
	t.Helper()
	ctx := context.Background()
	grant := grantFor(t, h, h.founderAgent.ID, h.workerAgent.ID, "tmux", "terminal", "")
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", domain.SessionRequest{Command: command(), GrantID: grant.ID,
		TargetID: h.workerAgent.ID, Mode: "tmux", Purpose: "terminal", Reason: "Isolated fixture, no native endpoint was opened", ExpiresAt: time.Now().Unix() + 120})
	if err != nil {
		t.Fatal(err)
	}
	close := domain.SessionChange{Command: command(), Action: "close"}
	close.ExpectedRevision = session.Revision
	session, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), close)
	if err != nil || session.State != "closed" {
		t.Fatal("fixture session did not close", session, err)
	}
	return session
}

func TestFounderRefreshAndClosedSessionRepairWorkWhileAgentIsPausedAndFenced(t *testing.T) {
	for _, item := range []struct {
		name  string
		proof bool
		skew  time.Duration
	}{
		{"legacy-proof-required", false, 0},
		{"durably-proven-cleanup", true, 0},
		{"target-clock-behind", true, -time.Hour},
		{"target-clock-ahead", true, time.Hour},
	} {
		t.Run(item.name, func(t *testing.T) {
			proof := item.proof
			h := newHarness(t)
			ctx := context.Background()
			pause := domain.ModeRequest{Command: command(), Mode: "manual", Reason: "Founder explicitly pauses isolated operation fixture"}
			pause.ExpectedRevision = h.workerAgent.Revision
			if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "mode"), pause); err != nil {
				t.Fatal(err)
			}
			session := closedOperationSession(t, h)
			x := &operationReadOnlyExecutor{}
			engine := newEngine(t, h, &executor{})
			engine.Executor = x
			engine.Now = func() time.Time { return time.Now().Add(item.skew) }
			if err := engine.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: session.ID, CleanupProven: proof, Generation: "fence_isolated"}); err != nil {
				t.Fatal(err)
			}
			refresh, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "diagnostics"), domain.DiagnosticRefreshRequest{Command: command(), Reason: "Founder requests new safe evidence"})
			if err != nil || refresh.State != "queued" {
				t.Fatal("refresh request was not durably queued", refresh, err)
			}
			startOperationWorker(t, engine)
			refresh = waitAgentOperation(t, h.founder, refresh.ID)
			if refresh.State != "refreshed" || refresh.Result == nil || refresh.Result.Observation == nil || refresh.Result.Observation.Provider == nil {
				t.Fatal("fresh evidence was blocked by pause/fence", refresh)
			}
			observation := refresh.Result.Observation
			if observation.Provider.Authentication != "credentials-present-unverified" || observation.SessionFence == nil || observation.SessionFence.SessionID != session.ID {
				t.Fatal("fresh inspection did not preserve readiness/fence distinction", observation)
			}
			data, _ := json.Marshal(refresh)
			if strings.Contains(string(data), "PRIVATE_CONTROL_PROVIDER_PATH") {
				t.Fatal("provider private path entered remote diagnostics", string(data))
			}
			repair, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "repairs"), domain.SessionRecoveryRequest{Command: command(), SessionID: session.ID,
				ExpectedFence: observation.SessionFence.Fingerprint, Reason: "Founder authorizes only this exact closed-session fence"})
			if err != nil {
				t.Fatal(err)
			}
			repair = waitAgentOperation(t, h.founder, repair.ID, engine)
			want := "operator-required"
			if proof {
				want = "recovered"
			}
			if repair.State != want || repair.Result == nil || repair.Result.ModeAtCheck != "manual" || repair.Result.AutomationMayResume {
				t.Fatal("unsafe or misleading remote recovery result", repair)
			}
			var fence remote.Fence
			if _, err := engine.State.Get(ctx, "session-fence", &fence); err != nil || fence.Active == proof {
				t.Fatal("fence mutation did not match durable cleanup proof", fence, err)
			}
			me, err := api.Call[domain.Agent](ctx, h.worker, "GET", "/v1/me", nil)
			if err != nil || me.Mode != "manual" || x.invocations.Load() != 0 || x.checks.Load() != 1 {
				t.Fatal("remote control changed pause or performed inference", me, err, x.invocations.Load(), x.checks.Load())
			}
		})
	}
}

func TestFounderDiagnosticRefreshDoesNotWaitForBlockedProviderInvocation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	release := make(chan struct{})
	x := &executor{block: release, entered: make(chan struct{})}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	if _, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID,
		Body: "Isolated request held by a deterministic provider", Origin: "human", Kind: "direct", ResponseRequest: true}); err != nil {
		t.Fatal(err)
	}
	startOperationWorker(t, engine)
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture invocation did not start")
	}
	defer close(release)
	refresh, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "diagnostics"), domain.DiagnosticRefreshRequest{Command: command(), Reason: "Read-only check during a blocked invocation"})
	if err != nil {
		t.Fatal(err)
	}
	refresh = waitAgentOperation(t, h.founder, refresh.ID)
	if refresh.State != "refreshed" || refresh.Result == nil || refresh.Result.Observation == nil {
		t.Fatal("operation waited for normal inference admission", refresh)
	}
	observation := refresh.Result.Observation
	if observation.WorkSlot != "occupied" || observation.Health == nil || observation.Health.Phase != "invoking" || x.count("message") != 1 {
		t.Fatal("refresh cancelled, reran or hid the current invocation", observation, x.count("message"))
	}
}
