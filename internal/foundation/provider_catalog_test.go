package foundation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func publishCatalog(t *testing.T, f *fixture, auth string) domain.ComputerModelOptions {
	t.Helper()
	models := []string{}
	if auth == "authenticated" {
		models = []string{"available-model"}
	}
	c, err := f.s.PublishProviderCatalog(context.Background(), f.worker.ID, domain.ProviderCatalogRequest{
		Command: op(), Observation: domain.ProviderCatalogObservation{Version: domain.ProviderCatalogVersion,
			ObservedAt: f.now.Unix(), Providers: []domain.ProviderModelCatalog{{Provider: "copilot", Version: "1.0.1",
				ObservedAt: f.now.Unix(), Authentication: auth, Models: models}}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProviderCatalogPinsAuthenticatedSelectionToCurrentComputerAndModel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := publishCatalog(t, f, "authenticated")
	options, err := f.s.AgentModelOptions(ctx, f.founder.ID, f.worker.ID)
	if err != nil || len(options.Computers) != 1 || options.Computers[0].BindingID != f.worker.ID ||
		options.Computers[0].Generation != c.Generation || !options.Computers[0].Allows("copilot", "available-model", options.ServerObservedAt) {
		t.Fatal("catalog lost the enrolled computer or authenticated models", options, err)
	}
	provider, model := "copilot", "available-model"
	req := domain.AgentPreferencesRequest{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1},
		Provider: &provider, Model: &model, Reason: "Choose a currently authenticated model",
		Catalog: &domain.ProviderCatalogBinding{BindingID: c.BindingID, Generation: c.Generation}}
	saved, err := f.s.SetAgentPreferences(ctx, f.worker.ID, f.worker.ID, req)
	if err != nil || saved.Model != model || saved.UpdatedBy != f.worker.ID {
		t.Fatal("self-selected authenticated default failed", saved, err)
	}
	for name, change := range map[string]func(*domain.AgentPreferencesRequest){
		"wrong computer":   func(r *domain.AgentPreferencesRequest) { r.Catalog.BindingID = f.other.ID },
		"wrong generation": func(r *domain.AgentPreferencesRequest) { r.Catalog.Generation++ },
		"not in catalog":   func(r *domain.AgentPreferencesRequest) { r.Model = preferenceText("absent-model") },
	} {
		t.Run(name, func(t *testing.T) {
			next := req
			next.OperationID, next.ExpectedRevision = domain.NewID("op"), saved.Revision
			binding := *req.Catalog
			next.Catalog = &binding
			change(&next)
			if _, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, next); err == nil {
				t.Fatal("invalid model selection was accepted")
			}
		})
	}
	publishCatalog(t, f, "login-required")
	req.OperationID, req.ExpectedRevision = domain.NewID("op"), saved.Revision
	_, err = f.s.SetAgentPreferences(ctx, f.worker.ID, f.worker.ID, req)
	assertCode(t, err, domain.Prerequisite)
	current, err := f.s.AgentPreferences(ctx, f.founder.ID, f.worker.ID)
	if err != nil || current != saved {
		t.Fatal("signed-out selection altered a saved default", current, err)
	}
}

func TestProviderCatalogReplayDoesNotRenewOldAuthenticationOrEraseEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := publishCatalog(t, f, "authenticated")
	f.now = f.now.Add(91 * time.Second)
	repeated, err := f.s.PublishProviderCatalog(ctx, f.worker.ID, domain.ProviderCatalogRequest{Command: op(), Observation: c.Observation})
	if err != nil || repeated.ReceivedAt != c.ReceivedAt || !repeated.Stale {
		t.Fatal("a cached observation renewed authentication freshness", repeated, err)
	}
	options, err := f.s.AgentModelOptions(ctx, f.founder.ID, f.worker.ID)
	if err != nil || len(options.Computers) != 1 || !options.Computers[0].Stale ||
		options.Computers[0].Allows("copilot", "available-model", options.ServerObservedAt) ||
		!reflect.DeepEqual(options.Computers[0].Observation, c.Observation) {
		t.Fatal("stale catalog was selected or its diagnostic evidence erased", options, err)
	}
	_, err = f.s.AgentModelOptions(ctx, f.worker.ID, f.founder.ID)
	assertCode(t, err, domain.Denied)
}
