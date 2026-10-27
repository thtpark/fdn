package platform

import (
	"context"
	"errors"
	"testing"
)

func TestManagedAccountGenerationFencesProvisionDisableAndRecovery(t *testing.T) {
	ctx := context.Background()
	manager, system := accountFixture(t)
	firstKey := accountTestKey(t)
	firstPlan, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: firstKey, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Apply(ctx, firstPlan, true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: "agent1", Generation: 2}, true)
	if err != nil || disabled.Generation != 2 || disabled.State != AccountDisabled {
		t.Fatal(disabled, err)
	}
	mutations, lookups := len(system.mutations), system.lookups
	if _, err := manager.Apply(ctx, firstPlan, true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("old provision replay was accepted", err)
	}
	if _, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: firstKey, Generation: 1, Reactivate: true}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal(err)
	}
	if _, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: "agent1", Generation: 1}, true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("old disable replay was accepted", err)
	}
	if mutations != len(system.mutations) || lookups != system.lookups {
		t.Fatal("stale operation invoked OS tools")
	}
	key3 := accountTestKey(t)
	plan3, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: key3, Generation: 3, Reactivate: true})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Apply(ctx, plan3, true)
	if err != nil || recovered.UID != first.UID || recovered.Generation != 3 {
		t.Fatal(recovered, err)
	}
	mutations, lookups = len(system.mutations), system.lookups
	if _, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: "agent1", Generation: 2}, true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal(err)
	}
	if _, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: accountTestKey(t), Generation: 3}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("same-generation key replacement accepted", err)
	}
	if mutations != len(system.mutations) || lookups != system.lookups {
		t.Fatal("stale disable/replacement invoked OS tools")
	}
	if replay, err := manager.Apply(ctx, plan3, true); err != nil || replay.Generation != 3 || len(system.mutations) != mutations {
		t.Fatal("exact same-generation retry was not idempotent", replay, err)
	}
	if _, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: "agent1", Generation: 4}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: key3, Generation: 4, Reactivate: true}); !errors.Is(err, ErrAccountRetired) {
		t.Fatal("same-generation disabled key reinstalled", err)
	}
}

func TestManagedAccountNeverProvisionedDisableRetainsGenerationTombstone(t *testing.T) {
	ctx := context.Background()
	manager, system := accountFixture(t)
	disabled, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: "agent1", Generation: 2}, true)
	if err != nil || disabled.UID != 0 || disabled.Generation != 2 || disabled.State != AccountDisabled || len(system.mutations) != 0 {
		t.Fatal(disabled, err)
	}
	reopened, err := newAccountManager(manager.options, system, manager.filesystemRoot)
	if err != nil {
		t.Fatal(err)
	}
	lookups := system.lookups
	if _, err := reopened.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: accountTestKey(t), Generation: 1, Reactivate: true}); !errors.Is(err, ErrAccountConflict) || system.lookups != lookups {
		t.Fatal("never-provisioned revocation was forgotten", err)
	}
	plan, err := reopened.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: accountTestKey(t), Generation: 3, Reactivate: true})
	if err != nil {
		t.Fatal(err)
	}
	account, err := reopened.Apply(ctx, plan, true)
	if err != nil || account.UID == 0 || account.Generation != 3 {
		t.Fatal(account, err)
	}
}
