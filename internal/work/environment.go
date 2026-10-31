package work

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
)

func (e *Engine) inventoryPaths(c config.Worker) (map[string]string, error) {
	if manager, ok := e.Executor.(*provider.Manager); ok {
		return manager.Paths()
	}
	return c.ProviderPaths, nil
}

func (e *Engine) publishEnvironment(ctx context.Context) error {
	c, err := e.currentConfig()
	if err != nil {
		return err
	}
	paths, err := e.inventoryPaths(c)
	if err != nil {
		return err
	}
	observation, err := e.State.CollectEnvironment(ctx, process.Native{}, paths)
	if err != nil {
		return err
	}
	_, err = api.Call[domain.ComputerEnvironment](ctx, e.Client, "POST",
		api.Path("agents", e.Client.Credential.AgentID, "environment"),
		domain.EnvironmentRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Observation: observation})
	return err
}
