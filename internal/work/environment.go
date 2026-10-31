package work

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
)

func (e *Engine) publishEnvironment(ctx context.Context) error {
	observation, err := local.CollectEnvironment(ctx, e.State.Root, process.Native{})
	if err != nil {
		return err
	}
	_, err = api.Call[domain.ComputerEnvironment](ctx, e.Client, "POST",
		api.Path("agents", e.Client.Credential.AgentID, "environment"),
		domain.EnvironmentRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Observation: observation})
	return err
}
