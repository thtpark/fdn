package work

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func (e *Engine) verificationCandidates(ctx context.Context, me domain.Agent) ([]domain.Task, error) {
	projects, err := api.Call[[]domain.Project](ctx, e.Client, http.MethodGet, "/v1/projects", nil)
	if err != nil {
		return nil, err
	}
	items := []domain.Task{}
	for _, project := range projects {
		if project.Archived || (!me.Founder && !slices.Contains(project.Delegates, me.ID)) {
			continue
		}
		cursor := ""
		for {
			path := "/v1/tasks?project=" + project.ID + "&status=completed&limit=200"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			page, err := api.Call[domain.Page[domain.Task]](ctx, e.Client, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			for _, task := range page.Items {
				if task.AssigneeID != me.ID && task.ActiveRun == nil && task.Waiting == nil && !task.Repetitive && task.Result != nil && task.Result.SpecGeneration == task.SpecGeneration && domain.EligibleApprover(me, project, task, true) {
					items = append(items, task)
				}
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
	}
	slices.SortStableFunc(items, func(a, b domain.Task) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		return b.Importance - a.Importance
	})
	return items, nil
}

func (e *Engine) verify(ctx context.Context, c config.Worker, me domain.Agent, task domain.Task, manual bool) error {
	launch, err := e.launch(ctx, domain.VerificationRun, &task)
	if err != nil {
		return err
	}
	if task.AssigneeID == me.ID || !domain.EligibleApprover(me, *launch.Project, task, true) {
		return domain.Fail(domain.Denied, "current independent verification authority is required")
	}
	return e.coding(ctx, c, task, launch, manual)
}

func (e *Engine) guardVerification(ctx context.Context, cancel context.CancelCauseFunc, launch Launch, run domain.Run) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			task, err := api.Call[domain.Task](ctx, e.Client, http.MethodGet, api.Path("tasks", launch.Task.ID), nil)
			if err == nil && (task.ActiveRun == nil || task.ActiveRun.ID != run.ID || task.ActiveRun.StopRequested || task.Status != domain.Completed || task.SpecGeneration != launch.Task.SpecGeneration || task.ResultGeneration != launch.Task.ResultGeneration || task.DiscussionGeneration != launch.Task.DiscussionGeneration) {
				err = domain.Fail(domain.Conflict, "verification ownership, specification, result or discussion changed")
			}
			if err == nil {
				var project domain.Project
				project, err = api.Call[domain.Project](ctx, e.Client, http.MethodGet, api.Path("projects", task.ProjectID), nil)
				if err == nil {
					var me domain.Agent
					me, err = api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
					if err == nil && (project.Epoch != launch.Project.Epoch || me.Revision != run.AgentRevision || !domain.EligibleApprover(me, project, task, true) || (!me.Founder && me.Mode != "ai")) {
						err = domain.Fail(domain.Denied, "verification authority or Founder mode authorization changed")
					}
				}
			}
			if err != nil {
				if domain.AsFault(err).Code == domain.Unavailable {
					e.recordProblem(ctx, "connection-problem", err)
					continue
				}
				cancel(err)
				return
			}
		}
	}
}
