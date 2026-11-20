package work

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func (e *Engine) initializeLocalModelOperation(ctx context.Context, c config.Worker, operation domain.AgentOperation, me domain.Agent) (bool, error) {
	if operation.LocalModel == nil {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "configuration-unavailable", nil)
	}
	remaining := operation.ExpiresAt - operationCapturedAt(ctx)
	if remaining <= 1 {
		return false, e.recordAgentOperationResult(ctx, operation, me, "expired", "operation-expired", nil)
	}
	snapshot, err := e.State.WorkSnapshot(ctx, []string{"maintenance"})
	if err != nil {
		return false, err
	}
	if len(snapshot.Journals) != 0 {
		code := "work-pending"
		for _, journal := range snapshot.Journals {
			if journal.State == "invoking" || journal.State == "uncertain" {
				code = "invocation-uncertain"
				break
			}
		}
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", code, nil)
	}
	if snapshot.PendingReports != 0 {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "report-pending", nil)
	}
	var maintenance struct {
		Active bool `json:"active"`
	}
	if data := snapshot.Values["maintenance"]; len(data) != 0 {
		if err := json.Unmarshal(data, &maintenance); err != nil {
			return false, err
		}
	}
	if maintenance.Active {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "maintenance-active", nil)
	}

	executor := e.Executor
	if manager, ok := executor.(*provider.Manager); ok {
		copyMgr := *manager
		copyMgr.Config = c
		executor = &copyMgr
	}
	var initializer provider.LocalModelInitializer
	if impl, ok := executor.(provider.LocalModelInitializer); ok {
		initializer = impl
	} else {
		initializer = provider.New(c)
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(remaining-1)*time.Second)
	defer cancel()

	initRes, initErr := initializer.InitializeLocalModel(opCtx, *operation.LocalModel)
	obs := &domain.LocalModelObservation{
		Provider:       "local",
		RuntimeVersion: initRes.RuntimeVersion,
		Family:         operation.LocalModel.Family,
		Model:          initRes.Model,
		Quantization:   initRes.Quantization,
		MemorySource:   initRes.Memory.Source,
		MemoryBytes:    initRes.Memory.TotalBytes,
		RequiredBytes:  initRes.RequiredBytes,
		GPUCount:       initRes.Memory.GPUCount,
	}
	if initErr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if domain.AsFault(initErr).Code == domain.Conflict {
			return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "slot-occupied", nil)
		}
		if initRes.MemoryInsufficient {
			return false, e.recordAgentOperationResultWithLocalModel(ctx, operation, me, "rejected", "memory-insufficient", nil, obs)
		}
		return false, e.recordAgentOperationResultWithLocalModel(ctx, operation, me, "operator-required", "local-model-unavailable", nil, obs)
	}

	catalog, err := provider.CollectCatalog(ctx, executor, e.Now)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, e.recordAgentOperationResultWithLocalModel(ctx, operation, me, "operator-required", "local-model-unavailable", nil, obs)
	}
	computer, err := commit[domain.ComputerModelOptions](ctx, e, http.MethodPost,
		api.Path("agents", e.Client.Credential.AgentID, "model-options"),
		domain.ProviderCatalogRequest{
			Command:     domain.Command{OperationID: operation.ID + ".model-catalog"},
			Observation: catalog,
		})
	if err != nil {
		return false, err
	}
	if err := e.State.Put(ctx, providerCatalogStateKey, catalog); err != nil {
		return false, err
	}
	obs.CatalogPublished = true
	_ = e.resolveAuthentication(ctx, "local")

	if operation.LocalModel.SetPreference {
		prefPath := api.Path("agents", e.Client.Credential.AgentID, "preferences")
		current, err := api.Call[domain.ProviderPreferences](ctx, e.Client, http.MethodGet, prefPath, nil)
		if err != nil {
			return false, err
		}
		providerName := "local"
		modelName := initRes.Model
		if _, err := commit[domain.ProviderPreferences](ctx, e, http.MethodPost, prefPath, domain.AgentPreferencesRequest{
			Command:  domain.Command{OperationID: operation.ID + ".preferences", ExpectedRevision: current.Revision},
			Provider: &providerName,
			Model:    &modelName,
			Reason:   operation.Reason,
			Catalog:  &domain.ProviderCatalogBinding{BindingID: computer.BindingID, Generation: computer.Generation},
		}); err != nil {
			return false, err
		}
		obs.PreferenceApplied = true
	}

	if err := e.recordAgentOperationResultWithLocalModel(ctx, operation, me, "initialized", "local-model-initialized", nil, obs); err != nil {
		return false, err
	}
	return true, nil
}
