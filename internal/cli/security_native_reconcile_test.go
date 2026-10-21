package cli

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/platform"
)

func TestNativeQuarantinedIdentityNeverProvisionsOldOrEmptyKeys(t *testing.T) {
	fixture := nativeFixtureForTest(t)
	identity := domain.AgentIdentity{AgentID: "agent_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", UID: -1, State: "restore-pending"}
	if _, err := provisionNativeIdentity(context.Background(), nil, fixture.client, config.Server{}, identity); domain.AsFault(err).Code != domain.Denied || fixture.client.created != 0 {
		t.Fatal("quarantined restore attempted native provisioning", err)
	}
}

func TestNativeBanDenialPrecedesCleanupAndRecoveryRetainsAccount(t *testing.T) {
	fixture := nativeFixtureForTest(t)
	value, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client)
	if err != nil {
		t.Fatal(err)
	}
	identity := value.(map[string]any)["identity"].(domain.AgentIdentity)
	c, err := config.LoadServer(fixture.app.configFile)
	if err != nil {
		t.Fatal(err)
	}
	store, err := foundation.Open(context.Background(), c.StateDir+"/foundation.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BanIdentity(context.Background(), identity.AgentID, "fixture duplicate identity"); err != nil {
		t.Fatal(err)
	}
	if fixture.client.records[identity.AgentID].State != platform.AccountActive {
		t.Fatal("fixture did not retain the pending native cleanup")
	}
	if _, err := store.ChallengeIdentity(context.Background(), identity.UID); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("application access was not denied before native cleanup", err)
	}
	disabled := map[string]bool{}
	fixture.client.fail = true
	if err := reconcileNativePending(context.Background(), store, fixture.client, c, disabled); err == nil || len(disabled) != 0 {
		t.Fatal("failed native cleanup was acknowledged", err)
	}
	banned, err := store.Identity(context.Background(), identity.AgentID)
	if err != nil || banned.State != "banned" || banned.UID != identity.UID {
		t.Fatal("cleanup uncertainty changed authoritative denial/reservation", banned, err)
	}
	fixture.client.fail = false
	if err := reconcileNativePending(context.Background(), store, fixture.client, c, disabled); err != nil || len(disabled) != 1 {
		t.Fatal("native cleanup replay did not finish", err)
	}
	fixture.client.fail = true
	if err := reconcileNativePending(context.Background(), store, fixture.client, c, disabled); err != nil {
		t.Fatal("completed cleanup was needlessly retried", err)
	}
	fixture.client.fail = false
	public, _, _, err := device.Generate("Recovered Founder")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverFounderOffline(context.Background(), domain.RecoverIdentityRequest{
		Command: domain.Command{OperationID: "native-founder-recovery"}, Reason: "explicit fixture operator recovery", Identity: public,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = provisionNativeIdentity(context.Background(), store, fixture.client, c, recovered)
	if err != nil || recovered.AgentID != identity.AgentID || recovered.UID != identity.UID || recovered.SSHUser != identity.SSHUser || recovered.State != "provisioned" {
		t.Fatal("native recovery did not preserve the immutable account", recovered, err)
	}
}
