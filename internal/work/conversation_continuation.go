package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func conversationOperation(action conversationAction) string {
	if action.ResumeFrom != "" {
		return action.ID + ".resume." + domain.Hash(action.ResumeFrom)[:32]
	}
	return action.ID
}

func conversationInstructions(action conversationAction) string {
	if action.ScopeInstructions != "" {
		return action.ScopeInstructions + "\nContinue the same scope only. Current follow-up: " + action.Plan.Instructions
	}
	return action.Plan.Instructions
}

func (e *Engine) validateActionSource(ctx context.Context, action conversationAction) error {
	if action.OriginalSource != nil {
		original := conversationAction{ID: conversationActionID(*action.OriginalSource), Source: *action.OriginalSource,
			SourceHandled: action.OriginalSource.Message != nil}
		if err := e.validateActionSource(ctx, original); err != nil {
			return err
		}
	}
	if !action.SourceHandled || action.Source.Message == nil {
		return e.validateConversationSource(ctx, action.Source)
	}
	var receipt proposal
	found, err := e.State.Get(ctx, "proposal:"+action.ID+".receipt", &receipt)
	if err != nil {
		return err
	}
	if !found || len(receipt.Result) == 0 || receipt.Method != "POST" ||
		receipt.Path != api.Path("messages", action.Source.Message.ID, "receipt") {
		return domain.Fail(domain.Prerequisite, "the original source's exact acknowledged receipt is required for continuation")
	}
	var acknowledged domain.Message
	if err := json.Unmarshal(receipt.Result, &acknowledged); err != nil {
		return err
	}
	current, err := api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", acknowledged.ID), nil)
	if err != nil {
		return err
	}
	source := action.Source.Message
	if current.ID != source.ID || current.Kind != "direct" || current.State != "handled" ||
		current.RecipientID != e.Client.Credential.AgentID || current.SenderID != source.SenderID ||
		current.Revision != acknowledged.Revision || current.Body != source.Body || !current.ResponseRequest {
		return domain.Fail(domain.Conflict, "the original conversation source changed; its old action cannot resume")
	}
	requester, err := api.Call[domain.Agent](ctx, e.Client, "GET", api.Path("agents", source.SenderID), nil)
	if err != nil {
		return err
	}
	if !requester.Active || requester.Banned {
		return domain.Fail(domain.Denied, "the original action requester is no longer active")
	}
	if current.Announcement != nil {
		authority, err := api.Call[map[string]bool](ctx, e.Client, "GET", api.Path("messages", current.ID, "announcement-authority"), nil)
		if err != nil {
			return err
		}
		if !authority["authorized"] {
			return domain.Fail(domain.Denied, "the original announcement authority is no longer current")
		}
	}
	return nil
}

func (e *Engine) resolveConversationContinuation(ctx context.Context, action *conversationAction) error {
	if action.Plan.TaskID != "" || action.Source.Message == nil {
		return nil
	}
	for _, task := range action.Source.ReferencedTasks {
		if task.ProjectID != domain.AdhocProjectID || task.CreatedBy != e.Client.Credential.AgentID ||
			task.AssigneeID != e.Client.Credential.AgentID || task.Status != domain.Paused || task.LastRunID == "" {
			continue
		}
		var parentID string
		found, err := e.State.Get(ctx, "conversation-task:"+task.ID, &parentID)
		if err != nil {
			return err
		}
		if !found {
			return domain.Fail(domain.Prerequisite, "the referenced interrupted task has no retained conversation source; do not replace it")
		}
		var parent conversationAction
		found, err = e.State.Get(ctx, "conversation-action:"+parentID, &parent)
		if err != nil {
			return err
		}
		if !found {
			return domain.Fail(domain.Prerequisite, "the referenced interrupted action is unavailable; do not create a replacement")
		}
		if parent.Source.Message == nil || parent.Source.Message.ThreadID != action.Source.Message.ThreadID {
			continue
		}
		if action.Plan.TaskID != "" && action.Plan.TaskID != task.ID {
			return domain.Fail(domain.Prerequisite, "multiple interrupted actions share this thread; the plan must identify the intended existing task")
		}
		action.Plan.TaskID = task.ID
	}
	return nil
}

