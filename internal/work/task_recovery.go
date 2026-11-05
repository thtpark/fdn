package work

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

type TaskRecoveryContext struct {
	SourceRun             domain.Run      `json:"source_run"`
	JournalID             string          `json:"journal_id"`
	InspectionAuthorized  bool            `json:"inspection_authorized"`
	JoinedProcessVerified bool            `json:"joined_process_verified"`
	FailuresRemaining     int             `json:"failures_remaining"`
	InspectionCLI         string          `json:"inspection_cli"`
	CorrectiveReviewOf    string          `json:"corrective_review_of,omitempty"`
	PreviousReply         *provider.Reply `json:"previous_reply,omitempty"`
}

func (e *Engine) taskRecoveryContext(ctx context.Context, task domain.Task) (TaskRecoveryContext, error) {
	var recovery TaskRecoveryContext
	if task.Status != domain.Paused || !task.UnsafeRetry || task.Waiting == nil || task.Waiting.Kind != "retry" ||
		task.ActiveRun != nil || task.LastRunID == "" || task.Readiness == nil {
		return recovery, domain.Fail(domain.Prerequisite, "task is not awaiting recovery under current readiness")
	}
	if task.Failures >= task.RetryPolicy.MaxRetries+1 {
		return recovery, domain.Fail(domain.Prerequisite, "approved failure retry budget is exhausted; recovery cannot reset it")
	}
	if task.NextAttemptAt > e.Now().Unix() {
		deferWorkUntil(ctx, time.Unix(task.NextAttemptAt, 0))
		return recovery, domain.Fail(domain.Prerequisite, "retry backoff has not elapsed")
	}
	if task.RecoveryAttempts >= domain.MaxTaskRecoveries {
		return recovery, domain.Fail(domain.Prerequisite, "automatic recovery budget is exhausted; retain the exact task and evidence")
	}
	runs, err := api.Call[[]domain.Run](ctx, e.Client, "GET", api.Path("tasks", task.ID, "runs"), nil)
	if err != nil {
		return recovery, err
	}
	for _, run := range runs {
		if run.ID == task.RecoveryRunID && run.SourceRunID == task.LastRunID {
			return recovery, domain.Fail(domain.Prerequisite, "the original work already has a recovery result: "+run.Summary)
		}
		if run.ID == task.LastRunID {
			recovery.SourceRun = run
		}
		if run.Kind == domain.RecoveryRun && run.SourceRunID == task.LastRunID &&
			slices.Contains([]string{"paused", "failed"}, run.State) {
			recovery.CorrectiveReviewOf = run.ID
		}
	}
	source := recovery.SourceRun
	if source.ID == "" || source.TaskID != task.ID || source.AgentID != e.Client.Credential.AgentID ||
		source.SpecGeneration != task.SpecGeneration || !slices.Contains([]string{"stopped", "failed", "paused"}, source.State) {
		return recovery, domain.Fail(domain.Prerequisite, "the exact current interrupted execution cannot be reconciled")
	}
	journal, err := e.State.Journal(ctx, source.Checkpoint)
	if err != nil {
		return recovery, fmt.Errorf("the interrupted execution's native journal is required: %w", err)
	}
	// A canonical done report follows joined provider cleanup. Operator recovery,
	// uncertain journals and missing old evidence are not adopted as this proof.
	if journal.Kind != domain.ExecutionRun || journal.State != "done" || journal.Problem != "" || journal.PID == 0 ||
		journal.TaskID != task.ID || journal.RunID != source.ID || journal.SpecGeneration != task.SpecGeneration ||
		journal.WorkspaceRef != task.WorkspaceRef || journal.Report == nil || journal.Report.RunID != source.ID ||
		journal.Report.OperationID != journal.ID+".report" || journal.Report.Checkpoint != journal.ID ||
		journal.Report.Outcome != source.State {
		return recovery, domain.Fail(domain.Prerequisite, "native joined-process evidence is missing or uncertain; preserve the exact invocation fence")
	}
	var original Launch
	if err := json.Unmarshal(journal.Context, &original); err != nil {
		return recovery, err
	}
	if original.AgentID != e.Client.Credential.AgentID || original.Task == nil || original.Task.ID != task.ID ||
		original.Task.SpecGeneration != task.SpecGeneration || original.Project == nil ||
		original.Project.Epoch != task.Readiness.ProjectEpoch {
		return recovery, domain.Fail(domain.Conflict, "the interrupted invocation's original scope or project authority changed")
	}
	if _, err := e.State.Workspace(ctx, journal.WorkspaceRef); err != nil {
		return recovery, err
	}
	recovery.JournalID = journal.ID
	recovery.InspectionAuthorized, recovery.JoinedProcessVerified = true, true
	recovery.FailuresRemaining = task.RetryPolicy.MaxRetries + 1 - task.Failures
	executable, err := os.Executable()
	if err != nil {
		return recovery, err
	}
	recovery.InspectionCLI = filepath.Join(filepath.Dir(executable), "agent-cli")
	if len(journal.Reply) != 0 {
		var reply provider.Reply
		if err := json.Unmarshal(journal.Reply, &reply); err != nil {
			return recovery, err
		}
		recovery.PreviousReply = &reply
	}
	return recovery, nil
}

func (e *Engine) recoverTask(ctx context.Context, c config.Worker, task domain.Task, launch Launch, manual bool) error {
	e.authorityMu.Lock()
	offline := e.feedRunning && !e.feedOnline
	e.authorityMu.Unlock()
	if offline {
		return domain.Fail(domain.Unavailable, "the authority channel is not online; retained work cannot begin reconciliation")
	}
	if err := e.validateRunMode(ctx, manual); err != nil {
		return err
	}
	recovery, err := e.taskRecoveryContext(ctx, task)
	if err != nil {
		return err
	}
	launch.Phase, launch.Recovery = domain.RecoveryRun, &recovery
	return e.coding(ctx, c, task, launch, manual)
}
