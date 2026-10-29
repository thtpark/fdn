package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func legacyResetFixture(t *testing.T) (config.Worker, domain.Session, SessionFenceSnapshot) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fdn-reset-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c := config.DefaultWorker(filepath.Join(root, "s"))
	if err := secure.Dir(filepath.Join(c.StateDir, "sessions")); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ID: "session_legacy", TargetID: "agent_owned", State: "closed", Mode: "tmux"}
	fence := SessionFenceSnapshot{Fence: Fence{Active: true, ID: session.ID, Generation: "fence_legacy"}, Fingerprint: domain.Hash("exact legacy fence")}
	return c, session, fence
}

func orphanLegacySocket(t *testing.T, path string) os.FileInfo {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestExplicitResetCleansLegacyOrphanSocketAndRetainsEvidence(t *testing.T) {
	for _, generation := range []string{"fence_legacy", ""} {
		t.Run(generation, func(t *testing.T) {
			c, session, snapshot := legacyResetFixture(t)
			snapshot.Fence.Generation = generation
			socket := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
			before := orphanLegacySocket(t, socket)
			native := Native{}
			if err := native.ResetSession(context.Background(), session, c, snapshot); err != nil {
				t.Fatal("Founder reset stranded a recoverable old-format orphan socket", err)
			}
			if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("original endpoint was not retired", err)
			}
			name := domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]
			retired := filepath.Join(c.StateDir, "sessions", "retired", name+".sock")
			after, err := os.Lstat(retired)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("reset discarded instead of preserving the exact legacy socket", err)
			}
			var proof legacyReset
			if err := config.Read(filepath.Join(c.StateDir, "sessions", name+".legacy-reset.json"), &proof); err != nil ||
				proof.State != "cleaned" || proof.Fingerprint != snapshot.Fingerprint || proof.TargetID != session.TargetID || proof.Generation != generation {
				t.Fatal("target-native cleanup was not durably bound to this exact fence", proof, err)
			}
			if err := native.ResetSession(context.Background(), session, c, snapshot); err != nil {
				t.Fatal("native cleanup proof could not be replayed", err)
			}
			orphanLegacySocket(t, socket)
			if err := native.ResetSession(context.Background(), session, c, snapshot); err == nil {
				t.Fatal("retained proof adopted a replacement endpoint")
			}
		})
	}
}

func TestExplicitResetDoesNotReuseProofForReappearedSocket(t *testing.T) {
	c, session, snapshot := legacyResetFixture(t)
	socket := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
	orphanLegacySocket(t, socket)
	if err := (Native{}).ResetSession(context.Background(), session, c, snapshot); err != nil {
		t.Fatal(err)
	}
	name := domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]
	retired := filepath.Join(c.StateDir, "sessions", "retired", name+".sock")
	if err := os.Rename(retired, socket); err != nil {
		t.Fatal(err)
	}
	if err := (Native{}).ResetSession(context.Background(), session, c, snapshot); err == nil {
		t.Fatal("old stopped proof was reused after its retired endpoint reappeared")
	}
}

func TestExplicitResetHandlesAbsentLegacyEndpointWithoutGuessingProcesses(t *testing.T) {
	c, session, snapshot := legacyResetFixture(t)
	if err := (Native{}).ResetSession(context.Background(), session, c, snapshot); err != nil {
		t.Fatal("kernel-confirmed missing legacy endpoint should not require operator recovery", err)
	}
}

func TestExplicitResetRejectsUnsafeLegacyResources(t *testing.T) {
	for _, scenario := range []string{"symlink", "regular-file", "public-socket", "live-other-endpoint", "unlinked-live-endpoint", "current-format", "different-generation", "live-session", "different-session", "different-target", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			c, session, snapshot := legacyResetFixture(t)
			path := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
			ctx := context.Background()
			switch scenario {
			case "symlink":
				if err := os.Symlink(filepath.Join(c.StateDir, "unrelated"), path); err != nil {
					t.Fatal(err)
				}
			case "regular-file":
				if err := secure.Write(path, []byte("unrelated private data")); err != nil {
					t.Fatal(err)
				}
			case "public-socket":
				orphanLegacySocket(t, path)
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "live-other-endpoint", "unlinked-live-endpoint":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				t.Cleanup(func() { listener.Close() })
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "unlinked-live-endpoint" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			case "current-format":
				_, socket, err := sessionNativePaths(c, session.ID, snapshot.Fence.Generation)
				if err != nil {
					t.Fatal(err)
				}
				orphanLegacySocket(t, socket)
			case "different-generation":
				path, _, err := sessionNativePaths(c, session.ID, "fence_other")
				if err != nil {
					t.Fatal(err)
				}
				if err := writeNativeSession(path, nativeSession{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: "fence_other", State: "running"}); err != nil {
					t.Fatal(err)
				}
			case "live-session":
				session.State = "active"
			case "different-session":
				snapshot.Fence.ID = "session_other"
			case "different-target":
				name := domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]
				record := legacyReset{Version: 1, SessionID: session.ID, TargetID: "agent_other", Fingerprint: snapshot.Fingerprint, Generation: snapshot.Fence.Generation, State: "cleaned"}
				if err := writeLegacyReset(filepath.Join(c.StateDir, "sessions", name+".legacy-reset.json"), record, true); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := (Native{}).ResetSession(ctx, session, c, snapshot); err == nil {
				t.Fatal("reset treated unsafe resources as stopped cleanup", scenario)
			}
			if _, err := os.Stat(filepath.Join(c.StateDir, "sessions", "retired", domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]+".sock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe legacy endpoint was moved", err)
			}
		})
	}
}

