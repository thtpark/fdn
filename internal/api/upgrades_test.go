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