func (e *Engine) bindConversationContinuation(ctx context.Context, action *conversationAction) error {
	var task *domain.Task
	for index := range action.Source.ReferencedTasks {
		if action.Source.ReferencedTasks[index].ID == action.Plan.TaskID {
			task = &action.Source.ReferencedTasks[index]
			break
		}
	}
	if task == nil || task.ProjectID != domain.AdhocProjectID || task.CreatedBy != e.Client.Credential.AgentID ||
		task.AssigneeID != e.Client.Credential.AgentID || task.Status != domain.Paused || task.ActiveRun != nil {
		return domain.Fail(domain.Prerequisite, "a continuation must name the current stopped owned Adhoc task from its actual thread")
	}
	var parentID string
	found, err := e.State.Get(ctx, "conversation-task:"+task.ID, &parentID)
	if err != nil {
		return err
	}
	if !found {
		return domain.Fail(domain.Prerequisite, "the original source-bound action is unavailable; do not replace its task")
	}
	var parent conversationAction
	found, err = e.State.Get(ctx, "conversation-action:"+parentID, &parent)
	if err != nil {
		return err
	}
	if !found || parent.State != "done" || parent.TaskID != task.ID || parent.Report == nil ||
		parent.Report.SpecGeneration != task.SpecGeneration || parent.Report.RunID != task.LastRunID {
		return domain.Fail(domain.Conflict, "the original action scope or latest stopped run changed")
	}
	if action.Plan.WorkspaceRef != "" && action.Plan.WorkspaceRef != task.WorkspaceRef {
		return domain.Fail(domain.Denied, "continuation cannot choose a different workspace")
	}
	action.TaskID, action.ScopeGeneration = task.ID, task.SpecGeneration
	action.ScopeInstructions = conversationInstructions(parent)
	original := parent.Source
	if parent.OriginalSource != nil {
		original = *parent.OriginalSource
	}
	action.OriginalSource = &original
	return nil
}

func (e *Engine) continuationLaunch(ctx context.Context, task domain.Task, action conversationAction) (Launch, error) {
	launch, err := e.launch(ctx, domain.ExecutionRun, &task)
	if err != nil {
		return launch, err
	}
	launch.Message, launch.Attention = action.Source.Message, action.Source.Attention
	launch.Thread, launch.Discussion = action.Source.Thread, action.Source.Discussion
	launch.ConversationID, launch.ActionInstructions = action.ID, conversationInstructions(action)
	if err := e.refreshActionContext(ctx, &launch, action.Source); err != nil {
		return launch, err
	}
	return launch, nil
}

func (e *Engine) refreshActionContext(ctx context.Context, launch *Launch, source Launch) error {
	source.ReferencedTasks, source.ReferencedProjects = nil, nil
	if source.Attention != nil {
		task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", source.Attention.TaskID), nil)
		if err != nil {
			return err
		}
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		source.Task, source.Project = &task, &project
	}
	if err := e.conversationContext(ctx, &source); err != nil {
		return err
	}
	launch.Thread, launch.ThreadCursor, launch.ContextIncomplete = source.Thread, source.ThreadCursor, source.ContextIncomplete
	launch.Requester, launch.ReferencedTasks, launch.ReferencedProjects = source.Requester, source.ReferencedTasks, source.ReferencedProjects
	if source.Task != nil && (launch.Task == nil || source.Task.ID != launch.Task.ID) {
		launch.ReferencedTasks = append(launch.ReferencedTasks, *source.Task)
	}
	if source.Project != nil && (launch.Project == nil || source.Project.ID != launch.Project.ID) {
		launch.ReferencedProjects = append(launch.ReferencedProjects, *source.Project)
	}
	return nil
}

