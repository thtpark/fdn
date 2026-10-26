package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

type preparation struct {
	Stage        string `json:"stage"`
	ContentHash  string `json:"content_hash"`
	LastReview   string `json:"last_review"`
	SignoffSpec  int64  `json:"signoff_spec"`
	SignoffEpoch int64  `json:"signoff_epoch"`
}

func contentHash(task domain.Task) string {
	data, _ := json.Marshal(struct {
		Title, Description, Criteria, Assignee, Provider, Model string
		Policy                                                  domain.ApprovalPolicy
		Retry                                                   domain.RetryPolicy
	}{task.Title, task.Description, task.AcceptanceCriteria, task.AssigneeID, task.Provider, task.Model, task.Policy, task.RetryPolicy})
	return domain.Hash(string(data))
}

func reviewKey(task domain.Task, phase string) string {
	return fmt.Sprintf("%s:%s:%d", phase, contentHash(task), task.DiscussionGeneration)
}

func (e *Engine) prepare(ctx context.Context, c config.Worker, task domain.Task, manual bool) (bool, error) {
	launch, err := e.launch(ctx, "scope", &task)
	if err != nil {
		return false, err
	}
	if err := e.phaseHealth(ctx, "preparing", &launch, "", 120, false); err != nil {
		return false, err
	}
	project := *launch.Project
	if project.Archived {
		return false, domain.Fail(domain.Prerequisite, "project is archived")
	}
	ready := task.Readiness != nil && task.Readiness.SpecGeneration == task.SpecGeneration && task.Readiness.ProjectEpoch == project.Epoch
	if task.Waiting != nil {
		switch task.Waiting.Kind {
		case "clarification":
			if !hasAnswers(task, launch.Discussion) {
				return false, errors.Join(domain.Fail(domain.Prerequisite, "awaiting replies from the requested respondents"), e.waitingActivity(ctx, "clarification"))
			}
		case "retry":
			if task.NextAttemptAt > e.Now().Unix() {
				deferWorkUntil(ctx, time.Unix(task.NextAttemptAt, 0))
				return false, errors.Join(domain.Fail(domain.Prerequisite, "retry backoff has not elapsed"), e.blockHealth(ctx, "retry-wait"))
			}
			if task.UnsafeRetry && (task.RetryApproval == nil || task.RetryApproval.SpecGeneration != task.SpecGeneration || task.RetryApproval.ProjectEpoch != project.Epoch || task.RetryApproval.RunID != task.LastRunID || task.RetryApproval.Attempt != task.Attempts+1) {
				if err := e.waitingActivity(ctx, "retry"); err != nil {
					return false, err
				}
				cause := domain.Fail(domain.Prerequisite, "exact unsafe-retry approval is required")
				return false, e.permissionBlocked(ctx, c, launch, "retry-approval", cause)
			}
			if task.Failures >= task.RetryPolicy.MaxRetries+1 {
				return false, domain.Fail(domain.Prerequisite, "approved failure retry budget is exhausted")
			}
		default:
			return false, domain.Fail(domain.Prerequisite, "recorded waiting condition is unresolved")
		}
	}
	if ready || task.Waiting != nil {
		if ready {
			if _, err := e.State.Workspace(ctx, task.WorkspaceRef); err != nil {
				return false, e.permissionBlocked(ctx, c, launch, "workspace", err)
			}
			if err := e.resolvePermission(ctx, requestedAction(ctx, launch), "workspace"); err != nil {
				return false, err
			}
		}
		if task.Clarity == nil || task.Clarity.SpecGeneration != task.SpecGeneration || task.Clarity.DiscussionGeneration != task.DiscussionGeneration || task.Clarity.ProjectEpoch != project.Epoch || task.Waiting != nil {
			launch.Phase = "preflight"
			_, err := e.text(ctx, c, launch, manual)
			return true, err
		}
		if ready {
			return true, e.execute(ctx, c, task, launch, manual)
		}
	}
	var prep preparation
	_, err = e.State.Get(ctx, "preparation:"+task.ID, &prep)
	if err != nil {
		return false, err
	}
	if prep.Stage == "" || prep.ContentHash != contentHash(task) {
		prep = preparation{Stage: "scope", ContentHash: contentHash(task)}
	}
	if prep.Stage == "signoff" {
		task, _, err = e.workspace(ctx, c, task)
		if err != nil {
			return false, err
		}
		if prep.SignoffSpec == task.SpecGeneration && prep.SignoffEpoch == project.Epoch {
			return false, errors.Join(domain.Fail(domain.Prerequisite, "current specification awaits authorized readiness sign-off"), e.blockHealth(ctx, "approval-required"))
		}
		if err := e.requestSignoff(ctx, task, project); err != nil {
			return false, err
		}
		prep.SignoffSpec, prep.SignoffEpoch = task.SpecGeneration, project.Epoch
		return true, e.State.Put(ctx, "preparation:"+task.ID, prep)
	}
	if prep.LastReview == reviewKey(task, prep.Stage) {
		return false, domain.Fail(domain.Prerequisite, "preparation is waiting for meaningful new discussion")
	}
	launch.Phase = prep.Stage
	_, err = e.text(ctx, c, launch, manual)
	return true, err
}

