package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) recoveryReady(ctx context.Context, tx *sql.Tx, actor domain.Agent, project domain.Project, task domain.Task, request domain.ClaimRequest) error {
	if task.AssigneeID != actor.ID {
		return domain.Fail(domain.Denied, "only the assigned agent can reconcile its interrupted work")
	}
	if request.SpecGeneration != task.SpecGeneration || request.ProjectEpoch != project.Epoch || request.ResultGeneration != 0 {
		return domain.Fail(domain.Conflict, "recovery specification or project authority changed")
	}
	if task.ArchivedAt != 0 || project.Archived || task.Repetitive || task.ActiveRun != nil ||
		task.Status != domain.Paused || !task.UnsafeRetry || task.Waiting == nil || task.Waiting.Kind != "retry" {
		return domain.Fail(domain.Prerequisite, "recovery requires stopped work with an unresolved retry fence")
	}
	approved, err := s.validApproval(ctx, tx, project, task, task.Readiness, false)
	if err != nil {
		return err
	}
	if !approved || task.WorkspaceRef == "" {
		return domain.Fail(domain.Prerequisite, "recovery requires unchanged current readiness and workspace authority")
	}
	if err := s.retryBudget(task); err != nil {
		return err
	}
	if task.RecoveryAttempts >= domain.MaxTaskRecoveries {
		return domain.Fail(domain.Prerequisite, "automatic recovery budget is exhausted; preserve the evidence for current project authority")
	}
	if request.SourceRunID == "" || request.SourceRunID != task.LastRunID {
		return domain.Fail(domain.Conflict, "recovery must identify the exact latest interrupted execution")
	}
	source, err := get[domain.Run](ctx, tx, "runs", request.SourceRunID)
	if err != nil {
		return err
	}
	if source.TaskID != task.ID || source.AgentID != actor.ID || source.SpecGeneration != task.SpecGeneration ||
		(source.Kind != "" && source.Kind != domain.ExecutionRun) || !slices.Contains([]string{"stopped", "failed", "paused"}, source.State) ||
		source.Checkpoint == "" || source.FinishedAt == 0 || (source.ProjectEpoch != 0 && source.ProjectEpoch != project.Epoch) {
		return domain.Fail(domain.Prerequisite, "the exact interrupted execution and its retained native checkpoint are required")
	}
	if request.Provider != source.Provider || (request.Model != "" && request.Model != source.Model) ||
		(task.Provider != "" && request.Provider != task.Provider) || (task.Model != "" && request.Model != "" && request.Model != task.Model) {
		return domain.Fail(domain.Prerequisite, "recovery must retain the original provider and model preference or delegate unavailable model selection to that provider")
	}
	if task.RecoveryRunID != "" {
		previous, err := get[domain.Run](ctx, tx, "runs", task.RecoveryRunID)
		if err != nil {
			return err
		}
		if previous.SourceRunID == source.ID {
			return domain.Fail(domain.Prerequisite, "this interrupted execution already has a recovery result; inspect it instead of repeating inference")
		}
	}
	return nil
}

