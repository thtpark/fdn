package work

import (
	"context"
	"net/http"
	"slices"

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

func (e *Engine) guardVerification(ctx context.Context, cancel context.CancelCauseFunc, launch Launch, run domain.Run) error {
	return e.guardAuthority(ctx, cancel, &authoritySubscription{taskID: launch.Task.ID, projectID: launch.Task.ProjectID}, func(ctx context.Context) error {
		task, err := api.Call[domain.Task](ctx, e.Client, http.MethodGet, api.Path("tasks", launch.Task.ID), nil)
		if err != nil {
			return err
		}
		if task.ActiveRun == nil || task.ActiveRun.ID != run.ID || task.ActiveRun.StopRequested || task.Status != domain.Completed || task.SpecGeneration != launch.Task.SpecGeneration || task.ResultGeneration != launch.Task.ResultGeneration || task.DiscussionGeneration != launch.Task.DiscussionGeneration {
			return domain.Fail(domain.Conflict, "verification ownership, specification, result or discussion changed")
		}
		project, err := api.Call[domain.Project](ctx, e.Client, http.MethodGet, api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
		if err != nil {
			return err
		}
		if me.ID != e.Client.Credential.AgentID || !me.Active || me.Banned || task.AssigneeID == me.ID || project.Archived || project.Epoch != launch.Project.Epoch || me.Revision != run.AgentRevision || !domain.EligibleApprover(me, project, task, true) || (!me.Founder && me.Mode != "ai") {
			return domain.Fail(domain.Denied, "verification authority or Founder mode authorization changed")
		}
		return nil
	})
}
