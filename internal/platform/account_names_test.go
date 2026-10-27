package platform

import (
	"context"
	"errors"
	"testing"
)

func TestManagedAgentNameAccountsRetainNameUIDAndGenerationFences(t *testing.T) {
	ctx := context.Background()
	manager, system := accountFixture(t)
	request := AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: accountTestKey(t), Generation: 1}
	plan, err := manager.Plan(ctx, request)
	if err != nil || plan.Username != "agent1" {
		t.Fatal(plan, err)
	}
	first, err := manager.Apply(ctx, plan, true)
	if err != nil || first.Username != "agent1" {
		t.Fatal(first, err)
	}
	mutations := len(system.mutations)
	if replay, err := manager.Apply(ctx, plan, true); err != nil || replay.UID != first.UID || len(system.mutations) != mutations {
		t.Fatal("named account retry was not idempotent", replay, err)
	}
	if _, err := manager.Disable(ctx, AccountDisableRequest{AgentID: first.AgentID, Username: first.Username, Generation: 2}, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := newAccountManager(manager.options, system, manager.filesystemRoot)
	if err != nil {
		t.Fatal(err)
	}
	mutations, lookups := len(system.mutations), system.lookups
	changed := request
	changed.Username, changed.PublicKey, changed.Generation, changed.Reactivate = "renamed", accountTestKey(t), 3, true
	if _, err := reopened.Plan(ctx, changed); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("recovery renamed a retained account", err)
	}
	if _, err := reopened.Disable(ctx, AccountDisableRequest{AgentID: first.AgentID, Username: "renamed", Generation: 3}, true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("disable changed a retained account name", err)
	}
	if _, err := reopened.Plan(ctx, AccountRequest{AgentID: accountTestOther, Username: first.Username, PublicKey: accountTestKey(t), Generation: 1}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("another identity reused a retired name", err)
	}
	if mutations != len(system.mutations) || lookups != system.lookups {
		t.Fatal("conflicting name reached OS operations")
	}
	changed.Username = first.Username
	recovery, err := reopened.Plan(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Apply(ctx, recovery, true)
	if err != nil || recovered.UID != first.UID || recovered.Username != first.Username || recovered.Generation != 3 {
		t.Fatal("fresh-key recovery changed the retained identity", recovered, err)
	}
}

func TestManagedAgentNameTombstonesAndExplicitAccountsCoexist(t *testing.T) {
	ctx := context.Background()
	manager, system := accountFixture(t)
	legacyPlan, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "reserved-agent", PublicKey: accountTestKey(t), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := manager.Apply(ctx, legacyPlan, true)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestOther, Username: "agent2", Generation: 2}, true)
	if err != nil || disabled.Username != "agent2" || disabled.UID != 0 || disabled.State != AccountDisabled {
		t.Fatal("named tombstone did not retain the reservation", disabled, err)
	}
	reopened, err := newAccountManager(manager.options, system, manager.filesystemRoot)
	if err != nil {
		t.Fatal(err)
	}
	lookups := system.lookups
	if _, err := reopened.Plan(ctx, AccountRequest{AgentID: accountTestOther, Username: "agent2", PublicKey: accountTestKey(t), Generation: 1}); !errors.Is(err, ErrAccountConflict) || system.lookups != lookups {
		t.Fatal("stale request bypassed a named tombstone", err)
	}
	namedPlan, err := reopened.Plan(ctx, AccountRequest{AgentID: accountTestOther, Username: "agent2", PublicKey: accountTestKey(t), Generation: 3, Reactivate: true})
	if err != nil {
		t.Fatal(err)
	}
	named, err := reopened.Apply(ctx, namedPlan, true)
	if err != nil || named.Username != "agent2" || named.UID == legacy.UID {
		t.Fatal(named, err)
	}
	if _, err := reopened.Reconcile(ctx, true); err != nil {
		t.Fatal("legacy/named inventory did not reconcile", err)
	}
	inventory, err := reopened.Inventory()
	if err != nil || len(inventory.Accounts) != 2 || inventory.Accounts[0].Username != legacy.Username || inventory.Accounts[0].UID != legacy.UID {
		t.Fatal("legacy account was renamed or replaced", inventory, err)
	}
	duplicate := inventory.Accounts[1]
	duplicate.AgentID = legacy.AgentID
	inventory.Accounts[1] = duplicate
	if err := reopened.validateInventory(inventory); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("two names represented the same immutable agent", err)
	}
}

func TestManagedAgentNameAccountsRejectUnmanagedAndUnsafeNames(t *testing.T) {
	ctx := context.Background()
	manager, system := accountFixture(t)
	key := accountTestKey(t)
	for _, name := range []string{"root", "Agent1", "-agent", "agent;id", "../agent", "agent\nUser=root"} {
		lookups := system.lookups
		if _, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: name, PublicKey: key, Generation: 1}); err == nil || system.lookups != lookups {
			t.Fatal("unsafe name reached OS lookup", name, err)
		}
		if _, err := manager.Disable(ctx, AccountDisableRequest{AgentID: accountTestAgent, Username: name, Generation: 1}, true); err == nil || system.lookups != lookups {
			t.Fatal("unsafe disable name reached OS lookup", name, err)
		}
	}
	system.users["agent1"] = AccountUser{Username: "agent1", UID: 9001, Marker: "unmanaged"}
	if _, err := manager.Plan(ctx, AccountRequest{AgentID: accountTestAgent, Username: "agent1", PublicKey: key, Generation: 1}); !errors.Is(err, ErrAccountConflict) || len(system.mutations) != 0 {
		t.Fatal("existing OS account was adopted", err)
	}
}
