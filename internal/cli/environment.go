package cli

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
)

func (a *app) reportEnvironment(ctx context.Context, r *resources) (any, error) {
	var observation domain.EnvironmentObservation
	key := "environment-observation:" + a.operation
	found, err := r.state.Get(ctx, key, &observation)
	if err != nil {
		return nil, err
	}
	if !found {
		observation, err = local.CollectEnvironment(ctx, r.config.StateDir, process.Native{})
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
