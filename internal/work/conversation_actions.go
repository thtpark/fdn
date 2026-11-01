package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

type conversationAction struct {
	Version           int                         `json:"version"`
	ID                string                      `json:"id"`
	State             string                      `json:"state"`
	Source            Launch                      `json:"source"`
	Plan              provider.ConversationAction `json:"plan"`
	Read              bool                        `json:"read"`
	ResponseRequested *bool                       `json:"response_requested,omitempty"`
	TaskID            string                      `json:"task_id,omitempty"`
	Report            *domain.ReportRequest       `json:"report,omitempty"`
	Result            *provider.Reply             `json:"result,omitempty"`
}

func conversationActionID(launch Launch) string {
	source := ""
	if launch.Message != nil {
		source = fmt.Sprintf("message:%s:%d", launch.Message.ID, launch.Message.Revision)
	} else if launch.Attention != nil {
		source = fmt.Sprintf("comment:%s:%d", launch.Attention.EntryID, launch.Attention.Generation)
	}
	return "conversation." + domain.Hash(source)[:32]
}

func (e *Engine) conversationActionPending(ctx context.Context, launch Launch) (bool, error) {
	var action conversationAction
	found, err := e.State.Get(ctx, "conversation-action:"+conversationActionID(launch), &action)
	if err != nil {
		return false, err
	}
	if found && action.Version != 1 {
		return false, domain.Fail(domain.Prerequisite, "unsupported retained conversation action")
	}
	return found, nil
}

func (e *Engine) queueConversationAction(ctx context.Context, journal *local.Journal, launch Launch, reply provider.Reply) error {
	if reply.Action == nil {
		return domain.Fail(domain.Internal, "conversation action plan is missing")
	}
	if err := e.validateConversationSource(ctx, launch); err != nil {
		return err
	}
	id := conversationActionID(launch)
	var retained conversationAction
	found, err := e.State.Get(ctx, "conversation-action:"+id, &retained)
	if err != nil {
		return err
	}
	if found {
		if retained.Version != 1 || retained.ID != id || retained.Plan != *reply.Action {
			return domain.Fail(domain.Conflict, "retained conversation action cannot be replaced by another plan")
		}
	} else {
		retained = conversationAction{Version: 1, ID: id, State: "queued", Source: launch, Plan: *reply.Action, Read: reply.MessageRead, ResponseRequested: reply.ResponseRequested}
		if err := e.State.Put(ctx, "conversation-action:"+id, retained); err != nil {
			return err
		}
	}
	return e.done(ctx, journal)
}

func (e *Engine) validateConversationSource(ctx context.Context, launch Launch) error {
	actor := e.Client.Credential.AgentID
	if source := launch.Message; source != nil {
		current, err := api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", source.ID), nil)
		if err != nil {
			return err
		}
		if current.Kind != "direct" || current.RecipientID != actor || current.State != "pending" ||
			current.Revision != source.Revision || !current.ResponseRequest {
			return domain.Fail(domain.Conflict, "source message disposition or recipient changed")
		}
		if current.Announcement != nil {
			authorized, err := api.Call[map[string]bool](ctx, e.Client, "GET", api.Path("messages", current.ID, "announcement-authority"), nil)
			if err != nil {
				return err
			}
			if !authorized["authorized"] {
				return domain.Fail(domain.Denied, "announcement authority is no longer current")
			}
		}
		return nil
	}
	if item := launch.Attention; item != nil {
		entry, err := api.Call[domain.Entry](ctx, e.Client, "GET", api.Path("entries", item.EntryID), nil)
		if err != nil {
			return err
		}
		task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", entry.TaskID), nil)
		if err != nil {
			return err
		}
		if entry.Generation != item.Generation || !domain.RelevantComment(entry, task, actor) {
			return domain.Fail(domain.Conflict, "source comment generation, assignment or mention changed")
		}
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		if task.ArchivedAt != 0 || project.Archived {
			return domain.Fail(domain.Conflict, "source task or project was archived; its action cannot execute")
		}
		return nil
	}
	return domain.Fail(domain.Internal, "conversation action has no exact message/comment source")
}

