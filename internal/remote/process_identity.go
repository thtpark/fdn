package remote

import (
	"context"
	"errors"
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
	if !p.valid() {
		return false, domain.Fail(domain.Prerequisite, "session cleanup has no exact owned process-group identity")
	}
	current, err := inspectProcess(p.PID)
	if err != nil {
		return false, err
	}
	if current != nil && *current != p {
		return false, domain.Fail(domain.Conflict, "session process identity changed; an unrelated process was not adopted")
	}
	err = syscall.Kill(-p.Group, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}

func (p ownedProcess) stop(ctx context.Context) error {
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
		return err
	}
	if current == nil {
		return p.waitStopped(ctx)
	}
	if *current != p {
		return domain.Fail(domain.Prerequisite, "session process-group ownership cannot be revalidated")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syscall.Kill(-p.Group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return p.waitStopped(ctx)
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
