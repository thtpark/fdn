package foundation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestSecurityNewAccountsUseAndReserveAgentNames(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	worker, _, _ := f.register(t, "Agent1", 6101)
	if f.founder.SSHUser != "founder" || worker.SSHUser != "agent1" {
		t.Fatal("account names were not derived from agent names", f.founder.SSHUser, worker.SSHUser)
	}
	for _, name := range []string{"AGENT1", "Agent1."} {
		keys := securityPublic(t, name)
		_, err := f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: keys.public})
		assertCode(t, err, domain.Conflict)
	}
	if err := f.s.BanIdentity(ctx, worker.AgentID, "retain the account reservation"); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: securityPublic(t, "agent1").public})
	assertCode(t, err, domain.Conflict)
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM agents").Scan(&count); err != nil || count != 2 {
		t.Fatal("rejected names left partial identities", count, err)
	}
}

func TestSecurityInvalidNewAccountNamesDoNotLeaveIdentityState(t *testing.T) {
	ctx := context.Background()
	f := securityEmptyStore(t)
	for _, name := range []string{"Root", "123", "東京", strings.Repeat("a", 33), "Agent\nOne"} {
		_, err := f.s.BootstrapIdentity(ctx, securityPublic(t, name).public, op().OperationID)
		assertCode(t, err, domain.Invalid)
		var count int
		if err := f.s.db.QueryRow("SELECT count(*) FROM agents").Scan(&count); err != nil || count != 0 {
			t.Fatal("invalid name left a partial Founder", count, err)
		}
	}
	founder, err := f.s.BootstrapIdentity(ctx, securityPublic(t, "Founder").public, op().OperationID)
	if err != nil || founder.SSHUser != "founder" {
		t.Fatal("invalid attempts prevented valid initialization", founder, err)
	}
}

func TestSecurityExplicitAccountBindingSurvivesReopenAndRecovery(t *testing.T) {
	ctx := context.Background()
	f := securityEmptyStore(t)
	keys := securityPublic(t, "Founder")
	identity, err := f.s.BootstrapIdentity(ctx, keys.public, op().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	retained := "reserved-founder"
	tx, finish, err := f.s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity.SSHUser = retained
	if err := saveIdentity(ctx, tx, identity); err != nil {
		finish()
		t.Fatal(err)
	}
	if err := f.s.commit(ctx, tx); err != nil {
		finish()
		t.Fatal(err)
	}
	finish()
	identity, _ = securityActivate(t, f.s, identity, keys, 6101)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	fresh := securityPublic(t, "Different recovery display name")
	recovered, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Identity: fresh.public, Reason: "explicit legacy recovery"})
	if err != nil || recovered.SSHUser != retained {
		t.Fatal("recovery recomputed a retained account name", recovered, err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, identity.UID)
	if recovered.UID != identity.UID || recovered.SSHUser != identity.SSHUser {
		t.Fatal("legacy account binding changed", recovered)
	}
}
