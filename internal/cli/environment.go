package cli

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
)

func (a *app) reportEnvironment(ctx context.Context, r *resources) (any, error) {
	var observation domain.EnvironmentObservation
	key := "environment-observation:" + a.operation
	found, err := r.state.Get(ctx, key, &observation)
	if err != nil {
		return nil, err
	}
	if !found {
		paths, err := provider.New(r.config).Paths()
		if err != nil {
			return nil, err
		}
		observation, err = r.state.CollectEnvironment(ctx, process.Native{}, paths)
		if err != nil {
			return nil, err
		}
		if err := r.state.Put(ctx, key, observation); err != nil {
			return nil, err
		}
	}
	req := domain.EnvironmentRequest{Observation: observation}
	if err := a.metadata(&req.Command); err != nil {
		return nil, err
	}
	return a.write(ctx, r, api.Path("agents", r.client.Credential.AgentID, "environment"), req, "")
}
