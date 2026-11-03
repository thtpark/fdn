package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type ownedProcess struct {
	PID     int    `json:"pid"`
	Group   int    `json:"group"`
	UID     uint32 `json:"uid"`
	Started string `json:"started"`
}

func inspectProcess(pid int) (*ownedProcess, error) {
	p, _, err := inspectProcessWithParent(pid)
	return p, err
}

func (p ownedProcess) valid() bool {
	return p.PID > 1 && p.Group == p.PID && p.UID == uint32(os.Geteuid()) && p.Started != ""
}

func (p ownedProcess) stopped() (bool, error) {
	return p.stoppedWithProbe(inspectProcess, func(group int) error { return syscall.Kill(-group, 0) })
}

func (p ownedProcess) stoppedWithProbe(inspect func(int) (*ownedProcess, error), probe func(int) error) (bool, error) {
	if !p.valid() {
		return false, domain.Fail(domain.Prerequisite, "session cleanup has no exact owned process-group identity")
	}
	groupErr := probe(p.Group)
	if errors.Is(groupErr, syscall.ESRCH) {
		return true, nil
	}
	if groupErr != nil && !errors.Is(groupErr, syscall.EPERM) {
		return false, groupErr
	}
	current, err := inspect(p.PID)
	if err != nil {
		return false, errors.Join(err, groupErr)
	}
	if current != nil && *current != p {
		return false, domain.Fail(domain.Conflict, "session process identity changed; an unrelated process was not adopted")
	}
	return false, groupErr
}

func (p ownedProcess) stop(ctx context.Context) error {
	return p.stopWithSignal(ctx, syscall.Kill)
}

func (p ownedProcess) stopWithSignal(ctx context.Context, signal func(int, syscall.Signal) error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stopped, err := p.stopped()
		if stopped || err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-ticker.C:
		}
	}
	current, err := inspectProcess(p.PID)
	if err != nil {
		return fmt.Errorf("recheck owned session process identity before signalling: %w", err)
	}
	if current == nil {
		return p.waitStopped(ctx)
	}
	if *current != p {
		return p.waitStopped(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	signalErr := signal(-p.Group, syscall.SIGKILL)
	if signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) && !errors.Is(signalErr, syscall.EPERM) {
		return fmt.Errorf("signal owned session process group: %w", signalErr)
	}
	// A racing Darwin shutdown can return EPERM; only disappearance proves cleanup.
	if err := p.waitStopped(ctx); err != nil {
		return errors.Join(signalErr, err)
	}
	return nil
}

func (p ownedProcess) waitStopped(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		stopped, err := p.stopped()
		// Darwin can report EPERM while the signalled group is being reaped.
		// Wait for actual disappearance; permission failure is never proof.
		if stopped || err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-ticker.C:
		}
	}
}