func (e *Engine) prepareConversationContinuation(ctx context.Context, c config.Worker, action *conversationAction, manual bool) (domain.Task, error) {
	task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", action.TaskID), nil)
	if err != nil {
		return task, err
	}
	if task.ProjectID != domain.AdhocProjectID || task.CreatedBy != e.Client.Credential.AgentID ||
		task.AssigneeID != e.Client.Credential.AgentID || task.Status != domain.Paused || task.ActiveRun != nil ||
		task.ArchivedAt != 0 || task.SpecGeneration != action.ScopeGeneration || task.Readiness == nil ||
		task.Readiness.SpecGeneration != task.SpecGeneration {
		return task, domain.Fail(domain.Prerequisite, "continuation requires unchanged stopped scope, assignment and readiness")
	}
	if _, err := e.State.Workspace(ctx, task.WorkspaceRef); err != nil {
		return task, err
	}
	if task.UnsafeRetry && task.RetryApproval == nil {
		launch, err := e.continuationLaunch(ctx, task, *action)
		if err != nil {
			return task, err
		}
		if err := e.recoverTask(ctx, c, task, launch, manual); err != nil {
			if domain.AsFault(err).Code == domain.Prerequisite || domain.AsFault(err).Code == domain.NotFound ||
				domain.AsFault(err).Code == domain.Denied || domain.AsFault(err).Code == domain.Conflict {
				return task, e.retainContinuationFailure(ctx, action, task, err)
			}
			return task, err
		}
		task, err = api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", task.ID), nil)
		if err != nil {
			return task, err
		}
		if task.UnsafeRetry {
			return task, e.retainBlockedContinuation(ctx, action, task)
		}
	}
	if task.Failures >= task.RetryPolicy.MaxRetries+1 {
		return task, domain.Fail(domain.Prerequisite, "approved failure retry budget is exhausted; a new message cannot reset it")
	}
	if err := e.State.Put(ctx, "conversation-task:"+task.ID, action.ID); err != nil {
		return task, err
	}
	task, err = e.changePrepared(ctx, task, conversationOperation(*action)+".clarity", "clarity",
		"reviewed the retained original scope, current source and native reconciliation before continuing",
		func(request *domain.TaskChange) { request.SpecGeneration = task.SpecGeneration })
	return task, err
}

func (e *Engine) recoveryReply(ctx context.Context, task domain.Task) (domain.Run, provider.Reply, bool, error) {
	runs, err := api.Call[[]domain.Run](ctx, e.Client, "GET", api.Path("tasks", task.ID, "runs"), nil)
	if err != nil {
		return domain.Run{}, provider.Reply{}, false, err
	}
	for _, run := range runs {
		if run.ID != task.RecoveryRunID || run.SourceRunID != task.LastRunID || run.State == "running" {
			continue
		}
		journal, err := e.State.Journal(ctx, run.Checkpoint)
		if err != nil {
			return run, provider.Reply{}, false, err
		}
		if journal.State != "done" || journal.Kind != domain.RecoveryRun || journal.RunID != run.ID {
			return run, provider.Reply{}, false, domain.Fail(domain.Prerequisite, "the exact recovery result is not durably acknowledged")
		}
		var launch Launch
		if err := json.Unmarshal(journal.Context, &launch); err != nil {
			return run, provider.Reply{}, false, err
		}
		reply := provider.Reply{Outcome: "paused", Summary: run.Summary, Evidence: run.Evidence}
		if len(journal.Reply) != 0 && journal.Problem == "" {
			if err := json.Unmarshal(journal.Reply, &reply); err != nil {
				return run, reply, false, err
			}
		} else if journal.Problem != "" {
			reply.Summary = journal.Problem
		}
		reply.Outcome = "paused"
		return run, reply, launch.Recovery != nil && launch.Recovery.CorrectiveReviewOf != "", nil
	}
	return domain.Run{}, provider.Reply{}, false, domain.Fail(domain.Prerequisite, "no settled recovery result exists for the current interrupted run")
}

func (e *Engine) retainBlockedContinuation(ctx context.Context, action *conversationAction, task domain.Task) error {
	run, reply, _, err := e.recoveryReply(ctx, task)
	if err != nil {
		return err
	}
	action.Result, action.Report, action.State = &reply, &domain.ReportRequest{RunID: run.ID, SpecGeneration: task.SpecGeneration, Outcome: run.State}, "reporting"
	if err := e.State.Put(ctx, "conversation-action:"+action.ID, *action); err != nil {
		return err
	}
	return domain.Fail(domain.Prerequisite, "recovery retained a specific unresolved risk: "+reply.Summary)
}

