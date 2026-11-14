package work

import (
	"context"
	"net/http"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

const providerCatalogStateKey = "provider-model-catalog"

func (e *Engine) publishProviderCatalog(ctx context.Context) error {
	catalog, err := provider.CollectCatalog(ctx, e.Executor, e.Now)
	if err != nil {
		return err
	}
	if _, err := api.Call[domain.ComputerModelOptions](ctx, e.Client, http.MethodPost,
		api.Path("agents", e.Client.Credential.AgentID, "model-options"),
		domain.ProviderCatalogRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Observation: catalog}); err != nil {
		return err
	}
	return e.State.Put(ctx, providerCatalogStateKey, catalog)
}

func (e *Engine) refreshModelContext(ctx context.Context, launch *Launch) error {
	var catalog domain.ProviderCatalogObservation
	found, err := e.State.Get(ctx, providerCatalogStateKey, &catalog)
	if err != nil {
		return err
	}
	if found && catalog.ObservedAt > 0 && e.Now().Unix() >= catalog.ObservedAt &&
		e.Now().Unix()-catalog.ObservedAt <= domain.ProviderCatalogFreshSeconds {
		launch.ModelOptions = &catalog
	}
	return nil
}

func (e *Engine) applyPreferredModel(ctx context.Context, journal *local.Journal, choice *provider.PreferenceChoice) error {
	if choice == nil {
		return nil
	}
	key := "preferred-model-catalog:" + journal.ID
	var request domain.ProviderCatalogRequest
	found, err := e.State.Get(ctx, key, &request)
	if err != nil {
		return err
	}
	if !found {
		health, err := e.Executor.Check(ctx, choice.Provider)
		if err != nil {
			return err
		}
		if health.Authentication != "authenticated" {
			return domain.Fail(domain.Prerequisite, "an agent-selected default requires a currently authenticated provider")
		}
		observed := e.Now().UTC().Unix()
		catalog := domain.ProviderModelCatalog{Provider: choice.Provider, Version: health.Version,
			Authentication: health.Authentication, ObservedAt: observed, Models: []string{}}
		if health.ModelCatalogError != "" {
			catalog.ModelProblem = domain.Unavailable
		} else {
			catalog.Models = append(catalog.Models, health.Models...)
		}
		request = domain.ProviderCatalogRequest{Command: domain.Command{OperationID: journal.ID + ".model-catalog"},
			Observation: domain.ProviderCatalogObservation{Version: domain.ProviderCatalogVersion, ObservedAt: observed,
				Providers: []domain.ProviderModelCatalog{catalog}}}
		if err := domain.ValidateProviderCatalog(request.Observation); err != nil {
			return err
		}
		if err := e.State.Put(ctx, key, request); err != nil {
			return err
		}
	}
	computer, err := commit[domain.ComputerModelOptions](ctx, e, http.MethodPost,
		api.Path("agents", e.Client.Credential.AgentID, "model-options"), request)
	if err != nil {
		return err
	}
	path := api.Path("agents", e.Client.Credential.AgentID, "preferences")
	current, err := api.Call[domain.ProviderPreferences](ctx, e.Client, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	update := domain.AgentPreferencesRequest{Command: domain.Command{OperationID: journal.ID + ".preferred-model", ExpectedRevision: current.Revision},
		Provider: &choice.Provider, Model: &choice.Model, Reason: choice.Reason,
		Catalog: &domain.ProviderCatalogBinding{BindingID: computer.BindingID, Generation: computer.Generation}}
	if _, err := commit[domain.ProviderPreferences](ctx, e, http.MethodPost, path, update); err != nil {
		return err
	}
	return nil
}