func hasAnswers(task domain.Task, entries []domain.Entry) bool {
	if task.Waiting == nil {
		return true
	}
	for _, question := range task.Waiting.QuestionIDs {
		answered := false
		for _, entry := range entries {
			if entry.Kind == "comment" && entry.ParentID == question && slices.Contains(task.Waiting.RequestedIDs, entry.AuthorID) {
				answered = true
				break
			}
		}
		if !answered {
			return false
		}
	}
	return true
}

func (e *Engine) applyText(ctx context.Context, j *local.Journal, launch Launch, reply provider.Reply) (err error) {
	if err := validateTextPhase(launch, reply); err != nil {
		if domain.AsFault(err).Code == domain.Provider {
			return e.rejectTextResponse(ctx, j, launch, err)
		}
		return err
	}
	if err := e.phaseHealth(ctx, "applying", &launch, j.ID, 30, true); err != nil {
		return err
	}
	defer func() {
		if err != nil && slices.Contains([]domain.Code{domain.Conflict, domain.Denied, domain.NotFound, domain.Invalid}, domain.AsFault(err).Code) {
			j.State, j.Problem = "stale", "prepared text decision could not apply to the current context; evidence retained"
			err = errors.Join(err, e.State.SaveJournal(ctx, *j))
		}
	}()
	if reply.Decision != "" {
		return domain.Fail(domain.Provider, "text-only responses cannot make verification decisions")
	}
	switch launch.Phase {
	case "onboarding":
		if reply.Outcome != "reviewed" || len(reply.Questions) != 0 {
			return domain.Fail(domain.Provider, "onboarding requires a complete bounded capability review")
		}
		inventory, err := e.State.Inventory(ctx)
		if err != nil {
			return err
		}
		if inventory.Hash() != launch.InventoryHash {
			j.State = "stale"
			return e.State.SaveJournal(ctx, *j)
		}
		if err := e.State.Put(ctx, "initial-review", launch.InventoryHash); err != nil {
			return err
		}
	case "scope", "acceptance", "preflight":
		if launch.Task == nil {
			return domain.Fail(domain.Internal, "task review omitted its bound context")
		}
		if !slices.Contains([]string{"clear", "blocked", "paused"}, reply.Outcome) {
			return domain.Fail(domain.Provider, "task review returned an execution outcome")
		}
		current, err := api.Call[domain.Task](ctx, e.Client, http.MethodGet, api.Path("tasks", launch.Task.ID), nil)
		if err != nil {
			return err
		}
		if j.State == "reply-prepared" {
			if current.SpecGeneration != launch.Task.SpecGeneration || current.DiscussionGeneration != launch.Task.DiscussionGeneration {
				return domain.Fail(domain.Conflict, "task changed during semantic review")
			}
			j.State = "applying"
			if err := e.State.SaveJournal(ctx, *j); err != nil {
				return err
			}
		}
		task := *launch.Task
		edited := false
		if (reply.Description != nil && *reply.Description != task.Description) || (reply.AcceptanceCriteria != nil && *reply.AcceptanceCriteria != task.AcceptanceCriteria) {
			task, err = e.changePrepared(ctx, task, j.ID+".edit", "edit", "incorporated reviewed clarification into the current task specification", func(r *domain.TaskChange) {
				r.Description = reply.Description
				r.AcceptanceCriteria = reply.AcceptanceCriteria
			})
			if err != nil {
				return err
			}
			edited = true
		}
		if len(reply.Questions) != 0 {
			waiting, err := e.publishQuestions(ctx, j, task, reply.Questions)
			if err != nil {
				return err
			}
			task, err = api.Call[domain.Task](ctx, e.Client, http.MethodGet, api.Path("tasks", task.ID), nil)
			if err != nil {
				return err
			}
			expectedSpec := launch.Task.SpecGeneration
			if edited {
				expectedSpec++
			}
			if task.SpecGeneration != expectedSpec {
				return domain.Fail(domain.Conflict, "task specification changed before recording questions")
			}
			task, err = e.changePrepared(ctx, task, j.ID+".waiting", "waiting", reply.Summary, func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration; r.Waiting = waiting })
			if err != nil {
				return err
			}
		} else {
			if reply.Outcome != "clear" {
				return domain.Fail(domain.Provider, "blocked review must publish actionable questions")
			}
			task, err = api.Call[domain.Task](ctx, e.Client, http.MethodGet, api.Path("tasks", task.ID), nil)
			if err != nil {
				return err
			}
			expectedSpec := launch.Task.SpecGeneration
			if edited {
				expectedSpec++
			}
			if task.SpecGeneration != expectedSpec || task.DiscussionGeneration != launch.Task.DiscussionGeneration {
				return domain.Fail(domain.Conflict, "reviewed specification/discussion is no longer current")
			}
			if launch.Phase == "preflight" || task.Waiting != nil {
				task, err = e.changePrepared(ctx, task, j.ID+".clarity", "clarity", reply.Summary, func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration; r.AnswerIDs = reply.AnswerIDs })
				if err != nil {
					return err
				}
				if err := e.acknowledgeReviewFeedback(ctx, j, launch, task); err != nil {
					return err
				}
			}
		}
		var prep preparation
		_, err = e.State.Get(ctx, "preparation:"+task.ID, &prep)
		if err != nil {
			return err
		}
		prep.ContentHash = contentHash(task)
		prep.LastReview = reviewKey(task, launch.Phase)
		if len(reply.Questions) != 0 {
			if prep.Stage == "" {
				prep.Stage = "scope"
			}
		} else if launch.Phase == "scope" {
			prep.Stage = "acceptance"
		} else if launch.Phase == "acceptance" {
			prep.Stage = "signoff"
		} else if prep.Stage == "" {
			prep.Stage = "scope"
		}
		if err := e.State.Put(ctx, "preparation:"+task.ID, prep); err != nil {
			return err
		}
		if _, err := commit[domain.Entry](ctx, e, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{Command: domain.Command{OperationID: j.ID + ".worklog"}, Kind: "worklog", Body: "Preparation/review: " + reply.Summary}); err != nil {
			return err
		}
	case "message":
		if launch.Message == nil || launch.Message.Kind != "direct" || reply.Outcome != "responded" || len(reply.Questions) != 0 {
			return domain.Fail(domain.Provider, "message reply must be a tool-disabled text response")
		}
		source, err := api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", launch.Message.ID), nil)
		if err != nil {
			return err
		}
		if source.State != "pending" {
			var previous proposal
			found, err := e.State.Get(ctx, "proposal:"+j.ID+".receipt", &previous)
			if err != nil {
				return err
			}
			if found && len(previous.Result) != 0 {
				return e.done(ctx, j)
			}
			return domain.Fail(domain.Conflict, "message disposition changed during response")
		}
		if reply.MessageRead {
			if _, err := commit[domain.Message](ctx, e, "POST", api.Path("messages", source.ID, "read"), domain.MessageReadRequest{Command: domain.Command{OperationID: j.ID + ".read"}}); err != nil {
				return errors.Join(domain.Fail(domain.Prerequisite, "explicit message read acknowledgement remains pending; reconcile the exact receipt without another model call"), err)
			}
		}
		if _, err := commit[domain.Message](ctx, e, "POST", "/v1/messages", domain.MessageRequest{Command: domain.Command{OperationID: j.ID + ".reply"}, RecipientID: source.SenderID, Body: neutralMentions(reply.Summary), Origin: "ai", Kind: "direct"}); err != nil {
			return err
		}
		if _, err := commit[domain.Message](ctx, e, "POST", api.Path("messages", source.ID, "receipt"), domain.MessageReceipt{Command: domain.Command{OperationID: j.ID + ".receipt", ExpectedRevision: source.Revision}, State: "handled"}); err != nil {
			return err
		}
	case "attention":
		if launch.Attention == nil || launch.Task == nil || reply.Outcome != "responded" || len(reply.Questions) != 0 {
			return domain.Fail(domain.Provider, "attention reply must be scoped text")
		}
		entries, err := e.launch(ctx, "attention", launch.Task)
		if err != nil {
			return err
		}
		var source *domain.Entry
		for _, entry := range entries.Discussion {
			if entry.ID == launch.Attention.EntryID {
				copy := entry
				source = &copy
			}
		}
		if source == nil || source.Generation != launch.Attention.Generation || !slices.Contains(source.Mentions, e.Client.Credential.AgentID) {
			return domain.Fail(domain.Conflict, "mention changed during response")
		}
		if _, err := commit[domain.Entry](ctx, e, "POST", api.Path("tasks", source.TaskID, "entries"), domain.EntryRequest{Command: domain.Command{OperationID: j.ID + ".reply"}, Kind: "comment", ParentID: source.ID, Body: neutralMentions(reply.Summary)}); err != nil {
			return err
		}
		if _, err := commit[domain.Attention](ctx, e, "POST", api.Path("attention", source.ID), domain.AttentionRequest{Command: domain.Command{OperationID: j.ID + ".receipt"}, Generation: source.Generation, State: "handled"}); err != nil {
			return err
		}
	case "research":
		if launch.Task == nil || reply.Outcome != "researched" {
			return domain.Fail(domain.Provider, "idle research cannot execute tasks")
		}
		if len(reply.Questions) != 0 {
			if _, err := e.publishQuestions(ctx, j, *launch.Task, reply.Questions); err != nil {
				return err
			}
		}
		if err := e.State.Put(ctx, "last-research", e.Now().UTC().Unix()); err != nil {
			return err
		}
	default:
		return domain.Fail(domain.Internal, "unknown text launch phase")
	}
	return e.done(ctx, j)
}