func (s *Store) authorizeRecoveryReview(ctx context.Context, tx *sql.Tx, actor domain.Agent, project domain.Project, task *domain.Task, request domain.TaskChange) error {
	if !eligibleApprover(actor, project, *task, false) {
		return domain.Fail(domain.Denied, "actor does not satisfy current recovery-review authority")
	}
	if request.SpecGeneration != task.SpecGeneration || request.RunID == "" || request.RunID != task.RecoveryRunID ||
		request.Attempt != task.RecoveryAttempts+1 {
		return domain.Fail(domain.Conflict, "corrective review must identify the exact settled recovery, specification and next recovery attempt")
	}
	if task.Status != domain.Paused || !task.UnsafeRetry || task.ActiveRun != nil || task.Waiting == nil ||
		task.Waiting.Kind != "retry" || task.RetryApproval != nil || task.RecoveryAttempts >= domain.MaxTaskRecoveries {
		return domain.Fail(domain.Prerequisite, "corrective review requires stopped uncertain work and available recovery budget")
	}
	approved, err := s.validApproval(ctx, tx, project, *task, task.Readiness, false)
	if err != nil {
		return err
	}
	if !approved || task.WorkspaceRef == "" {
		return domain.Fail(domain.Prerequisite, "corrective review requires unchanged current readiness and workspace authority")
	}
	if err := s.retryBudget(*task); err != nil {
		return err
	}
	previous, err := get[domain.Run](ctx, tx, "runs", request.RunID)
	if err != nil {
		return err
	}
	if previous.Kind != domain.RecoveryRun || previous.TaskID != task.ID || previous.SourceRunID != task.LastRunID ||
		previous.AgentID != task.AssigneeID || previous.SpecGeneration != task.SpecGeneration ||
		previous.ProjectEpoch != project.Epoch || previous.DiscussionGeneration != task.DiscussionGeneration ||
		!slices.Contains([]string{"paused", "failed"}, previous.State) || previous.FinishedAt == 0 || previous.Checkpoint == "" {
		return domain.Fail(domain.Prerequisite, "only the exact settled non-stale recovery can receive a corrective review")
	}
	var invoked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE task_id=?
AND json_extract(data,'$.kind')=? AND json_extract(data,'$.source_run_id')=?
AND json_extract(data,'$.state')<>?`, task.ID, domain.RecoveryRun, task.LastRunID, domain.RecoveryNotStarted).Scan(&invoked); err != nil {
		return err
	}
	if invoked != 1 {
		return domain.Fail(domain.Prerequisite, "only one corrective recovery review is allowed per interrupted execution")
	}
	if err := s.history(ctx, tx, task.ID, actor.ID, "recovery-review-authorized", *task, request.Reason); err != nil {
		return err
	}
	// Retain the prior run and spent count; this authorizes inspection, never execution.
	task.RecoveryRunID, task.Clarity = "", nil
	task.Waiting = &domain.Waiting{Kind: "retry", Reason: "A corrective inspection was authorized; the previous recovery result and original execution uncertainty remain retained.", Since: s.timestamp()}
	return nil
}

func (s *Store) reportRecovery(ctx context.Context, tx *sql.Tx, actor domain.Agent, task domain.Task, run domain.Run, request domain.ReportRequest) (domain.ReportOutcome, error) {
	if request.ResultGeneration != 0 || request.Decision != "" || request.ApproveRework || request.Waiting != nil ||
		request.Outcome == "completed" {
		return domain.ReportOutcome{}, domain.Fail(domain.Invalid, "reconciliation cannot complete, verify or change the execution specification")
	}
	if request.Outcome == "reconciled" {
		if !request.SafeToRetry || len(request.Evidence) == 0 || request.Checkpoint == "" {
			return domain.ReportOutcome{}, domain.Fail(domain.Invalid, "safe reconciliation requires checked evidence and its exact native checkpoint")
		}
	} else if request.SafeToRetry {
		return domain.ReportOutcome{}, domain.Fail(domain.Invalid, "a stopped or failed recovery cannot authorize execution")
	}
	if request.Outcome == domain.RecoveryNotStarted && request.Checkpoint == "" {
		return domain.ReportOutcome{}, domain.Fail(domain.Invalid, "an unused recovery claim requires its exact native no-invocation checkpoint")
	}
	project, err := get[domain.Project](ctx, tx, "projects", task.ProjectID)
	if err != nil {
		return domain.ReportOutcome{}, err
	}
	approved, err := s.validApproval(ctx, tx, project, task, task.Readiness, false)
	if err != nil {
		return domain.ReportOutcome{}, err
	}
	stale := !approved || project.Archived || task.ArchivedAt != 0 || task.ActiveRun.StopRequested ||
		task.AssigneeID != actor.ID || task.SpecGeneration != run.SpecGeneration || task.DiscussionGeneration != run.DiscussionGeneration ||
		project.Epoch != run.ProjectEpoch || task.LastRunID != run.SourceRunID || task.RecoveryRunID != run.ID ||
		(!actor.Founder && actor.Mode != "ai") || actor.Revision != run.AgentRevision
	run.State, run.Summary, run.Evidence, run.Checkpoint, run.FinishedAt = request.Outcome, request.Summary, request.Evidence, request.Checkpoint, s.timestamp()
	afterStop := task.ActiveRun.AfterStop
	task.ActiveRun, task.Clarity = nil, nil
	if stale {
		run.State = "stale"
	}
	if !stale && request.Outcome == domain.RecoveryNotStarted {
		if task.RecoveryAttempts != run.Attempt || task.RecoveryAttempts < 1 {
			return domain.ReportOutcome{}, domain.Fail(domain.Conflict, "unused recovery reservation no longer matches")
		}
		task.RecoveryRunID, task.RecoveryAttempts = "", task.RecoveryAttempts-1
		task.UnsafeRetry = true
		task.Waiting = &domain.Waiting{Kind: "retry", Reason: "Reconciliation did not invoke a provider; original execution evidence remains fenced.", Since: s.timestamp()}
	} else if !stale && request.Outcome == "reconciled" {
		task.UnsafeRetry, task.Waiting, task.RetryApproval = false, nil, nil
	} else {
		task.UnsafeRetry = true
		task.Waiting = &domain.Waiting{Kind: "retry", Reason: "Recovery did not establish safe continuation: " + request.Summary, Since: s.timestamp()}
	}
	if afterStop == domain.CancelledStatus {
		task.Status, task.Waiting = domain.CancelledStatus, nil
	}
	if err := s.saveRun(ctx, tx, run); err != nil {
		return domain.ReportOutcome{}, err
	}
	if err := s.saveTask(ctx, tx, &task, actor.ID, "recovery-report"); err != nil {
		return domain.ReportOutcome{}, err
	}
	if err := s.adhocRunWorklog(ctx, tx, task, run); err != nil {
		return domain.ReportOutcome{}, err
	}
	return domain.ReportOutcome{Accepted: !stale, Stale: stale, Task: task, Run: run}, nil
}