func (e *Engine) retainContinuationFailure(ctx context.Context, action *conversationAction, task domain.Task, cause error) error {
	runs, err := api.Call[[]domain.Run](ctx, e.Client, "GET", api.Path("tasks", task.ID, "runs"), nil)
	if err != nil {
		return errors.Join(cause, err)
	}
	for _, run := range runs {
		if run.ID == task.RecoveryRunID && run.SourceRunID == task.LastRunID && run.State != "running" {
			return e.retainBlockedContinuation(ctx, action, task)
		}
	}
	for _, run := range runs {
		if run.ID != task.LastRunID || run.State == "running" {
			continue
		}
		reply := provider.Reply{Outcome: "paused",
			Summary:  "Cannot continue the original task yet: " + domain.AsFault(cause).Message,
			Evidence: []string{"Current task: " + task.ID, "Retained interrupted execution: " + run.ID}}
		action.Result, action.Report, action.State = &reply, &domain.ReportRequest{
			RunID: run.ID, SpecGeneration: task.SpecGeneration, Outcome: run.State,
		}, "reporting"
		return errors.Join(cause, e.State.Put(ctx, "conversation-action:"+action.ID, *action))
	}
	return errors.Join(cause, domain.Fail(domain.Prerequisite, "the original stopped run is unavailable; no result was manufactured"))
}

func (e *Engine) continueManagedTask(ctx context.Context, c config.Worker, task domain.Task, actionID string, manual bool) (bool, error) {
	if task.Status != domain.Paused || task.ActiveRun != nil || task.LastRunID == "" ||
		(task.Waiting != nil && task.Waiting.Kind != "retry") {
		return false, nil
	}
	var action conversationAction
	found, err := e.State.Get(ctx, "conversation-action:"+actionID, &action)
	if err != nil {
		return false, err
	}
	if !found || action.Version != 1 || action.State != "done" || action.TaskID != task.ID ||
		action.Report == nil || action.Report.RunID != task.LastRunID || action.Report.SpecGeneration != task.SpecGeneration {
		return false, domain.Fail(domain.Prerequisite, "the exact stopped conversation action and scope are required")
	}
	if action.AwaitingReply || (action.Result != nil && len(action.Result.Questions) != 0) {
		return false, domain.Fail(domain.Prerequisite, "the original action is awaiting the requested reply; recovery cannot invent that answer")
	}
	action.SourceHandled = action.Source.Message != nil
	if err := e.validateActionSource(ctx, action); err != nil {
		return false, err
	}
	if task.UnsafeRetry && task.RetryApproval == nil {
		launch, err := e.continuationLaunch(ctx, task, action)
		if err != nil {
			return false, err
		}
		if err := e.State.Put(ctx, "conversation-action:"+action.ID, action); err != nil {
			return false, err
		}
		err = e.recoverTask(ctx, c, task, launch, manual)
		if err == nil {
			return true, nil
		}
		if task.RecoveryRunID != "" {
			run, reply, corrective, readErr := e.recoveryReply(ctx, task)
			if readErr != nil {
				return false, errors.Join(err, readErr)
			}
			if action.Source.Message != nil {
				reference := task.LastRunID
				if corrective {
					reference = run.ID
				}
				operation := action.ID + ".recovery." + domain.Hash(reference)[:32] + ".reply"
				if sendErr := e.sendConversationReply(ctx, operation, *action.Source.Message, reply); sendErr != nil {
					return false, errors.Join(err, sendErr)
				}
			}
		}
		return false, err
	}
	if task.Failures >= task.RetryPolicy.MaxRetries+1 {
		return false, domain.Fail(domain.Prerequisite, "approved failure retry budget is exhausted; retain the same task and prior attempts")
	}
	action.ResumeFrom, action.ScopeGeneration = task.LastRunID, task.SpecGeneration
	action.State, action.Report, action.Result = "queued", nil, nil
	if err := e.State.Put(ctx, "conversation-action:"+action.ID, action); err != nil {
		return false, fmt.Errorf("persist exact conversation continuation: %w", err)
	}
	return true, nil
}
