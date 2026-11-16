package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

func improvementProject(policy domain.ImprovementPolicy, id string) bool {
	return id == domain.FoundationProjectID || slices.Contains(policy.ProjectIDs, id)
}

func reserveDue(usage domain.MaintenanceUsage, now int64) bool {
	policy := usage.Policy
	return policy.Enabled && usage.AgentSeconds+policy.SliceSeconds <= policy.PerAgentSeconds &&
		usage.FleetSeconds+policy.SliceSeconds <= policy.FleetSeconds &&
		now-usage.WindowStart >= int64(usage.AgentSeconds)*int64(policy.WindowSeconds)/int64(policy.PerAgentSeconds)
}

func (e *Engine) improvementStep(ctx context.Context, c config.Worker, manual bool, policy domain.ImprovementPolicy, tasks []domain.Task) (bool, error) {
	if !policy.Enabled {
		return false, nil
	}
	me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if !improvementProject(policy, task.ProjectID) || task.ArchivedAt != 0 || task.Repetitive || task.ActiveRun != nil ||
			(task.Status != domain.Todo && task.Status != domain.Paused) {
			continue
		}
		acted, err := e.prepare(ctx, c, task, manual)
		if err != nil {
			if slices.Contains([]domain.Code{domain.Prerequisite, domain.Conflict, domain.Denied}, domain.AsFault(err).Code) && !acted {
				continue
			}
			return acted, err
		}
		if acted {
			return true, nil
		}
	}
	projects, err := api.Call[[]domain.Project](ctx, e.Client, http.MethodGet, "/v1/projects", nil)
	if err != nil {
		return false, err
	}
	for _, project := range projects {
		if project.Archived || !improvementProject(policy, project.ID) || (!me.Founder && !slices.Contains(project.Delegates, me.ID)) {
			continue
		}
		path := "/v1/improvements?project=" + url.QueryEscape(project.ID) + "&state=proposed&limit=50"
		page, err := api.Call[domain.Page[domain.Improvement]](ctx, e.Client, http.MethodGet, path, nil)
		if err != nil {
			return false, err
		}
		for _, request := range page.Items {
			launch, err := e.launch(ctx, "improvement-triage", nil)
			if err != nil {
				return false, err
			}
			launch.Improvement, launch.Project = &request, &project
			agents, err := api.Call[[]domain.Agent](ctx, e.Client, http.MethodGet, "/v1/agents", nil)
			if err != nil {
				return false, err
			}
			for _, agent := range agents {
				if agent.Active && !agent.Banned && len(launch.CandidateAgents) < 32 {
					launch.CandidateAgents = append(launch.CandidateAgents, agent)
				}
			}
			_, err = e.text(ctx, c, launch, manual)
			return true, err
		}
		candidates, err := api.Call[domain.Page[domain.Task]](ctx, e.Client, http.MethodGet,
			"/v1/tasks?project="+url.QueryEscape(project.ID)+"&status=todo&limit=200", nil)
		if err != nil {
			return false, err
		}
		for _, task := range candidates.Items {
			if task.AssigneeID == me.ID || task.AssigneeID == "" || task.WorkspaceRef == "" || task.Readiness != nil ||
				task.ActiveRun != nil || task.Waiting != nil || !domain.EligibleApprover(me, project, task, false) {
				continue
			}
			key := fmt.Sprintf("%d:%d:%d", task.SpecGeneration, task.DiscussionGeneration, project.Epoch)
			var previous string
			_, err := e.State.Get(ctx, "improvement-readiness:"+task.ID, &previous)
			if err != nil {
				return false, err
			}
			if previous == key {
				continue
			}
			launch, err := e.launch(ctx, "improvement-readiness", &task)
			if err != nil {
				return false, err
			}
			_, err = e.text(ctx, c, launch, manual)
			return true, err
		}
	}
	reviews, err := e.verificationCandidates(ctx, me)
	if err != nil {
		return false, err
	}
	for _, task := range reviews {
		if improvementProject(policy, task.ProjectID) {
			return true, e.verify(ctx, c, me, task, manual)
		}
	}
	return false, nil
}

