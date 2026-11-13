package upgrade

import (
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeRestartUsesCanonicalDurableProfileStatesWithoutInventingActiveAuthority(t *testing.T) {
	native, _, _, _ := restartFixture(t)
	var identity config.IdentityService
	if err := config.Read(native.Policy.IdentityConfig, &identity); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(identity.StateDir, "connection.json")
	for _, state := range []string{"provisioned", "active", "registered", "revoked", "banned"} {
		profile := config.ConnectionProfile{Identity: domain.AgentIdentity{
			AgentID: native.Policy.TargetID, InstanceID: native.Policy.InstanceID, Generation: 1, State: state}}
		if err := config.Write(path, profile); err != nil {
			t.Fatal(err)
		}
		generation, err := native.restartGeneration()
		if state == "provisioned" || state == "active" {
			if err != nil || generation != 1 {
				t.Fatal("canonical retained profile was mistaken for inactive server authority", state, generation, err)
			}
		} else if err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("unsupported/retired local profile was adopted", state, generation, err)
		}
	}
}
