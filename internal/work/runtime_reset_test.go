package work

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
)

func resetFixture(t *testing.T) *operationFixture {
	t.Helper()
	f := newOperationFixture(t, "runtime-reset", false)
	if err := f.engine.State.Put(context.Background(), "session-fence", remote.Fence{}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRuntimeResetPreservesDurableStateAndMode(t *testing.T) {
	for _, mode := range []string{"ai", "manual"} {
		t.Run(mode, func(t *testing.T) {
			f := resetFixture(t)
			ctx := context.Background()
			f.me.Mode, f.engine.Config.Mode = mode, mode
			before := f.engine.Config
			values := map[string]string{
				"initial-review":          "retained-inventory",
				"identity":                "retained-identity",
				"authentication-requests": "retained-authentication-evidence",
				"permission-requests":     "retained-permission-evidence",
			}
			for key, value := range values {
				if err := f.engine.State.Put(ctx, key, value); err != nil {
					t.Fatal(err)
				}
			}
			journal := local.Journal{ID: "invocation_completed", State: "done", PromptHash: "retained-prompt", CreatedAt: 1800000000}
			if err := f.engine.State.SaveJournal(ctx, journal); err != nil {
				t.Fatal(err)
			}
			reset, err := f.engine.handleAgentOperation(ctx, f.operation.ID)
			if err != nil || !reset || f.operation.State != "reset" || f.operation.Result == nil || f.operation.Result.ModeAtCheck != mode || f.operation.Result.AutomationMayResume != (mode == "ai") {
				t.Fatal("runtime reset did not preserve the authoritative mode", reset, f.operation, err)
			}
			if err := domain.ValidateAgentOperation(f.operation); err != nil {
				t.Fatal("reset result violates the common operation contract", err)
			}
			for key, want := range values {
				var got string
				if found, err := f.engine.State.Get(ctx, key, &got); err != nil || !found || got != want {
					t.Fatal("reset changed retained private state", key, found, got, err)
				}
			}
			got, err := f.engine.State.Journal(ctx, journal.ID)
			if err != nil || !reflect.DeepEqual(got, journal) || !reflect.DeepEqual(f.engine.Config, before) || f.executor.checks.Load() != 0 || f.executor.invocations.Load() != 0 {
				t.Fatal("reset changed configuration/history or invoked a provider", got, err)
			}
		})
	}
}

func TestRuntimeResetJoinsOwnedWorkAndRetainsUncertainty(t *testing.T) {
	f := resetFixture(t)
	ctx, finished, err := f.engine.beginRuntimeWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	journal := local.Journal{ID: "invocation_owned", State: "uncertain", PID: 12345, CreatedAt: 1800000000, Problem: "private retained uncertainty"}
	joined := make(chan error, 1)
	go func() {
		<-ctx.Done()
		err := f.engine.State.SaveJournal(context.Background(), journal)
		finished()
		joined <- err
	}()
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || reset || f.operation.State != "operator-required" || f.operation.Result.Code != "invocation-uncertain" || !errors.Is(context.Cause(ctx), errRuntimeReset) {
		t.Fatal("reset discarded uncertainty or did not cancel its own work", reset, f.operation, err)
	}
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	got, err := f.engine.State.Journal(context.Background(), journal.ID)
	if err != nil || !reflect.DeepEqual(got, journal) || f.executor.invocations.Load() != 0 {
		t.Fatal("reset did not retain the joined invocation exactly", got, err)
	}
}

func TestRuntimeResetNeverBypassesIndependentFences(t *testing.T) {
	for _, item := range []struct {
		name, code string
		prepare    func(*testing.T, *operationFixture)
	}{
		{"unsupported-legacy-session", "cleanup-unproven", func(t *testing.T, f *operationFixture) {
			if err := f.engine.State.Put(context.Background(), "session-fence", remote.Fence{Active: true, ID: f.session.ID, Generation: "legacy"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"live-proven-session", "session-unavailable", func(t *testing.T, f *operationFixture) {
			f.session.State = "active"
			if err := f.engine.State.Put(context.Background(), "session-fence", remote.Fence{Active: true, ID: f.session.ID, CleanupProven: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"other-process-slot", "slot-occupied", func(t *testing.T, f *operationFixture) {
			slot, err := secure.Acquire(filepath.Join(f.engine.State.Root, "work.lock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { slot.Close() })
		}},
		{"prepared-work", "work-pending", func(t *testing.T, f *operationFixture) {
			if err := f.engine.State.SaveJournal(context.Background(), local.Journal{ID: "prepared", State: "prepared", CreatedAt: 1800000000}); err != nil {
				t.Fatal(err)
			}
		}},
		{"pending-report", "report-pending", func(t *testing.T, f *operationFixture) {
			if err := f.engine.State.Enqueue(context.Background(), local.Outbox{OperationID: "pending_report", Method: "POST", Path: "/v1/tasks/task_retained/report", Body: json.RawMessage(`{"operation_id":"pending_report"}`), CreatedAt: 1800000000}); err != nil {
				t.Fatal(err)
			}
		}},
		{"maintenance", "maintenance-active", func(t *testing.T, f *operationFixture) {
			if err := f.engine.State.Put(context.Background(), "maintenance", struct {
				Active bool `json:"active"`
			}{true}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := resetFixture(t)
			item.prepare(t, f)
			before, err := f.engine.State.WorkSnapshot(context.Background(), []string{"maintenance", "session-fence"})
			if err != nil {
				t.Fatal(err)
			}
			reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
			if err != nil || reset || f.operation.State != "operator-required" || f.operation.Result.Code != item.code || f.operation.Result.AutomationMayResume {
				t.Fatal("independent fence was bypassed", reset, f.operation, err)
			}
			after, err := f.engine.State.WorkSnapshot(context.Background(), []string{"maintenance", "session-fence"})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("reset erased independent durable evidence", before, after, err)
			}
		})
	}
}

func TestRuntimeResetRetiresOnlyProvenTerminalSession(t *testing.T) {
	f := resetFixture(t)
	if err := f.engine.State.Put(context.Background(), "session-fence", remote.Fence{Active: true, ID: f.session.ID, Generation: "owned", CleanupProven: true}); err != nil {
		t.Fatal(err)
	}
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || !reset || f.operation.State != "reset" || f.fence().Active {
		t.Fatal("proved terminal session was not atomically retired", reset, f.operation, err)
	}
	var receipt local.AgentOperationReceipt
	if found, err := f.engine.State.Get(context.Background(), local.AgentOperationResultKey(f.operation.ID), &receipt); err != nil || !found || receipt.Report.State != "reset" {
		t.Fatal("fence retirement lost its durable reset result", found, receipt, err)
	}
}

func TestRuntimeResetProvesTerminalLegacyCleanupBeforeRetirement(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fdn-reset-work-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	f := resetFixture(t)
	state, err := local.Open(context.Background(), filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	f.engine.State, f.engine.Config.StateDir = state, state.Root
	f.session.Mode = "tmux"
	if err := f.engine.State.Put(context.Background(), "session-fence", remote.Fence{Active: true, ID: f.session.ID, Generation: "legacy"}); err != nil {
		t.Fatal(err)
	}
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || !reset || f.operation.State != "reset" || f.fence().Active {
		t.Fatal("explicit reset stranded a kernel-confirmed missing legacy endpoint", reset, f.operation, err)
	}
	var receipt local.AgentOperationReceipt
	if found, err := f.engine.State.Get(context.Background(), local.AgentOperationResultKey(f.operation.ID), &receipt); err != nil || !found || receipt.Report.State != "reset" {
		t.Fatal("legacy cleanup did not commit its exact final reset receipt", found, receipt, err)
	}
}

func TestRuntimeResetReplayNeverCancelsNewerWork(t *testing.T) {
	f := resetFixture(t)
	f.lostAck = true
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err == nil || !reset {
		t.Fatal("lost reset acknowledgement was hidden", reset, err)
	}
	ctx, finished, err := f.engine.beginRuntimeWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finished()
	reset, err = f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || reset || ctx.Err() != nil || f.operation.State != "reset" {
		t.Fatal("replayed reset cancelled a newer invocation", reset, f.operation, err, ctx.Err())
	}
	if len(f.reports) != 3 || !reflect.DeepEqual(f.reports[1], f.reports[2]) {
		t.Fatal("reset result was not replayed with the original exact proposal", f.reports)
	}
}

func TestInterruptedRuntimeResetDoesNotCancelNewerWork(t *testing.T) {
	f := resetFixture(t)
	hash, err := agentOperationHash(f.operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.State.Put(context.Background(), "agent-reset-start:"+f.operation.ID, hash); err != nil {
		t.Fatal(err)
	}
	ctx, finished, err := f.engine.beginRuntimeWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finished()
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || reset || f.operation.State != "operator-required" || f.operation.Result.Code != "reset-interrupted" || ctx.Err() != nil {
		t.Fatal("interrupted reset was blindly rerun", reset, f.operation, err)
	}
}

func TestRuntimeResetFounderSelfTargetAndChangedAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		f := resetFixture(t)
		f.me.Founder = true
		f.founder = f.me
		f.operation.RequesterID = f.me.ID
		f.operation.RequesterGeneration = f.operation.TargetGeneration
		f.denyBeforeMutation = revoked
		ctx, finished, err := f.engine.beginRuntimeWork(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !revoked {
			go func() { <-ctx.Done(); finished() }()
		}
		reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
		if revoked {
			if err == nil || reset || ctx.Err() != nil {
				t.Fatal("changed Founder authority still cancelled owned work", reset, err)
			}
			finished()
		} else if err != nil || !reset || f.operation.State != "reset" || f.operation.Result.ModeAtCheck != "manual" || f.operation.Result.AutomationMayResume {
			t.Fatal("Founder could not reset its own runtime without changing mode", reset, f.operation, err)
		}
	}
}

type resetSessionRunner struct {
	runs, active, maximum, cleanups atomic.Int64
	started                         chan struct{}
	prepare                         func(context.Context, domain.Session, remote.SessionFenceSnapshot) error
}

func (r *resetSessionRunner) Run(ctx context.Context) error {
	r.runs.Add(1)
	n := r.active.Add(1)
	for previous := r.maximum.Load(); n > previous; previous = r.maximum.Load() {
		if r.maximum.CompareAndSwap(previous, n) {
			break
		}
	}
	r.started <- struct{}{}
	<-ctx.Done()
	r.active.Add(-1)
	return nil
}

func (r *resetSessionRunner) Cleanup(context.Context) error {
	if r.active.Load() != 0 {
		return errors.New("cleanup raced a running session supervisor")
	}
	r.cleanups.Add(1)
	return nil
}

func (r *resetSessionRunner) PrepareReset(ctx context.Context, session domain.Session, snapshot remote.SessionFenceSnapshot) error {
	if r.prepare == nil {
		return domain.Fail(domain.Prerequisite, "fixture endpoint has no native reset proof")
	}
	return r.prepare(ctx, session, snapshot)
}

func TestRuntimeResetCleanupCannotRetireReplacementFence(t *testing.T) {
	f := resetFixture(t)
	f.session.Mode = "tmux"
	original := remote.Fence{Active: true, ID: f.session.ID, Generation: "legacy"}
	replacement := remote.Fence{Active: true, ID: f.session.ID, Generation: "replacement"}
	if err := f.engine.State.Put(context.Background(), "session-fence", original); err != nil {
		t.Fatal(err)
	}
	runner := &resetSessionRunner{started: make(chan struct{}, 4), prepare: func(ctx context.Context, _ domain.Session, expected remote.SessionFenceSnapshot) error {
		if expected.Fence != original {
			t.Fatal("reset did not bind the exact pre-cleanup fence", expected)
		}
		return f.engine.State.Put(ctx, "session-fence", replacement)
	}}
	ctx, cancel := context.WithCancel(context.Background())
	f.engine.sessions = newSessionRuntime(ctx, runner)
	t.Cleanup(func() { cancel(); f.engine.sessions.stop() })
	<-runner.started
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err == nil || reset || f.fence() != replacement || f.operation.State == "reset" {
		t.Fatal("native cleanup proof was applied to a newer fence", reset, f.operation, err)
	}
}

func TestRuntimeResetJoinsAndReinitializesSingleSessionSupervisor(t *testing.T) {
	f := resetFixture(t)
	runner := &resetSessionRunner{started: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.engine.sessions = newSessionRuntime(ctx, runner)
	t.Cleanup(func() {
		cancel()
		if err := f.engine.sessions.stop(); err != nil {
			t.Error(err)
		}
	})
	<-runner.started
	reset, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
	if err != nil || !reset {
		t.Fatal(reset, f.operation, err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("session supervisor did not restart")
	}
	if runner.runs.Load() != 2 || runner.active.Load() != 1 || runner.maximum.Load() != 1 || runner.cleanups.Load() != 1 {
		t.Fatal("reset overlapped supervisors or omitted owned cleanup", runner.runs.Load(), runner.maximum.Load(), runner.cleanups.Load())
	}
}

type stoppedSessionRunner struct{}

func (stoppedSessionRunner) Run(context.Context) error { return nil }

func TestRuntimeResetDoesNotAdoptUnexpectedSupervisorExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := newSessionRuntime(ctx, stoppedSessionRunner{})
	<-runtime.current.done
	if err := runtime.pause(context.Background()); err == nil {
		t.Fatal("cancellation reclassified a prior unexpected supervisor exit")
	}
	if err := runtime.resume(); err == nil {
		t.Fatal("reset restarted an unexpectedly stopped supervisor without resolving its failure")
	}
}
