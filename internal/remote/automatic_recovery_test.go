package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type retryCleanupEndpoint struct {
	io.ReadWriteCloser
	failures int64
	closes   atomic.Int64
	once     sync.Once
}

func (e *retryCleanupEndpoint) Close() error {
	var closeErr error
	e.once.Do(func() { closeErr = e.ReadWriteCloser.Close() })
	if e.closes.Add(1) <= e.failures {
		return errors.Join(closeErr, &process.CleanupError{Err: errors.New("temporary owned cleanup failure")})
	}
	return closeErr
}

func failingManagerEndpoint(t *testing.T, f *managerFixture, failures int64) *retryCleanupEndpoint {
	t.Helper()
	left, right := net.Pipe()
	right.Close()
	endpoint := &retryCleanupEndpoint{ReadWriteCloser: left, failures: failures}
	f.manager.Opener = openerFunc(func(context.Context, domain.Session, config.Worker) (io.ReadWriteCloser, error) {
		return endpoint, nil
	})
	return endpoint
}

func TestAutomaticSessionCleanupRetriesOwnedEndpointWithoutHumanRecovery(t *testing.T) {
	f := newManagerFixture(t)
	endpoint := failingManagerEndpoint(t, f, 2)
	session, _, _ := f.snapshot()
	if err := f.manager.start(context.Background(), session); err == nil {
		t.Fatal("temporary cleanup error was hidden")
	}
	var before Fence
	if _, err := f.manager.State.Get(context.Background(), "session-fence", &before); err != nil || !before.Active || before.CleanupProven {
		t.Fatal("unproved endpoint lost its fence", before, err)
	}
	if err := f.manager.retryCleanup(context.Background()); err != nil {
		t.Fatal("owned endpoint was not automatically retried", err)
	}
	var after Fence
	if _, err := f.manager.State.Get(context.Background(), "session-fence", &after); err != nil || after.Active {
		t.Fatal("successful native cleanup did not retire the fence", after, err)
	}
	current, _, _ := f.snapshot()
	if current.State != "closed" || endpoint.closes.Load() != 3 || f.opened.Load() != 1 || f.manager.Config.Mode != "manual" {
		t.Fatal("cleanup retried tools, changed mode or failed retirement", current, endpoint.closes.Load())
	}
}

func TestAutomaticSessionCleanupRunsWithoutAnotherSessionUpdate(t *testing.T) {
	f := newManagerFixture(t)
	endpoint := failingManagerEndpoint(t, f, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.manager.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var fence Fence
		if _, err := f.manager.State.Get(ctx, "session-fence", &fence); err != nil {
			t.Fatal(err)
		}
		if endpoint.closes.Load() >= 3 && !fence.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("due cleanup was stranded waiting for an unrelated update", endpoint.closes.Load(), fence)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.opened.Load() != 1 {
		t.Fatal("cleanup reopened the interactive endpoint")
	}
}

func TestAutomaticCleanupPreservesBusySlotsInvocationsAndReplacementFences(t *testing.T) {
	for _, kind := range []string{"work-slot", "invocation", "replacement-fence"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagerFixture(t)
			endpoint := failingManagerEndpoint(t, f, 10)
			session, _, _ := f.snapshot()
			if err := f.manager.start(context.Background(), session); err == nil {
				t.Fatal("fixture cleanup did not fail")
			}
			var before Fence
			if _, err := f.manager.State.Get(context.Background(), "session-fence", &before); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "work-slot":
				lock, err := secure.Acquire(filepath.Join(f.manager.Config.StateDir, "work.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "invocation":
				if err := f.manager.State.SaveJournal(context.Background(), local.Journal{ID: "invocation_owned", State: "running", PID: 123, CreatedAt: 1}); err != nil {
					t.Fatal(err)
				}
			case "replacement-fence":
				before.Generation = "fence_replacement"
				if err := f.manager.State.Put(context.Background(), "session-fence", before); err != nil {
					t.Fatal(err)
				}
			}
			count := endpoint.closes.Load()
			err := f.manager.retryCleanup(context.Background())
			if kind != "replacement-fence" && err == nil {
				t.Fatal("another admission owner did not block cleanup")
			}
			var after Fence
			if _, err := f.manager.State.Get(context.Background(), "session-fence", &after); err != nil || after != before || endpoint.closes.Load() != count {
				t.Fatal("cleanup changed unrelated ownership evidence", after, err)
			}
		})
	}
}

