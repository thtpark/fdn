package cli

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (e *Embedded) PublishEnvironment(ctx context.Context) error {
	observation, err := e.res.state.CollectEmbeddedEnvironment(ctx)
	if err != nil {
		return err
	}
	_, err = api.Call[domain.ComputerEnvironment](ctx, e.res.client, "POST",
		api.Path("agents", e.res.client.Credential.AgentID, "environment"),
		domain.EnvironmentRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Observation: observation})
	return err
}
