package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/platform"
)

type nativeBindRaceRegistry struct {
	*foundation.Store
	afterBind func() error
}

func (r nativeBindRaceRegistry) BindIdentityAccount(ctx context.Context, id, user string, uid int64, fingerprint string) (domain.AgentIdentity, error) {
	identity, err := r.Store.BindIdentityAccount(ctx, id, user, uid, fingerprint)
	if err == nil && r.afterBind != nil {
		err = r.afterBind()
	}
	return identity, err
}

func TestNativeProvisionRechecksGenerationAfterBind(t *testing.T) {
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
	store, err := foundation.Open(context.Background(), filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := nativeBindRaceRegistry{Store: store, afterBind: func() error {
		return store.BanIdentity(context.Background(), identity.AgentID, "fixture concurrent native bind revocation")
	}}
	if _, err := provisionNativeIdentity(context.Background(), registry, fixture.client, c, identity); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("post-bind revoked generation was returned as provisioned", err)
	}
	banned, err := store.Identity(context.Background(), identity.AgentID)
	if err != nil || banned.State != "banned" || banned.Generation <= identity.Generation {
		t.Fatal(banned, err)
	}
	fixture.client.fail = true
	if _, err := provisionNativeIdentity(context.Background(), store, fixture.client, c, identity); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("stale registration reached the native provisioner instead of current-registry guard", err)
	}
}

func TestNativeFounderDeviceProvisioningAndRevocationUseIndependentBindings(t *testing.T) {
	ctx := context.Background()
	fixture := nativeFixtureForTest(t)
	value, err := fixture.app.initNativeFoundation(ctx, fixture.args, fixture.client)
	if err != nil {
		t.Fatal(err)
	}
	founder := value.(map[string]any)["identity"].(domain.AgentIdentity)
	c, err := config.LoadServer(fixture.app.configFile)
	if err != nil {
		t.Fatal(err)
	}
	store, err := foundation.Open(ctx, filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	signer, err := device.ParseSigner(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.ChallengeIdentity(ctx, founder.UID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(signer, challenge)
	if err != nil {
		t.Fatal(err)
	}
	founder, err = store.ActivateIdentity(ctx, founder.UID, domain.ActivateIdentityRequest{
		Command: domain.Command{OperationID: "activate-original-Founder"}, Nonce: challenge.Nonce, Proof: proof})
	if err != nil {
		t.Fatal(err)
	}
	public, _, _, err := device.Generate("Founder Phone")
	if err != nil {
		t.Fatal(err)
	}
	phone, err := store.RegisterFounderDevice(ctx, founder.AgentID, domain.RegisterIdentityRequest{
		Command: domain.Command{OperationID: "register-native-founder-phone"}, Identity: public})
	if err != nil {
		t.Fatal(err)
	}
	fixture.client.fail = true
	if _, err := provisionNativeIdentity(ctx, store, fixture.client, c, phone); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("lost native acknowledgement was not explicit", err)
	}
	fixture.client.fail = false
	provisioned, err := provisionNativeIdentity(ctx, store, fixture.client, c, phone)
	if err != nil || provisioned.DeviceID != phone.DeviceID || provisioned.AgentID != founder.AgentID || provisioned.UID == founder.UID || fixture.client.created != 2 {
		t.Fatal("device provisioning reused Founder account or lost replay", provisioned, err)
	}
	revoked, err := store.RevokeFounderDevice(ctx, founder.AgentID, phone.DeviceID, domain.RevokeDeviceRequest{
		Command: domain.Command{OperationID: "revoke-native-founder-phone"}, Reason: "Withdraw just this device"})
	if err != nil {
		t.Fatal(err)
	}
	if err := disableNativeIdentity(ctx, fixture.client, revoked); err != nil {
		t.Fatal(err)
	}
	if fixture.client.records[phone.DeviceID].State != platform.AccountDisabled ||
		fixture.client.records[founder.AgentID].State != platform.AccountActive {
		t.Fatal("native device revocation changed the original Founder account")
	}
	if _, err := provisionNativeIdentity(ctx, store, fixture.client, c, phone); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("old provisioning resurrected a revoked device", err)
	}
}