func (e *Engine) reserveImprovement(ctx context.Context, launch *Launch) error {
	project := ""
	if launch.Project != nil {
		project = launch.Project.ID
	}
	if project == "" {
		return nil
	}
	policy, err := api.Call[domain.ImprovementPolicy](ctx, e.Client, http.MethodGet, "/v1/improvement-policy", nil)
	if err != nil {
		return err
	}
	if !improvementProject(policy, project) {
		return nil
	}
	kind, source := "preparation", ""
	if launch.Improvement != nil {
		kind, source = "triage", launch.Improvement.ID
	} else if launch.Task != nil {
		source = launch.Task.ID
		switch launch.Phase {
		case domain.ExecutionRun, domain.VerificationRun, domain.RecoveryRun:
			kind = launch.Phase
		case "improvement-readiness":
			kind = "readiness"
		}
	}
	if source == "" {
		return domain.Fail(domain.Internal, "improvement invocation omitted its scoped source")
	}
	request := domain.MaintenanceRequest{Command: domain.Command{OperationID: domain.NewID("op")},
		ProjectID: project, SourceID: source, Kind: kind, Seconds: policy.SliceSeconds}
	reservation, err := api.Call[domain.MaintenanceReservation](ctx, e.Client, http.MethodPost, "/v1/maintenance-reservations", request)
	if err != nil {
		return err
	}
	launch.Maintenance = &reservation
	return nil
}

func (e *Engine) settleImprovement(ctx context.Context, launch Launch) error {
	if launch.Maintenance == nil {
		return nil
	}
	request := domain.MaintenanceSettlement{Command: domain.Command{OperationID: launch.Maintenance.ID + ".settle"}}
	_, err := api.Call[domain.MaintenanceReservation](ctx, e.Client, http.MethodPost,
		api.Path("maintenance-reservations", launch.Maintenance.ID, "settle"), request)
	return err
}

func (e *Engine) applyImprovementText(ctx context.Context, journal *local.Journal, launch Launch, reply provider.Reply) error {
	if launch.Phase == "improvement-triage" {
		if launch.Improvement == nil || reply.Improvement == nil || reply.Decision != "" || reply.Action != nil || len(reply.Questions) != 0 {
			return domain.Fail(domain.Provider, "triage requires its exact bounded improvement choice, not execution or task verification")
		}
		choice := reply.Improvement
		request := domain.ImprovementDecision{Command: domain.Command{OperationID: journal.ID + ".triage", ExpectedRevision: launch.Improvement.Revision},
			Decision: choice.Decision, Reason: reply.Summary, AssigneeID: choice.AssigneeID, DuplicateOf: choice.DuplicateOf, Source: choice.Source}
		if _, err := api.Call[domain.ImprovementResult](ctx, e.Client, http.MethodPost,
			api.Path("improvements", launch.Improvement.ID, "decision"), request); err != nil {
			return err
		}
	} else {
		if launch.Task == nil || launch.Project == nil || reply.Improvement != nil || reply.Decision != "" || reply.Action != nil {
			return domain.Fail(domain.Provider, "readiness is an exact independent task specification review")
		}
		if reply.Outcome == "clear" {
			request := domain.TaskChange{Command: domain.Command{OperationID: journal.ID + ".readiness", ExpectedRevision: launch.Task.Revision},
				Kind: "approve", Reason: reply.Summary, SpecGeneration: launch.Task.SpecGeneration}
			if _, err := api.Call[domain.Task](ctx, e.Client, http.MethodPost, api.Path("tasks", launch.Task.ID, "change"), request); err != nil {
				return err
			}
		} else {
			request := domain.EntryRequest{Command: domain.Command{OperationID: journal.ID + ".readiness-feedback"},
				Kind: "comment", Body: reply.Summary}
			if _, err := api.Call[domain.Entry](ctx, e.Client, http.MethodPost, api.Path("tasks", launch.Task.ID, "entries"), request); err != nil {
				return err
			}
		}
		key := fmt.Sprintf("%d:%d:%d", launch.Task.SpecGeneration, launch.Task.DiscussionGeneration, launch.Project.Epoch)
		if err := e.State.Put(ctx, "improvement-readiness:"+launch.Task.ID, key); err != nil {
			return err
		}
	}
	journal.State = "done"
	return e.State.SaveJournal(ctx, *journal)
}

func improvementTimeout(launch Launch, maximum time.Duration) time.Duration {
	if launch.Maintenance == nil {
		return maximum
	}
	return min(maximum, time.Duration(launch.Maintenance.Seconds)*time.Second)
}

func improvementContent(task domain.Task) string {
	if task.Source == nil && task.BuildID == "" {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Source  *domain.TaskSource `json:"source"`
		BuildID string             `json:"build_id"`
	}{task.Source, task.BuildID})
	return string(raw)
}

func settleAfterImprovement(e *Engine, launch Launch, err *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	*err = errors.Join(*err, e.settleImprovement(ctx, launch))
}