func (e *Engine) publishQuestions(ctx context.Context, j *local.Journal, task domain.Task, questions []provider.Question) (*domain.Waiting, error) {
	project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		return nil, err
	}
	agents, err := api.Call[[]domain.Agent](ctx, e.Client, "GET", "/v1/agents", nil)
	if err != nil {
		return nil, err
	}
	waiting := &domain.Waiting{Kind: "clarification", Reason: "clarify the published blocking questions", RequestedIDs: []string{}, QuestionIDs: []string{}}
	for i, question := range questions {
		respondents := slices.Clone(question.RespondentIDs)
		if len(respondents) == 0 {
			for _, agent := range agents {
				if agent.Active && agent.Founder {
					respondents = append(respondents, agent.ID)
					break
				}
			}
			if len(respondents) == 0 {
				respondents = append(respondents, project.Delegates...)
			}
		}
		body := question.Body
		for _, id := range respondents {
			if id == e.Client.Credential.AgentID {
				return nil, domain.Fail(domain.Provider, "worker must ask an identified responsible actor rather than itself")
			}
			valid := false
			for _, agent := range agents {
				if agent.ID == id && agent.Active {
					valid = true
					break
				}
			}
			if !valid {
				return nil, domain.Fail(domain.Provider, "question named an unknown or revoked respondent")
			}
			if !strings.Contains(body, "@{"+id+"}") {
				body += " @{" + id + "}"
			}
			if !slices.Contains(waiting.RequestedIDs, id) {
				waiting.RequestedIDs = append(waiting.RequestedIDs, id)
			}
		}
		if len(respondents) == 0 {
			return nil, domain.Fail(domain.Prerequisite, "no active respondent can route this clarification")
		}
		entry, err := commit[domain.Entry](ctx, e, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{Command: domain.Command{OperationID: fmt.Sprintf("%s.question%d", j.ID, i)}, Kind: "comment", Body: body})
		if err != nil {
			return nil, err
		}
		waiting.QuestionIDs = append(waiting.QuestionIDs, entry.ID)
	}
	return waiting, nil
}

