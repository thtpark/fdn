// Fixtureobserver exercises the real stdio observer with synthetic data only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/thtpark/fdn/internal/cli"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

type source struct{}

func (source) Identity(context.Context) (domain.AgentIdentity, error) {
	return domain.AgentIdentity{AgentID: "agent_fixture", InstanceID: "instance_fixture", Generation: 1}, nil
}
func (source) Snapshot(_ context.Context, q observe.Query) (json.RawMessage, error) {
	var value any
	switch q.Kind {
	case "identity":
		value = domain.AgentIdentity{AgentID: "agent_fixture", InstanceID: "instance_fixture", Generation: 1}
	case "agent":
		value = domain.Agent{ID: "agent_fixture", Name: "Observer fixture", Founder: true, Active: true, Mode: "manual", Revision: 1}
	case "task":
		value = domain.Task{ID: q.ID, ProjectID: "project_fixture", Title: "Synthetic observed task", Status: domain.Todo, Revision: 1}
	case "tasks":
		value = domain.Page[domain.Task]{Items: []domain.Task{}, NextCursor: ""}
	default:
		return nil, domain.Fail(domain.Invalid, "query not implemented by isolated protocol fixture")
	}
	return json.Marshal(value)
}
func (source) Updates(ctx context.Context, cursor string, _ domain.UpdateFilter, wait bool) (domain.Page[domain.Update], error) {
	if wait {
		<-ctx.Done()
		return domain.Page[domain.Update]{}, ctx.Err()
	}
	return domain.Page[domain.Update]{Items: []domain.Update{}, NextCursor: "fixture-private"}, nil
}
func (source) Events(ctx context.Context, cursor string, _ domain.UpdateFilter, wait bool) (domain.Page[domain.Event], error) {
	if wait {
		<-ctx.Done()
		return domain.Page[domain.Event]{}, ctx.Err()
	}
	return domain.Page[domain.Event]{Items: []domain.Event{}, NextCursor: "fixture-public"}, nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !slices.Contains(os.Args[1:], "observe") {
		os.Exit(cli.RunAgent(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if err := observe.Run(ctx, os.Stdin, os.Stdout, source{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(domain.ExitCode(err))
	}
}
