package work

import (
	"context"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (e *Engine) reconcileMessageAction(ctx context.Context, message domain.Message) (bool, error) {
	request := message.Request
	if request == nil || request.Action.Kind != "work" || request.Action.TaskID == "" ||
		(request.State != "accepted" && request.State != "executing") {
		return false, nil
	}
	task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", request.Action.TaskID), nil)
	if err != nil {
		return false, err
	}
	state, reason := "", ""
	switch {
	case task.ArchivedAt != 0 || task.AssigneeID != e.Client.Credential.AgentID || task.SpecGeneration != request.SpecGeneration:
		state, reason = "failed", "The linked task assignment, specification or archive state changed; request execution is not being retried."
	case task.Status != domain.CancelledStatus && task.ResultGeneration > request.ResultGeneration && task.Result != nil &&
		task.Result.SpecGeneration == request.SpecGeneration && task.Result.RunID == task.LastRunID:
		state, reason = "completed", "The linked task produced a new execution result. Independent task verification/acceptance remains separate."
	case task.Status == domain.CancelledStatus || (task.ActiveRun == nil && (task.Status == domain.Paused || task.Waiting != nil)):
		state, reason = "failed", "The linked task stopped or is blocked without a new completed result. Retained checkpoints, retry approvals and independent task authority still apply; no execution is being retried."
	case request.State == "accepted" && task.ActiveRun != nil && task.ActiveRun.Kind != domain.VerificationRun && !task.ActiveRun.StopRequested:
		state, reason = "executing", "The existing approved task was admitted through its normal fenced execution path."
	default:
		return false, nil
	}
	_, err = commit[domain.Message](ctx, e, "POST", api.Path("messages", message.ID, "request"),
		domain.MessageActionReport{Command: domain.Command{OperationID: "request." + message.ID + "." + strconv.FormatInt(message.Revision, 10) + "." + state, ExpectedRevision: message.Revision}, State: state, Reason: reason})
	return true, err
}