func (e *Engine) requestSignoff(ctx context.Context, task domain.Task, project domain.Project) error {
	agents, err := api.Call[[]domain.Agent](ctx, e.Client, "GET", "/v1/agents", nil)
	if err != nil {
		return err
	}
	recipients := []string{}
	for _, agent := range agents {
		if !agent.Active || (!agent.Founder && !slices.Contains(project.Delegates, agent.ID)) {
			continue
		}
		if len(task.Policy.RequiredActors) > 0 && !slices.Contains(task.Policy.RequiredActors, agent.ID) {
			continue
		}
		if task.Policy.IndependentReadiness && agent.ID == task.AssigneeID {
			continue
		}
		recipients = append(recipients, agent.ID)
	}
	if len(recipients) == 0 {
		return domain.Fail(domain.Prerequisite, "no current project authority satisfies the named/independent readiness policy")
	}
	for _, recipient := range recipients {
		key := fmt.Sprintf("%s:%d:%d:%s", task.ID, task.SpecGeneration, project.Epoch, recipient)
		body := fmt.Sprintf("Ready for a bounded first attempt: task %s, project %s, specification %d, authority epoch %d. Review task show and approve the exact current specification with task change kind=approve. No timed expiry; specification, assignment, workspace, policy, or authority changes invalidate this request. Delivery is not approval.", task.ID, task.ProjectID, task.SpecGeneration, project.Epoch)
		if _, err := commit[domain.Message](ctx, e, "POST", "/v1/messages", domain.MessageRequest{Command: domain.Command{OperationID: "signoff_" + domain.Hash(key)}, RecipientID: recipient, Body: body, Origin: "service", Kind: "direct"}); err != nil {
			return err
		}
	}
	return nil
}