func TestLegacyResetEndpointProcess(t *testing.T) {
	path := os.Getenv("FDN_LEGACY_RESET_SOCKET")
	if path == "" {
		return
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	pane := exec.Command("/bin/sleep", "60")
	pane.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := pane.Start(); err != nil {
		t.Fatal(err)
	}
	go pane.Wait()
	fmt.Println("ready:" + strconv.Itoa(pane.Process.Pid))
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			io.Copy(io.Discard, conn)
		}()
	}
}

func legacyProcess(t *testing.T, socket string) (*exec.Cmd, int) {
	t.Helper()
	var cmd *exec.Cmd
	if socket != "" {
		cmd = exec.Command(os.Args[0], "-test.run=^TestLegacyResetEndpointProcess$")
		cmd.Env = append(os.Environ(), "FDN_LEGACY_RESET_SOCKET="+socket)
	} else {
		cmd = exec.Command("/bin/sleep", "60")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-done
	})
	panePID := 0
	if socket != "" {
		ready := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stdout).ReadString('\n')
			ready <- strings.TrimSpace(line)
		}()
		select {
		case line := <-ready:
			if !strings.HasPrefix(line, "ready:") {
				t.Fatal("owned Unix endpoint helper did not start")
			}
			var err error
			panePID, err = strconv.Atoi(strings.TrimPrefix(line, "ready:"))
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("owned Unix endpoint helper did not become ready")
		}
		pane, err := inspectProcess(panePID)
		if err != nil || pane == nil || !pane.valid() {
			t.Fatal("fixture server did not create its dedicated pane", pane, err)
		}
		t.Cleanup(func() {
			current, err := inspectProcess(panePID)
			if err == nil && current != nil && *current == *pane {
				syscall.Kill(-pane.Group, syscall.SIGKILL)
			}
		})
	}
	return cmd, panePID
}

type legacyTmuxRunner struct {
	server, pane int
	change       func()
	kills        int
	calls        []string
}

func (r *legacyTmuxRunner) Run(_ context.Context, spec process.Spec) error {
	if len(spec.Args) < 3 || spec.Args[0] != "-S" {
		return errors.New("unexpected legacy native command")
	}
	r.calls = append(r.calls, spec.Args[2])
	switch spec.Args[2] {
	case "list-sessions":
		if r.change != nil {
			r.change()
		}
		_, err := fmt.Fprintf(spec.Stdout, "foundation:%d\n", r.server)
		return err
	case "list-clients":
		return nil
	case "list-panes":
		if r.pane > 0 {
			_, err := fmt.Fprintf(spec.Stdout, "%d\n", r.pane)
			return err
		}
		return nil
	case "kill-server":
		r.kills++
		return syscall.Kill(r.server, syscall.SIGTERM)
	}
	return errors.New("unexpected legacy tmux action")
}

func TestExplicitResetStopsOnlyKernelAuthenticatedLegacyResources(t *testing.T) {
	c, session, snapshot := legacyResetFixture(t)
	socket := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
	server, panePID := legacyProcess(t, socket)
	unrelated, _ := legacyProcess(t, "")
	runner := &legacyTmuxRunner{server: server.Process.Pid, pane: panePID}
	if err := (Native{Runner: runner}).ResetSession(context.Background(), session, c, snapshot); err != nil {
		t.Fatal("explicit reset could not clean its authenticated old-format endpoint", err, runner.calls)
	}
	for _, pid := range []int{server.Process.Pid, panePID} {
		observed, err := inspectProcess(pid)
		if err != nil || observed != nil {
			t.Fatal("owned native resource did not join", observed, err)
		}
	}
	if err := syscall.Kill(unrelated.Process.Pid, 0); err != nil || runner.kills != 1 {
		t.Fatal("reset changed unrelated work or omitted exact native shutdown", err, runner.kills)
	}
}

func TestExplicitResetRejectsSocketReplacementBeforeAnySignals(t *testing.T) {
	c, session, snapshot := legacyResetFixture(t)
	socket := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
	server, panePID := legacyProcess(t, socket)
	runner := &legacyTmuxRunner{server: server.Process.Pid, pane: panePID, change: func() {
		if err := os.Rename(socket, socket+".original"); err != nil {
			t.Fatal(err)
		}
		orphanLegacySocket(t, socket)
	}}
	if err := (Native{Runner: runner}).ResetSession(context.Background(), session, c, snapshot); err == nil {
		t.Fatal("replaced socket was adopted")
	}
	if runner.kills != 0 || syscall.Kill(server.Process.Pid, 0) != nil || syscall.Kill(panePID, 0) != nil {
		t.Fatal("reset signalled processes before revalidating endpoint ownership")
	}
}

func TestExplicitResetRejectsUnrelatedPIDReportedAsLegacyPane(t *testing.T) {
	c, session, snapshot := legacyResetFixture(t)
	socket := filepath.Join(c.StateDir, "sessions", session.ID+".sock")
	server, _ := legacyProcess(t, socket)
	unrelated, _ := legacyProcess(t, "")
	runner := &legacyTmuxRunner{server: server.Process.Pid, pane: unrelated.Process.Pid}
	if err := (Native{Runner: runner}).ResetSession(context.Background(), session, c, snapshot); err == nil {
		t.Fatal("tmux-reported PID was accepted without its kernel parent relationship")
	}
	if runner.kills != 0 || syscall.Kill(server.Process.Pid, 0) != nil || syscall.Kill(unrelated.Process.Pid, 0) != nil {
		t.Fatal("reset signalled an unrelated process or its server before verifying scope")
	}
}
