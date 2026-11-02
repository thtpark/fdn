package remote

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestOwnedProcessWaitsForDisappearanceAfterSignalPermissionRace(t *testing.T) {
	cmd, _ := legacyProcess(t, "")
	owned, err := inspectProcess(cmd.Process.Pid)
	if err != nil || owned == nil || !owned.valid() {
		t.Fatal("fixture lacks an authenticated owned process group", owned, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	signals := 0
	err = owned.stopWithSignal(ctx, func(group int, signal syscall.Signal) error {
		signals++
		if group != -owned.Group || signal != syscall.SIGKILL {
			t.Fatal("cleanup attempted a signal outside its exact owned group", group, signal)
		}
		if err := syscall.Kill(group, signal); err != nil {
			t.Fatal(err)
		}
		return syscall.EPERM
	})
	if err != nil {
		t.Fatal("a racing permission result stranded the already stopped owned group", err)
	}
	stopped, err := owned.stopped()
	if err != nil || !stopped || signals != 1 {
		t.Fatal("permission result was mistaken for proof or retried a signal", stopped, signals, err)
	}
}

func TestOwnedProcessDeniedSignalCannotProveCleanup(t *testing.T) {
	cmd, _ := legacyProcess(t, "")
	owned, err := inspectProcess(cmd.Process.Pid)
	if err != nil || owned == nil || !owned.valid() {
		t.Fatal("fixture lacks an authenticated owned process group", owned, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := 0
	err = owned.stopWithSignal(ctx, func(group int, signal syscall.Signal) error {
		signals++
		if group != -owned.Group || signal != syscall.SIGKILL {
			t.Fatal("cleanup attempted a signal outside its exact owned group", group, signal)
		}
		cancel()
		return syscall.EPERM
	})
	if !errors.Is(err, syscall.EPERM) || !errors.Is(err, context.Canceled) {
		t.Fatal("persistent permission failure did not retain the bounded cleanup fence", err)
	}
	current, inspectErr := inspectProcess(owned.PID)
	if inspectErr != nil || current == nil || *current != *owned || signals != 1 {
		t.Fatal("denied signal changed or adopted native ownership", current, signals, inspectErr)
	}
}
