package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

func sharingNativeFixture(t *testing.T) (context.Context, config.Worker, *local.State, *nativeSharingBackend) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("native tmux is unavailable")
	}
	root, err := os.MkdirTemp("/tmp", "fdn-share-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c := config.DefaultWorker(filepath.Join(root, "state"))
	state, err := local.Open(ctx, c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	backend := newNativeSharingBackend(ctx)
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	return ctx, c, state, backend
}

func readNativeSharing(t *testing.T, endpoint io.ReadWriteCloser, marker string) {
	t.Helper()
	type result struct {
		err    error
		output string
	}
	done := make(chan result, 1)
	go func() {
		var output strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := endpoint.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				if strings.Contains(output.String(), marker) {
					done <- result{}
					return
				}
			}
			if err != nil {
				tail := output.String()
				if len(tail) > len(buffer) {
					tail = tail[len(tail)-len(buffer):]
				}
				done <- result{err: err, output: tail}
				return
			}
		}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			if attachment, ok := endpoint.(*sharingAttachment); ok {
				current, inspectErr := inspectProcess(attachment.identity.PID)
				t.Logf("attachment identity: expected=%+v current=%+v inspection=%v", attachment.identity, current, inspectErr)
				select {
				case <-attachment.done:
					t.Log("attachment process has exited")
				default:
					t.Log("attachment process has not joined")
				}
			}
			t.Fatalf("native terminal output was not observed: %v; fixture output tail=%q", got.err, got.output)
		}
	case <-time.After(5 * time.Second):
		closeErr := endpoint.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned terminal reader did not join")
		}
		t.Fatal("native terminal output timed out", closeErr)
	}
}

func TestSharingNativeAttachmentDoesNotOwnTheProducerOrExecutionSlot(t *testing.T) {
	ctx, c, state, backend := sharingNativeFixture(t)
	inventory, err := backend.Inventory(ctx, c, state)
	if err != nil {
		t.Fatal(err)
	}
	shell := inventory[0]
	lock, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := backend.Open(ctx, c, state, domain.Session{ID: "viewer_first", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, shell)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	second, err := backend.Open(ctx, c, state, domain.Session{ID: "viewer_second", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, shell)
	if err != nil {
		first.Close()
		lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(second, "printf 'FDN_NATIVE_%s\\n' 'PRODUCER_ALIVE'\n"); err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, second, "FDN_NATIVE_PRODUCER_ALIVE")
	if len(backend.managed) != 1 {
		t.Fatal("viewers created independent terminal producers")
	}
}

func TestSharingNativeExecutionMirrorStreamsActualPrivateBytesAndClosesOnlyItsTail(t *testing.T) {
	ctx, c, state, backend := sharingNativeFixture(t)
	meta := domain.Terminal{ID: "execution", Name: "Execution output", Generation: "invocation_fixture", Kind: "execution"}
	if err := state.Put(ctx, "sharing-execution", meta); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(c.StateDir, "invocations", meta.Generation)
	for _, name := range []string{"stdout.log", "stderr.log"} {
		if err := secure.Create(filepath.Join(directory, name), nil); err != nil {
			t.Fatal(err)
		}
	}
	output, err := os.OpenFile(filepath.Join(directory, "stdout.log"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if _, err := output.WriteString("FDN_EXECUTION_BEFORE_VIEW\n"); err != nil {
		t.Fatal(err)
	}
	endpoint, err := backend.Open(ctx, c, state, domain.Session{ID: "viewer_execution", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, endpoint, "FDN_EXECUTION_BEFORE_VIEW")
	if _, err := output.WriteString("FDN_EXECUTION_LIVE_APPEND\n"); err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, endpoint, "FDN_EXECUTION_LIVE_APPEND")
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteString("FDN_EXECUTION_STILL_WRITABLE\n"); err != nil {
		t.Fatal("closing observation affected the producer", err)
	}
	next := meta
	next.Generation = "invocation_next"
	if err := state.Put(ctx, "sharing-execution", next); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Inventory(ctx, c, state); err != nil {
		t.Fatal(err)
	}
	if len(backend.managed) != 0 {
		t.Fatal("retired execution tail was kept after switching invocation generation")
	}
}

func TestSharingNativeStaleSelectionAndInputCleanupDoNotDestroyNewerTerminals(t *testing.T) {
	ctx, c, state, backend := sharingNativeFixture(t)
	inventory, err := backend.Inventory(ctx, c, state)
	if err != nil {
		t.Fatal(err)
	}
	shell := inventory[0]
	session := domain.Session{ID: "handoff_fixture", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing-input", TerminalID: shell.ID}
	endpoint, err := backend.Open(ctx, c, state, session, shell)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, sharingInputKey(session.ID), sharingInputRecord{Session: session, Terminal: shell}); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.EndInput(ctx, c, state, session); err != nil {
		t.Fatal(err)
	}
	current, err := backend.Inventory(ctx, c, state)
	if err != nil {
		t.Fatal(err)
	}
	if current[0].Generation == shell.Generation {
		t.Fatal("closed input terminal generation was reused")
	}
	_, err = backend.Open(ctx, c, state, session, shell)
	if err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("stale native terminal generation attached", err)
	}
	newer, err := backend.Open(ctx, c, state, session, current[0])
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	if err := backend.EndInput(ctx, c, state, session); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(newer, "printf 'FDN_NEWER_%s\\n' 'TERMINAL_PRESERVED'\n"); err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, newer, "FDN_NEWER_TERMINAL_PRESERVED")
	if err := backend.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
