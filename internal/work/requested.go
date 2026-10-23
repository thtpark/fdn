package work

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

type requestedActionContext struct{}

// WithRequestedAction identifies an explicit caller request, not an authorization.
func WithRequestedAction(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, requestedActionContext{}, operation)
}

func requestedAction(ctx context.Context, launch Launch) string {
	if launch.ActionKey != "" {
		return launch.ActionKey
	}
	if operation, _ := ctx.Value(requestedActionContext{}).(string); operation != "" {
		return "explicit:" + operation
	}
	if launch.Message != nil {
		return fmt.Sprintf("message:%s:%d", launch.Message.ID, launch.Message.Revision)
	}
	if launch.Attention != nil {
		return fmt.Sprintf("attention:%s:%d", launch.Attention.EntryID, launch.Attention.Generation)
	}
	if task := launch.Task; task != nil {
		return fmt.Sprintf("task:%s:%d:%d:%d:%s", task.ID, task.SpecGeneration, task.DiscussionGeneration, task.ResultGeneration, task.LastRunID)
	}
	return ""
}

func actionContext(ctx context.Context, launch Launch) (context.Context, Launch) {
	launch.ActionKey = requestedAction(ctx, launch)
	return WithRequestedAction(ctx, launch.ActionKey), launch
}

func (e *Engine) requestedAuthentication(ctx context.Context, c config.Worker, me domain.Agent) error {
	tasks, err := e.assigned(ctx, me.ID)
	if err != nil {
		return err
	}
	launches := []Launch{}
	for index := range tasks {
		task := &tasks[index]
		if task.Status != domain.Todo || task.ArchivedAt != 0 || task.Repetitive || task.ActiveRun != nil {
			continue
		}
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		if !project.Archived {
			launches = append(launches, Launch{Task: task})
		}
	}
	cursor := ""
	for {
		path := "/v1/messages?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			return err
		}
		for index := range page.Items {
			message := &page.Items[index]
			if message.Kind == "direct" && message.ResponseRequest {
				launches = append(launches, Launch{Message: message})
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	attention, err := api.Call[domain.Page[domain.Attention]](ctx, e.Client, "GET", "/v1/attention?limit=200", nil)
	if err != nil {
		return err
	}
	for index := range attention.Items {
		item := &attention.Items[index]
		task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", item.TaskID), nil)
		if err != nil {
			return err
		}
		launches = append(launches, Launch{Task: &task, Attention: item})
	}
	reviews, err := e.verificationCandidates(ctx, me)
	if err != nil {
		return err
	}
	for index := range reviews {
		launches = append(launches, Launch{Task: &reviews[index], Phase: domain.VerificationRun})
	}
	var blocked error
	for _, launch := range launches {
		actionCtx, launch := actionContext(ctx, launch)
		if err := e.phaseHealth(actionCtx, "preparing", &launch, "", 120, false); err != nil {
			return err
		}
		providers := []string{c.Provider}
		if launch.Phase != domain.VerificationRun && launch.Task != nil && launch.Task.Provider != "" && launch.Task.Provider != c.Provider {
			providers = append(providers, launch.Task.Provider)
		}
		for _, name := range providers {
			if _, _, err := e.selectProvider(actionCtx, c, name, "", launch.Task); err != nil {
				blocked = errors.Join(blocked, err)
			}
		}
	}
	return blocked
}

type helpEscalated struct{ error }

func (e helpEscalated) Unwrap() error { return e.error }

func (e *Engine) permissionBlocked(ctx context.Context, c config.Worker, launch Launch, requirement string, cause error) error {
	var previous helpEscalated
	if errors.As(cause, &previous) {
		return cause
	}
	key := requestedAction(ctx, launch)
	if key == "" {
		key = "local:" + requirement
	}
	blocker := "work-prerequisite"
	switch requirement {
	case "workspace":
		blocker = "workspace-permission"
	case "provider-permission":
		blocker = "provider-permission"
	case "retry-approval":
		blocker = "approval-required"
	}
	healthErr := e.blockHealth(ctx, blocker)
	_, err := e.requestPermission(ctx, c, launch.Task, key, requirement)
	return helpEscalated{errors.Join(cause, err, healthErr)}
}

func permissionFailure(err error) bool {
	return errors.Is(err, fs.ErrPermission) || domain.AsFault(err).Code == domain.Denied
}

func (e *Engine) invocationBlocked(ctx context.Context, c config.Worker, j local.Journal, launch Launch, cause error) error {
	ctx, launch = actionContext(ctx, launch)
	health, err := e.Executor.Check(ctx, j.Provider)
	if observedErr := e.providerHealth(ctx, j.Provider, j.Model, health, err); observedErr != nil {
		return observedErr
	}
	if err != nil {
		if permissionFailure(err) {
			return e.permissionBlocked(ctx, c, launch, "provider-permission", err)
		}
		return nil
	}
	if slices.Contains([]string{"login-required", "credentials-rejected"}, health.Authentication) {
		if launch.Phase == "onboarding" {
			me, err := api.Call[domain.Agent](ctx, e.Client, "GET", "/v1/me", nil)
			if err != nil {
				return err
			}
			if err := e.requestedAuthentication(ctx, c, me); err != nil {
				return err
			}
		}
		_, err := e.requestAuthentication(ctx, c, j.Provider, launch.Task)
		return err
	}
	if j.WorkspaceRef != "" {
		if _, err := e.State.Workspace(ctx, j.WorkspaceRef); err != nil {
			return e.permissionBlocked(ctx, c, launch, "workspace", err)
		}
	}
	if permissionFailure(cause) {
		return e.permissionBlocked(ctx, c, launch, "provider-permission", cause)
	}
	return nil
}
