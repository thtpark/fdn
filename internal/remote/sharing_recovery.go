package remote

import (
	"context"
	"errors"
	"path/filepath"
	"slices"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func (m *Manager) recoverSharingInput(ctx context.Context, backend SharingBackend) (err error) {
	snapshot, err := ReadSessionFence(ctx, m.State)
	if err != nil || !snapshot.Fence.Active {
		return err
	}
	var lease sharingInputRecord
	found, err := m.State.Get(ctx, sharingInputKey(snapshot.Fence.ID), &lease)
	if err != nil || !found {
		return err
	}
	c, err := m.currentConfig()
	if err != nil {
		return err
	}
	slot, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	return m.recoverSharingInputLocked(ctx, backend, c, snapshot)
}

func (m *Manager) recoverSharingInputLocked(ctx context.Context, backend SharingBackend, c config.Worker, expected SessionFenceSnapshot) error {
	snapshot, err := ReadSessionFence(ctx, m.State)
	if err != nil || !snapshot.Fence.Active {
		return err
	}
	if snapshot.Fingerprint != expected.Fingerprint {
		return domain.Fail(domain.Conflict, "sharing input fence changed before exclusive cleanup ownership")
	}
	var lease sharingInputRecord
	found, err := m.State.Get(ctx, sharingInputKey(snapshot.Fence.ID), &lease)
	if err != nil {
		return err
	}
	if !found || lease.Session.ID != snapshot.Fence.ID || lease.Fence.Generation != snapshot.Fence.Generation ||
		lease.OwnerPID <= 1 || lease.OwnerStart == "" {
		return domain.Fail(domain.Prerequisite, "sharing input recovery evidence does not match the exact retained fence")
	}
	if lease.State == "operator-required" {
		return domain.Fail(domain.Prerequisite, "interrupted input was cleaned but its effects require Founder inspection and exact repair")
	}
	owner, err := inspectProcess(lease.OwnerPID)
	if err != nil {
		return err
	}
	orphan := owner == nil || owner.Started != lease.OwnerStart
	if !orphan && lease.State != "closing" {
		return domain.Fail(domain.Prerequisite, "a live input owner has not acknowledged closure; its resources were not interrupted")
	}
	current, err := api.Call[domain.Session](ctx, m.Client, "GET", api.Path("sessions", lease.Session.ID), nil)
	if err != nil {
		return err
	}
	if slices.Contains([]string{"consented", "active"}, current.State) {
		if _, err := m.sharingChange(ctx, current, "close"); err != nil {
			return err
		}
	}
	if !snapshot.Fence.CleanupProven {
		if err := backend.EndInput(ctx, c, m.State, lease.Session); err != nil {
			return err
		}
	}
	if orphan && lease.State != "closing" {
		if err := m.State.ProveSessionCleanup(ctx, snapshot.Fence.ID, snapshot.Fingerprint); err != nil {
			return err
		}
		lease.State = "operator-required"
		return m.State.Put(ctx, sharingInputKey(snapshot.Fence.ID), lease)
	}
	return m.finish(ctx, lease.Session.ID)
}
