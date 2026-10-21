package platform

import (
	"context"
	"errors"
	"slices"
)

// Prepare reserves/creates the ingress-only group and adds only the pinned
// server account to it. Existing processes must be restarted with fresh group
// membership (or systemd SupplementaryGroups); changing /etc/group is not proof
// that an already running unprivileged process can create usable ingress.
func (m *AccountManager) Prepare(ctx context.Context, apply bool) (AccountInventory, error) {
	if err := m.guard(ctx, apply); err != nil {
		return AccountInventory{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, err := m.lockInventory()
	if err != nil {
		return AccountInventory{}, err
	}
	defer lock.Close()
	inventory, err := m.loadInventory()
	if err != nil {
		return AccountInventory{}, err
	}
	if err := m.checkGroup(ctx, inventory); err != nil {
		return inventory, err
	}
	if !inventory.GroupReserved {
		inventory.GroupReserved = true
		if err := m.saveInventory(&inventory); err != nil {
			return inventory, err
		}
	}
	if _, exists, err := m.system.LookupGroup(ctx, m.options.IngressGroup); err != nil {
		return inventory, err
	} else if !exists {
		if err := m.system.CreateGroup(ctx, AccountGroup{Name: m.options.IngressGroup, GID: m.options.IngressGID}); err != nil {
			return inventory, err
		}
	}
	if err := m.ensureServerIngress(ctx); err != nil {
		return inventory, err
	}
	if err := m.checkGroup(ctx, inventory); err != nil {
		return inventory, err
	}
	if m.native {
		if err := m.prepareNativeIngressDirectories(); err != nil {
			return inventory, err
		}
	}
	return inventory, nil
}

func (m *AccountManager) ensureServerIngress(ctx context.Context) error {
	server, exists, err := m.system.LookupUID(ctx, m.options.ServerUID)
	if err != nil || !exists || server.GID != m.options.ServerGID {
		return errors.Join(ErrAccountConflict, err)
	}
	if !slices.Contains(server.Groups, m.options.IngressGID) {
		if err := m.system.GrantServerIngress(ctx, server); err != nil {
			return err
		}
	}
	server, exists, err = m.system.LookupUID(ctx, m.options.ServerUID)
	if err != nil || !exists || !slices.Contains(server.Groups, m.options.IngressGID) {
		return errors.Join(ErrAccountConflict, err)
	}
	return nil
}
