package display

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type foregroundRun struct {
	done   chan struct{}
	cancel context.CancelFunc
	owned  *applicationProcess
	err    error
}

func startForegroundRun(t *testing.T, f *screenFixture) *foregroundRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &foregroundRun{done: make(chan struct{}), cancel: cancel}
	go func() {
		run.err = RunApplication(ctx, f.config, "right", "browser")
		close(run.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			t.Error("foreground application did not join on fixture cancellation")
		}
	})
	waitFor(t, func() bool {
		applications.Lock()
		run.owned = applications.owned[filepath.Join(f.config.StateDir, "right")]
		applications.Unlock()
		select {
		case <-run.done:
			return true
		default:
			return run.owned != nil
		}
	})
	select {
	case <-run.done:
		t.Fatal("foreground application returned before exit/cancellation", run.err)
	default:
	}
	if !strings.HasPrefix(run.owned.owner, "foreground_") {
		t.Fatal("foreground application did not retain distinct caller ownership")
	}
	return run
}

func awaitForegroundRun(t *testing.T, run *foregroundRun) error {
	t.Helper()
	select {
	case <-run.done:
		return run.err
	case <-time.After(5 * time.Second):
		t.Fatal("foreground application did not finish")
		return nil
	}
}

func assertForegroundJoined(t *testing.T, f *screenFixture, run *foregroundRun) {
	t.Helper()
	select {
	case <-run.owned.done:
	default:
		t.Fatal("foreground call returned before reaping the application")
	}
	select {
	case <-run.owned.cleanupDone:
	default:
		t.Fatal("foreground call returned before joining owned cleanup")
	}
	pid := run.owned.cmd.Process.Pid
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		t.Fatal("foreground call returned with an owned process/group still present")
	}
	applications.Lock()
	defer applications.Unlock()
	if applications.owned[filepath.Join(f.config.StateDir, "right")] != nil {
		t.Fatal("foreground call returned before retiring its owned application record")
	}
}

func foregroundExitGate(t *testing.T, f *screenFixture, exitCode int) string {
	t.Helper()
	gate := filepath.Join(f.home, "run.exit")
	writeHelper(t, f.config.Screens[1].Applications[0].Executable, "printf '%s\\n' \"$$\" > app.pid\nprintf 'launch\\n' >> app.launches\nwhile [ ! -f run.exit ]; do /bin/sleep 0.01; done\nexit "+strconv.Itoa(exitCode)+"\n")
	return gate
}

func TestRunApplicationWaitsForExitAndJoinsCleanup(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := screenFixtureFor(t)
			gate := foregroundExitGate(t, f, code)
			run := startForegroundRun(t, f)
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			err := awaitForegroundRun(t, run)
			if code == 0 && err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != code {
					t.Fatal("application exit status was lost", err)
				}
			}
			assertForegroundJoined(t, f, run)
		})
	}
}

func TestRunApplicationCancellationJoinsCleanupBeforeReturning(t *testing.T) {
	f := screenFixtureFor(t)
	run := startForegroundRun(t, f)
	run.cancel()
	if err := awaitForegroundRun(t, run); !errors.Is(err, context.Canceled) {
		t.Fatal("foreground cancellation was not preserved", err)
	}
	assertForegroundJoined(t, f, run)
}

func TestRunApplicationDoesNotAdoptAnotherForegroundOrControllerOwner(t *testing.T) {
	for _, controller := range []bool{false, true} {
		t.Run(strconv.FormatBool(controller), func(t *testing.T) {
			f := screenFixtureFor(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var first *foregroundRun
			if controller {
				if err := Launch(ctx, f.config, "right", "browser"); err != nil {
					t.Fatal(err)
				}
			} else {
				first = startForegroundRun(t, f)
			}
			pid := applicationPID(t, f)
			secondCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			err := RunApplication(secondCtx, f.config, "right", "browser")
			if err == nil || domain.AsFault(err).Code != domain.Conflict || syscall.Kill(pid, 0) != nil {
				t.Fatal("foreground caller adopted or stopped another caller's application", err)
			}
			if controller {
				cancel()
				waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
			} else {
				first.cancel()
				if err := awaitForegroundRun(t, first); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertForegroundJoined(t, f, first)
			}
		})
	}
}

func TestRunApplicationRetainsAmbiguousCleanupErrorAndOwnedRecord(t *testing.T) {
	f := screenFixtureFor(t)
	gate := foregroundExitGate(t, f, 0)
	run := startForegroundRun(t, f)
	t.Cleanup(func() {
		os.WriteFile(gate, nil, 0600)
		<-run.owned.done
		applications.Lock()
		if applications.owned[run.owned.key] == run.owned {
			delete(applications.owned, run.owned.key)
		}
		applications.Unlock()
	})
	uncertain := &process.CleanupError{Err: errors.New("synthetic owned-group cleanup uncertainty")}
	run.owned.once.Do(func() { run.owned.err = uncertain })
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var cleanup *process.CleanupError
	if err := awaitForegroundRun(t, run); !errors.As(err, &cleanup) || cleanup != uncertain {
		t.Fatal("foreground caller hid ambiguous process cleanup", err)
	}
	select {
	case <-run.owned.cleanupDone:
	default:
		t.Fatal("ambiguous foreground cleanup was not joined")
	}
	applications.Lock()
	defer applications.Unlock()
	if applications.owned[run.owned.key] != run.owned {
		t.Fatal("ambiguous cleanup cleared its owned-process fence")
	}
}

func TestRunApplicationRejectsUnboundSelectionsBeforeStarting(t *testing.T) {
	f := screenFixtureFor(t)
	for _, item := range []struct {
		screen, application string
		code                domain.Code
	}{
		{"right", "", domain.Invalid},
		{"missing", "browser", domain.Prerequisite},
		{"right", "left-only", domain.Denied},
	} {
		if err := RunApplication(context.Background(), f.config, item.screen, item.application); err == nil || domain.AsFault(err).Code != item.code {
			t.Fatal("foreground launch bypassed selected-profile validation", item, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "app.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unbound foreground launch started an application", err)
	}
}
