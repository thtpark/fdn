package work

import (
	"context"
	"errors"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

type backgroundControlContext struct{}

func (e *Engine) pendingHelpDeliveries(ctx context.Context) (bool, error) {
	authentication, err := e.authenticationRequests(ctx)
	if err != nil {
		return false, err
	}
	for _, request := range authentication {
		if request.State == "delivery-pending" {
			return true, nil
		}
	}
	permission, err := e.permissionRequests(ctx)
	if err != nil {
		return false, err
	}
	for _, request := range permission {
		if request.State == "delivery-pending" {
			return true, nil
		}
	}
	return false, nil
}

// Retry only retained deliveries on a real due timer. This does not select work,
// invoke a provider, acknowledge a message read, or renew a help episode.
func (e *Engine) retryHelpDeliveries(ctx context.Context) error {
	c, err := e.currentConfig()
	if err != nil {
		return err
	}
	var blocked error
	authentication, err := e.authenticationRequests(ctx)
	if err != nil {
		return err
	}
	for _, request := range authentication {
		if request.State != "delivery-pending" {
			continue
		}
		var task *domain.Task
		if request.ProjectID != "" {
			task = &domain.Task{ID: request.TaskID, ProjectID: request.ProjectID, SpecGeneration: request.SpecGeneration}
		}
		actionCtx := context.WithValue(ctx, requestedActionContext{}, request.ActionKey)
		_, err := e.refreshAuthentication(actionCtx, c, request.Provider, task, request.RefreshID)
		blocked = errors.Join(blocked, err)
	}
	permission, err := e.permissionRequests(ctx)
	if err != nil {
		return errors.Join(blocked, err)
	}
	for _, request := range permission {
		if request.State != "delivery-pending" {
			continue
		}
		var task *domain.Task
		if request.TaskID != "" {
			current, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", request.TaskID), nil)
			if err != nil {
				blocked = errors.Join(blocked, err)
				continue
			}
			if current.ProjectID != request.ProjectID {
				blocked = errors.Join(blocked, domain.Fail(domain.Conflict, "pending permission delivery task scope changed"))
				continue
			}
			task = &current
		} else if request.ProjectID != "" {
			task = &domain.Task{ProjectID: request.ProjectID}
		}
		_, err := e.requestPermission(ctx, c, task, request.ActionKey, request.Requirement)
		blocked = errors.Join(blocked, err)
	}
	return blocked
}
