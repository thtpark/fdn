package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
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
