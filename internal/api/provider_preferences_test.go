package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentPreferencesNativeAPIEnforcesFounderOrSelfAndRetainsExactDefaults(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	target := messageFixtureClient(t, f.server, f.target.ID)
	delegate := messageFixtureClient(t, f.server, f.delegate.ID)
	path := Path("agents", f.target.ID, "preferences")
	initial, err := Call[domain.ProviderPreferences](ctx, target, http.MethodGet, path, nil)
	if err != nil || initial.Provider != "" || initial.Revision != 1 {
		t.Fatal("unset defaults were not explicit", initial, err)
	}
	name, model := "copilot", "native-model"
	request := domain.AgentPreferencesRequest{
		Command:  domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: initial.Revision},
		Provider: &name, Model: &model, Reason: "Founder configures the next session's defaults.",
	}
	for _, caller := range []*Client{delegate} {
		if _, err := Call[domain.ProviderPreferences](ctx, caller, http.MethodPost, path, request); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("a worker/delegate changed agent defaults", err)
		}
	}
	before, err := Call[domain.Agent](ctx, target, http.MethodGet, "/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Call[domain.ProviderPreferences](ctx, founder, http.MethodPost, path, request)
	if err != nil || saved.Model != model || saved.Revision != 2 {
		t.Fatal("native DTO lost the preference", saved, err)
	}
	replayed, err := Call[domain.ProviderPreferences](ctx, founder, http.MethodPost, path, request)
	if err != nil || replayed != saved {
		t.Fatal("native replay changed preferences", replayed, err)
	}
	after, err := Call[domain.Agent](ctx, target, http.MethodGet, "/v1/me", nil)
	if err != nil || after.Preferences == nil || *after.Preferences != saved ||
		after.Revision != before.Revision || after.Mode != before.Mode {
		t.Fatal("default preference was absent or changed authority/mode", after, err)
	}
	for _, field := range []string{"updated_by", "revision", "updated_at", "founder"} {
		envelope, code := f.request(t, f.founder, http.MethodPost, path, map[string]any{
			"operation_id": domain.NewID("op"), "expected_revision": saved.Revision,
			"provider": "codex", "reason": "Invalid protected field", field: "forged",
		})
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("raw preferences accepted a protected field", field, code, envelope)
		}
	}
}