func (e *Engine) continueConversationActions(ctx context.Context, c config.Worker, manual bool) (bool, error) {
	values, err := e.State.PendingConversationActions(ctx)
	if err != nil {
		return false, err
	}
	for _, raw := range values {
		var action conversationAction
		if err := json.Unmarshal(raw, &action); err != nil {
			return false, err
		}
		if action.Version != 1 || action.ID != conversationActionID(action.Source) {
			return false, domain.Fail(domain.Prerequisite, "unsupported retained conversation action identity")
		}
		var receipt proposal
		receiptPrepared, err := e.State.Get(ctx, "proposal:"+action.ID+".receipt", &receipt)
		if err != nil {
			return false, err
		}
		if err := e.validateConversationSource(ctx, action.Source); err != nil && !(action.State == "reporting" && receiptPrepared) {
			if slices.Contains([]domain.Code{domain.Conflict, domain.NotFound, domain.Denied}, domain.AsFault(err).Code) {
				if action.TaskID != "" {
					task, readErr := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", action.TaskID), nil)
					if readErr != nil {
						return false, errors.Join(err, readErr)
					}
					if task.ActiveRun == nil && (task.Status == domain.Todo || task.Status == domain.Paused) {
						if _, stopErr := e.changePrepared(ctx, task, action.ID+".source-stale", "status", "source conversation changed; do not execute or retry its old action",
							func(q *domain.TaskChange) { q.Status = domain.CancelledStatus }); stopErr != nil {
							return false, errors.Join(err, stopErr)
						}
					}
				}
				action.State = "stale"
				if saveErr := e.State.Put(ctx, "conversation-action:"+action.ID, action); saveErr != nil {
					return false, errors.Join(err, saveErr)
				}
				e.Log.Warn("conversation action source changed; retained work is not being retried", "action", action.ID, "code", domain.AsFault(err).Code)
				continue
			}
			return false, err
		}
		if action.State == "reporting" {
			return true, e.deliverConversationAction(ctx, &action)
		}
		if action.State != "queued" && action.State != "working" {
			return false, domain.Fail(domain.Prerequisite, "unknown retained conversation action state")
		}
		task, err := e.prepareConversationTask(ctx, c, &action)
		if err != nil {
			return true, err
		}
		launch, err := e.launch(ctx, domain.ExecutionRun, &task)
		if err != nil {
			return true, err
		}
		launch.Message, launch.Attention = action.Source.Message, action.Source.Attention
		launch.Thread, launch.ThreadCursor = action.Source.Thread, action.Source.ThreadCursor
		launch.ContextIncomplete, launch.Requester = action.Source.ContextIncomplete, action.Source.Requester
		launch.ReferencedTasks = slices.Clone(action.Source.ReferencedTasks)
		launch.ReferencedProjects = slices.Clone(action.Source.ReferencedProjects)
		if action.Source.Task != nil {
			launch.ReferencedTasks = append(launch.ReferencedTasks, *action.Source.Task)
		}
		if action.Source.Project != nil {
			launch.ReferencedProjects = append(launch.ReferencedProjects, *action.Source.Project)
		}
		launch.ConversationID, launch.ActionInstructions = action.ID, action.Plan.Instructions
		launch.ActionKey = requestedAction(ctx, action.Source)
		launch.Discussion = action.Source.Discussion
		action.State = "working"
		if err := e.State.Put(ctx, "conversation-action:"+action.ID, action); err != nil {
			return true, err
		}
		runErr := e.execute(ctx, c, task, launch, manual)
		if _, err := e.State.Get(ctx, "conversation-action:"+action.ID, &action); err != nil {
			return true, errors.Join(runErr, err)
		}
		if action.State == "reporting" {
			runErr = errors.Join(runErr, e.deliverConversationAction(ctx, &action))
		}
		return true, runErr
	}
	return false, nil
}

