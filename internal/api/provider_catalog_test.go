package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestModelOptionsNativeAPIPreservesComputerAuthenticationAndReadPrivacy(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	path := Path("agents", f.target.ID, "model-options")
	now := time.Now().Unix()
	observation := domain.ProviderCatalogObservation{Version: domain.ProviderCatalogVersion, ObservedAt: now,
		Providers: []domain.ProviderModelCatalog{{Provider: "copilot", Version: "1.0.1", Authentication: "authenticated",
			ObservedAt: now, Models: []string{"native-authenticated-model"}}}}
	envelope, code := f.request(t, f.target, http.MethodPost, path, domain.ProviderCatalogRequest{
		Command: domain.Command{OperationID: "model-catalog-native"}, Observation: observation})
	if code != http.StatusOK || !envelope.OK {
		t.Fatal("native catalog publication failed", code, envelope.Error)
	}
	envelope, code = f.request(t, f.founder, http.MethodGet, path, nil)
	var options domain.AgentModelOptions
	if err := json.Unmarshal(envelope.Data, &options); err != nil || code != http.StatusOK || len(options.Computers) != 1 ||
		options.Computers[0].BindingID != f.target.ID || !options.Computers[0].Allows("copilot", "native-authenticated-model", options.ServerObservedAt) {
		t.Fatal("native catalog lost scoped authentication/model facts", options, code, err)
	}
	if _, code := f.request(t, f.founder, http.MethodPost, path, domain.ProviderCatalogRequest{
		Command: domain.Command{OperationID: "forged-model-catalog"}, Observation: observation}); code != http.StatusForbidden {
		t.Fatal("Founder impersonated a target-computer observation", code)
	}
	if _, code := f.request(t, f.target, http.MethodGet, path+"?raw=true", nil); code != http.StatusBadRequest {
		t.Fatal("model discovery accepted unsupported raw selectors", code)
	}
}
