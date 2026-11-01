package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestUpgradeAPIContextIsFounderOnlyAndStrictlyReadOnly(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx := context.Background()
	instance, err := f.store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, actor := range []domain.Agent{f.target, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodGet, "/v1/upgrades/context", nil)
		if status != http.StatusForbidden || envelope.Error.Code != domain.Denied {
			t.Fatal("project delegation granted software upgrade discovery", status, envelope)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodGet, "/v1/upgrades/context", nil)
	if status != 200 || !envelope.OK {
		t.Fatal("Founder software context failed", status, envelope)
	}
	client := messageFixtureClient(t, f.server, f.founder.ID)
	result, err := Call[domain.UpgradeContext](ctx, client, "GET", "/v1/upgrades/context", nil)
	if err != nil || result.InstanceID != instance || len(result.Agents) != 3 {
		t.Fatal("native protobuf software context differs from JSON contract", result, err)
	}
	for _, route := range []string{"/v1/upgrades/context?force=true", "/v1/upgrades/context?limit=1"} {
		envelope, status := f.request(t, f.founder, http.MethodGet, route, nil)
		if status != http.StatusBadRequest || envelope.Error.Code != domain.Invalid {
			t.Fatal("software context accepted unsupported query selectors", status, envelope)
		}
	}
	envelope, status = f.request(t, f.target, http.MethodPost, "/v1/upgrades/authorize", domain.UpgradeAuthorizationRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
	})
	if status != http.StatusForbidden || envelope.Error.Code != domain.Denied {
		t.Fatal("ordinary target reached native upgrade authorization", status, envelope)
	}
}

func TestTemporaryUpgradeMaintenanceAPIIsFounderOnlyAndStrictlyScoped(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	request := domain.UpgradeMaintenanceRequest{Command: domain.Command{OperationID: domain.NewID("op")}, ID: domain.NewID("upgrade")}
	for _, actor := range []domain.Agent{f.target, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodPost, "/v1/upgrades/maintenance", request)
		if status != http.StatusForbidden || envelope.Error.Code != domain.Denied {
			t.Fatal("non-Founder acquired a temporary mode-restoration grant", status, envelope)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/upgrades/maintenance?force=true", request)
	if status != http.StatusBadRequest || envelope.Error.Code != domain.Invalid {
		t.Fatal("maintenance accepted a force/query side channel", status, envelope)
	}
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/upgrades/maintenance", request)
	if status != http.StatusBadRequest || envelope.Error.Code != domain.Invalid {
		t.Fatal("maintenance accepted an unbound/empty proposal batch", status, envelope)
	}
	client := messageFixtureClient(t, f.server, f.founder.ID)
	_, err := Call[domain.UpgradeMaintenance](context.Background(), client, http.MethodPost, "/v1/upgrades/maintenance", request)
	if domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("native maintenance DTO/route differed from its strict API input", err)
	}
}
