package remote

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type sessionReadinessRunner struct {
	discovered chan struct{}
	release    chan struct{}
	failure    error
	missing    int
	once       sync.Once
}

func (runner *sessionReadinessRunner) Run(ctx context.Context, spec process.Spec) error {
	if len(spec.Args) > 2 && spec.Args[2] == "has-session" {
		if len(spec.Args) != 5 || spec.Args[3] != "-t" || spec.Args[4] != "=foundation" {
			return errors.New("native startup did not query its exact session")
		}
		select {
		case <-runner.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if runner.failure != nil {
			return runner.failure
		}
		if runner.missing > 0 {
			runner.missing--
			spec.Args = append([]string(nil), spec.Args...)
			spec.Args[4] = "=fixture_missing_session"
		}
	}
	err := (process.Native{}).Run(ctx, spec)
	if len(spec.Args) > 2 && spec.Args[2] == "display-message" && err == nil {
		runner.once.Do(func() { close(runner.discovered) })
	}
	return err
}

func TestSharingNativeStartupWaitsForItsSessionNotOnlyTheServer(t *testing.T) {
	ctx, c, _, _ := sharingNativeFixture(t)
	session := domain.Session{ID: "session_startup_readiness", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}
	generation := "native_startup_readiness"
	runner := &sessionReadinessRunner{discovered: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		endpoint io.ReadWriteCloser
		err      error
	}
	opened := make(chan result, 1)
	go func() {
		endpoint, err := (Native{Runner: runner, Command: []string{"/bin/sh", "-i"}}).OpenOwned(ctx, session, c, generation)
		opened <- result{endpoint: endpoint, err: err}
	}()
	t.Cleanup(func() {
		close(runner.release)
		select {
		case got := <-opened:
			if got.err != nil || got.endpoint == nil {
				t.Error("native startup failed after session-admission proof was released", got.err)
			}
			if got.endpoint != nil {
				if err := got.endpoint.Close(); err != nil {
					t.Error("owned native startup did not clean up", err)
				}
			}

		case <-time.After(5 * time.Second):
			t.Error("native startup did not join")
		}
	})
	select {
	case <-runner.discovered:
	case <-ctx.Done():
		t.Fatal("fixture did not establish its exact native server", ctx.Err())
	}
	select {
	case got := <-opened:
		opened <- got
		t.Fatal("native startup returned before its session readiness was proven", got.err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSharingNativeStartupQueryFailureIsNotReadiness(t *testing.T) {
	ctx, c, _, _ := sharingNativeFixture(t)
	want := errors.New("fixture native session query failed")
	runner := &sessionReadinessRunner{discovered: make(chan struct{}), release: make(chan struct{}), failure: want}
	close(runner.release)
	endpoint, err := (Native{Runner: runner, Command: []string{"/bin/sh", "-i"}}).OpenOwned(ctx,
		domain.Session{ID: "session_startup_query_failed", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, c, "native_startup_query_failed")
	if endpoint != nil {
		defer func() {
			if err := endpoint.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	if !errors.Is(err, want) {
		t.Fatal("native startup hid an unproved session query", err)
	}
}

func TestSharingNativeStartupWaitsForMissingSession(t *testing.T) {
	ctx, c, _, _ := sharingNativeFixture(t)
	runner := &sessionReadinessRunner{discovered: make(chan struct{}), release: make(chan struct{}), missing: 2}
	close(runner.release)
	endpoint, err := (Native{Runner: runner, Command: []string{"/bin/sh", "-i"}}).OpenOwned(ctx,
		domain.Session{ID: "session_startup_missing", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, c, "native_startup_missing")
	if endpoint != nil {
		defer func() {
			if err := endpoint.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	if endpoint == nil || err != nil || runner.missing != 0 {
		t.Fatal("native startup did not wait for missing-session query results", runner.missing, err)
	}
}
