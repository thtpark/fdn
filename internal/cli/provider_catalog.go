package cli

import (
	"context"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func (a *app) reportProviderCatalog(ctx context.Context, r *resources) (any, error) {
	var observation domain.ProviderCatalogObservation
	key := "provider-catalog-observation:" + a.operation
	found, err := r.state.Get(ctx, key, &observation)
	if err != nil {
		return nil, err
	}
	if !found {
		observation, err = provider.CollectCatalog(ctx, provider.New(r.config), time.Now)
		if err != nil {
			return nil, err
		}
		if err := r.state.Put(ctx, key, observation); err != nil {
			return nil, err
		}
	}
	req := domain.ProviderCatalogRequest{Observation: observation}
	if err := a.metadata(&req.Command); err != nil {
		return nil, err
	}
	return a.write(ctx, r, api.Path("agents", r.client.Credential.AgentID, "model-options"), req, "")
}