func TestAutomaticRecoveryRetiresDurableNativeProofAfterRestart(t *testing.T) {
	f := newManagerFixture(t)
	ctx := context.Background()
	f.mu.Lock()
	f.session.Mode, f.session.State = "tmux", "closed"
	f.mu.Unlock()
	session, _, _ := f.snapshot()
	fence := Fence{Active: true, ID: session.ID, Generation: "fence_restart"}
	if err := f.manager.State.Put(ctx, "session-fence", fence); err != nil {
		t.Fatal(err)
	}
	path, _, err := sessionNativePaths(f.manager.Config, session.ID, fence.Generation)
	if err != nil {
		t.Fatal(err)
	}
	record := nativeSession{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: fence.Generation, State: "cleaned", ServerStopped: true}
	if err := writeNativeSession(path, record); err != nil {
		t.Fatal(err)
	}
	root := f.manager.State.Root
	if err := f.manager.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	restarted := &Manager{Client: f.manager.Client, State: state, Config: f.manager.Config, Opener: Native{}}
	if err := restarted.retryCleanup(ctx); err != nil {
		t.Fatal("restart did not replay durable native proof", err)
	}
	var after Fence
	if _, err := state.Get(ctx, "session-fence", &after); err != nil || after.Active || f.opened.Load() != 0 {
		t.Fatal("restart recovery opened a session or retained a proven fence", after, err)
	}
}

func TestAutomaticRecoveryDoesNotAdoptLegacyOrLiveSessions(t *testing.T) {
	for _, kind := range []string{"missing-ownership", "running-session", "different-generation", "different-target", "launch-uncertain"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagerFixture(t)
			ctx := context.Background()
			f.mu.Lock()
			f.session.Mode, f.session.State = "tmux", "closed"
			f.mu.Unlock()
			session, _, _ := f.snapshot()
			fence := Fence{Active: true, ID: session.ID, Generation: "fence_original"}
			record := nativeSession{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: fence.Generation, State: "cleaned", ServerStopped: true}
			switch kind {
			case "running-session":
				f.mu.Lock()
				f.session.State = "active"
				f.mu.Unlock()
				record.State = "running"
			case "different-target":
				f.mu.Lock()
				f.session.TargetID = "agent_other"
				f.mu.Unlock()
			case "launch-uncertain":
				record.State, record.ServerStopped = "launching", false
			}
			if err := f.manager.State.Put(ctx, "session-fence", fence); err != nil {
				t.Fatal(err)
			}
			generation := fence.Generation
			if kind == "different-generation" {
				generation = "fence_other"
			}
			path, _, err := sessionNativePaths(f.manager.Config, session.ID, generation)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "missing-ownership" {
				record.Generation = generation
				if err := writeNativeSession(path, record); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.manager.retryCleanup(ctx); err == nil {
				t.Fatal("unproved or active ownership was silently retired")
			}
			var after Fence
			if _, err := f.manager.State.Get(ctx, "session-fence", &after); err != nil || after != fence {
				t.Fatal("unsafe automatic recovery rewrote the fence", after, err)
			}
		})
	}
}

func TestNativePrelaunchTmuxFailureAutomaticallyRetiresFence(t *testing.T) {
	f := newManagerFixture(t)
	f.mu.Lock()
	f.session.Mode, f.session.Purpose, f.session.Provider = "tmux", "authentication", "codex"
	f.mu.Unlock()
	f.manager.Config.ProviderPaths = map[string]string{"codex": filepath.Join(t.TempDir(), "missing-codex")}
	session, _, _ := f.snapshot()
	if err := f.manager.start(context.Background(), session); err == nil {
		t.Fatal("missing provider was reported as a successful login")
	}
	var fence Fence
	if _, err := f.manager.State.Get(context.Background(), "session-fence", &fence); err != nil || fence.Active {
		t.Fatal("known no-process startup failure stranded automatic work", fence, err)
	}
	current, _, _ := f.snapshot()
	if current.State != "closed" {
		t.Fatal("failed authentication preparation left a live session")
	}
}

type cleanupRunner struct {
	calls    atomic.Int64
	failures int64
}

func (r *cleanupRunner) Run(_ context.Context, spec process.Spec) error {
	if len(spec.Args) == 0 || spec.Args[len(spec.Args)-1] != "kill-server" {
		return errors.New("unexpected native cleanup command")
	}
	if r.calls.Add(1) <= r.failures {
		return errors.New("temporary scoped tmux cleanup failure")
	}
	return nil
}