func (e *Engine) prepareConversationTask(ctx context.Context, c config.Worker, action *conversationAction) (domain.Task, error) {
	task, err := commit[domain.Task](ctx, e, "POST", "/v1/tasks", domain.TaskRequest{
		Command: domain.Command{OperationID: action.ID + ".task"}, ProjectID: domain.AdhocProjectID,
		Title:              "Adhoc conversation action",
		Description:        "Perform the authorized standalone action in the retained message/comment context. Private request text, workspace paths and raw evidence remain in the private controller journal.",
		AcceptanceCriteria: "Retrieve relevant context and matching skills, perform only authorized work, verify actual effects, and report results or blockers to the source conversation. This record grants no other project's authority or spending permission.",
	})
	if err != nil {
		return task, err
	}
	if action.TaskID != "" && action.TaskID != task.ID {
		return task, domain.Fail(domain.Conflict, "conversation action task identity changed")
	}
	action.TaskID = task.ID
	if err := e.State.Put(ctx, "conversation-action:"+action.ID, action); err != nil {
		return task, err
	}
	if err := e.State.Put(ctx, "conversation-task:"+task.ID, action.ID); err != nil {
		return task, err
	}
	current, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", task.ID), nil)
	if err != nil {
		return current, err
	}
	if current.ProjectID != domain.AdhocProjectID || current.CreatedBy != e.Client.Credential.AgentID ||
		current.AssigneeID != e.Client.Credential.AgentID || current.ActiveRun != nil || current.LastRunID != "" ||
		current.Status != domain.Todo || current.ArchivedAt != 0 {
		return current, domain.Fail(domain.Prerequisite, "Adhoc action already ran, stopped or changed; no automatic execution retry is authorized")
	}
	task = current
	ref := action.Plan.WorkspaceRef
	if ref == "" && action.Source.Task != nil && action.Source.Task.AssigneeID == e.Client.Credential.AgentID {
		ref = action.Source.Task.WorkspaceRef
	}
	var binding local.Binding
	if ref != "" {
		binding, err = e.State.Workspace(ctx, ref)
	} else {
		binding, err = e.projectWorkspace(ctx, c, domain.AdhocProjectID)
	}
	if err != nil {
		return task, e.permissionBlocked(ctx, c, action.Source, "workspace", err)
	}
	if task.WorkspaceRef == "" {
		task, err = e.changePrepared(ctx, task, action.ID+".workspace", "workspace", "use the existing granted local workspace for this Adhoc action",
			func(q *domain.TaskChange) { q.WorkspaceRef = binding.Ref })
		if err != nil {
			return task, err
		}
	}
	if task.WorkspaceRef != binding.Ref || task.SpecGeneration != 2 {
		return task, domain.Fail(domain.Conflict, "Adhoc task specification/workspace changed after its exact action was prepared")
	}
	if task.Readiness == nil {
		task, err = e.changePrepared(ctx, task, action.ID+".readiness", "approve", "standing Adhoc policy permits only this creator's own authorized action",
			func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
		if err != nil {
			return task, err
		}
	}
	if task.Clarity == nil {
		task, err = e.changePrepared(ctx, task, action.ID+".clarity", "clarity", "reviewed the exact retained source, action plan and current permission boundaries",
			func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	}
	return task, err
}

func (e *Engine) retainConversationReport(ctx context.Context, journal *local.Journal) error {
	if len(journal.Context) == 0 {
		return nil
	}
	var launch Launch
	if err := json.Unmarshal(journal.Context, &launch); err != nil {
		return err
	}
	if launch.ConversationID == "" {
		return nil
	}
	var action conversationAction
	found, err := e.State.Get(ctx, "conversation-action:"+launch.ConversationID, &action)
	if err != nil {
		return err
	}
	if !found || action.Version != 1 || action.TaskID != journal.TaskID || journal.Report == nil {
		return domain.Fail(domain.Prerequisite, "conversation action's exact task/report record is unavailable")
	}
	result := provider.Reply{Outcome: journal.Report.Outcome, Summary: journal.Report.Summary, Evidence: journal.Report.Evidence}
	if len(journal.Reply) != 0 {
		if err := json.Unmarshal(journal.Reply, &result); err != nil {
			return err
		}
		if result.ResponseRequested == nil {
			result.ResponseRequested = action.ResponseRequested
		}
	}
	result.Outcome = journal.Report.Outcome
	if result.Outcome != "completed" {
		result.Summary = "Action " + result.Outcome + ": " + result.Summary
	}
	action.Result, action.Report, action.State = &result, journal.Report, "reporting"
	if err := e.State.Put(ctx, "conversation-action:"+action.ID, action); err != nil {
		return err
	}
	journal.Report.Summary = "Adhoc conversation action: " + journal.Report.Outcome + ". Detailed results and checks remain in the source conversation and private journal."
	journal.Report.Evidence = []string{"Exact controller journal and source-generation references retained; independent verification remains separate."}
	return nil
}

func (e *Engine) deliverConversationAction(ctx context.Context, action *conversationAction) error {
	if action.Report == nil || action.Result == nil {
		return domain.Fail(domain.Prerequisite, "conversation action has no retained actual result/report")
	}
	runs, err := api.Call[[]domain.Run](ctx, e.Client, "GET", api.Path("tasks", action.TaskID, "runs"), nil)
	if err != nil {
		return err
	}
	var run *domain.Run
	for index := range runs {
		if runs[index].ID == action.Report.RunID {
			run = &runs[index]
			break
		}
	}
	if run == nil || run.State == "running" {
		return domain.Fail(domain.Prerequisite, "actual Adhoc execution report is still awaiting acknowledgement")
	}
	if run.State != action.Report.Outcome {
		return domain.Fail(domain.Conflict, "Adhoc execution outcome changed; retained result was not replaced")
	}
	result := *action.Result
	result.Evidence = append(slices.Clone(result.Evidence), "Adhoc task: "+action.TaskID, "Execution run: "+run.ID)
	source := action.Source
	if source.Message != nil {
		if action.Read {
			if _, err := commit[domain.Message](ctx, e, "POST", api.Path("messages", source.Message.ID, "read"),
				domain.MessageReadRequest{Command: domain.Command{OperationID: action.ID + ".read"}}); err != nil {
				return err
			}
		}
		if err := e.sendConversationReply(ctx, action.ID+".reply", *source.Message, result); err != nil {
			return err
		}
		if _, err := commit[domain.Message](ctx, e, "POST", api.Path("messages", source.Message.ID, "receipt"),
			domain.MessageReceipt{Command: domain.Command{OperationID: action.ID + ".receipt", ExpectedRevision: source.Message.Revision}, State: "handled"}); err != nil {
			return err
		}
	} else if source.Attention != nil {
		body, err := conversationBody(result)
		if err != nil {
			return err
		}
		if _, err := commit[domain.Entry](ctx, e, "POST", api.Path("tasks", source.Attention.TaskID, "entries"),
			domain.EntryRequest{Command: domain.Command{OperationID: action.ID + ".reply"}, Kind: "comment", ParentID: source.Attention.EntryID, Body: body}); err != nil {
			return err
		}
		if _, err := commit[domain.Attention](ctx, e, "POST", api.Path("attention", source.Attention.EntryID),
			domain.AttentionRequest{Command: domain.Command{OperationID: action.ID + ".receipt"}, Generation: source.Attention.Generation, State: "handled"}); err != nil {
			return err
		}
	} else {
		return domain.Fail(domain.Internal, "conversation result has no source recipient")
	}
	action.State = "done"
	return e.State.Put(ctx, "conversation-action:"+action.ID, action)
}

func (e *Engine) guardConversationAction(ctx context.Context, cancel context.CancelCauseFunc, launch Launch) error {
	if launch.ConversationID == "" {
		return nil
	}
	var action conversationAction
	found, err := e.State.Get(ctx, "conversation-action:"+launch.ConversationID, &action)
	if err != nil {
		return err
	}
	if !found {
		return domain.Fail(domain.Prerequisite, "conversation action source is unavailable")
	}
	subscription := &authoritySubscription{}
	if source := action.Source.Message; source != nil {
		subscription.messageID = source.ID
	}
	if source := action.Source.Task; source != nil {
		subscription.taskID, subscription.projectID = source.ID, source.ProjectID
	}
	return e.guardAuthority(ctx, cancel, subscription, func(ctx context.Context) error {
		if err := e.validateConversationSource(ctx, action.Source); err != nil {
			return fmt.Errorf("conversation action admission changed: %w", err)
		}
		return nil
	})
}