func TestNativeCloseRetriesAndPersistsOwnedCleanupWithoutCachingFailure(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := config.DefaultWorker(filepath.Join(root, "state"))
	if err := secure.Dir(c.StateDir); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ID: "session_native_retry", TargetID: "agent_native", Mode: "tmux", State: "closed"}
	runner := &cleanupRunner{failures: 1}
	endpoint, err := (Native{Runner: runner}).OpenOwned(context.Background(), session, c, "fence_owned")
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	if err := endpoint.Close(); err == nil {
		t.Fatal("temporary cleanup error was hidden")
	}
	restarted := Native{Runner: runner}
	if err := restarted.RecoverOwned(context.Background(), session, c, "fence_owned"); err != nil {
		t.Fatal("durable owned cleanup could not be retried after losing the handle", err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal("failed close result was cached forever", err)
	}
	record, _, _, err := readNativeSession(c, session, "fence_owned")
	if err != nil || record.State != "cleaned" || !record.ServerStopped {
		t.Fatal("native cleanup proof was not durable", record, err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal("successful cleanup was not idempotent", err)
	}
}

func TestOwnedProcessIdentityRejectsPIDReuseWithoutSignals(t *testing.T) {
	current, err := inspectProcess(os.Getpid())
	if err != nil || current == nil || current.Started == "" || current.UID != uint32(os.Geteuid()) {
		t.Fatal("kernel process identity unavailable", current, err)
	}
	wrong := *current
	wrong.Group = wrong.PID
	wrong.Started = "different-process-start"
	if _, err := wrong.stopped(); err == nil {
		t.Fatal("PID alone was treated as process ownership")
	}
}

type staleServerRunner struct{ calls atomic.Int64 }

func (r *staleServerRunner) Run(context.Context, process.Spec) error {
	r.calls.Add(1)
	return errors.New("the recorded tmux server has already exited")
}

func TestNativeRecoveryHandlesStaleSocketUsingExactRecordedProcessIdentity(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fdn-owned-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c := config.DefaultWorker(filepath.Join(root, "s"))
	session := domain.Session{ID: "session_stale", TargetID: "agent_owned", Mode: "tmux", State: "closed"}
	path, socket, err := sessionNativePaths(c, session.ID, "fence_stale")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	identity, err := inspectProcess(child.Process.Pid)
	if err != nil || identity == nil || !identity.valid() {
		child.Process.Kill()
		child.Wait()
		t.Fatal("owned fixture process identity unavailable", identity, err)
	}
	if err := syscall.Kill(-identity.Group, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	record := nativeSession{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: "fence_stale",
		State: "closing", Client: identity, Server: identity}
	if err := writeNativeSession(path, record); err != nil {
		t.Fatal(err)
	}
	runner := &staleServerRunner{}
	if err := (Native{Runner: runner}).RecoverOwned(context.Background(), session, c, record.Generation); err != nil {
		t.Fatal("stale socket stranded already-recorded cleanup", err)
	}
	after, _, _, err := readNativeSession(c, session, record.Generation)
	if err != nil || after.State != "cleaned" || !after.ServerStopped || runner.calls.Load() != 2 {
		t.Fatal("recorded native exit did not produce durable cleanup evidence", after, err)
	}
}

func TestNativeRecoveryRejectsForeignSocketBeforeExecutingCleanupCommands(t *testing.T) {
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "s"))
	session := domain.Session{ID: "session_foreign", TargetID: "agent_owned", Mode: "tmux", State: "closed"}
	path, socket, err := sessionNativePaths(c, session.ID, "fence_foreign")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c.StateDir, "another-session.sock"), socket); err != nil {
		t.Fatal(err)
	}
	record := nativeSession{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: "fence_foreign",
		State: "closing", Client: &ownedProcess{PID: 12345, Group: 12345, UID: uint32(os.Geteuid()), Started: "fixture"}}
	if err := writeNativeSession(path, record); err != nil {
		t.Fatal(err)
	}
	runner := &staleServerRunner{}
	err = (Native{Runner: runner}).RecoverOwned(context.Background(), session, c, record.Generation)
	if err == nil || domain.AsFault(err).Code != domain.Denied || runner.calls.Load() != 0 {
		t.Fatal("cleanup adopted a replaced endpoint", err, runner.calls.Load())
	}
}
