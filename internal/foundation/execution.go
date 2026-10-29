package foundation

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) retryReady(ctx context.Context, tx *sql.Tx, p domain.Project, t domain.Task) error {
	if t.Failures >= t.RetryPolicy.MaxRetries+1 {
		return domain.Fail(domain.Prerequisite, "approved retry budget is exhausted; revise and reapprove the specification")
	}
	if t.NextAttemptAt > s.timestamp() {
		return domain.Fail(domain.Prerequisite, "retry backoff has not elapsed")
	}
	if t.UnsafeRetry {
		valid, err := s.validApproval(ctx, tx, p, t, t.RetryApproval, false)
		if err != nil {
			return err
		}
		if !valid || t.RetryApproval.RunID != t.LastRunID || t.RetryApproval.Attempt != t.Attempts+1 {
			return domain.Fail(domain.Prerequisite, "unknown side effects require exact unsafe-retry approval")
		}
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, actor, id string, req domain.ClaimRequest) (domain.Run, error) {
	return mutate(ctx, s, actor, "task.claim:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Run, error) {
		if !a.Founder && a.Mode != "ai" {
			return domain.Run{}, domain.Fail(domain.Prerequisite, "Founder paused this agent; a new claim requires Founder-authorized resumption")
		}
		t, err := get[domain.Task](ctx, tx, "tasks", id)
		if err != nil {
			return domain.Run{}, err
		}
		if err := expect(t.Revision, req.ExpectedRevision); err != nil {
			return domain.Run{}, err
		}
		p, err := get[domain.Project](ctx, tx, "projects", t.ProjectID)
		if err != nil {
			return domain.Run{}, err
		}
		kind := req.Kind
		if kind == "" {
			kind = domain.ExecutionRun
		}
		if kind != domain.ExecutionRun && kind != domain.VerificationRun {
			return domain.Run{}, domain.Fail(domain.Invalid, "claim kind must be execution or verification")
		}
		if !domain.ValidHash(req.TokenHash) || !domain.ValidProvider(req.Provider) {
			return domain.Run{}, domain.Fail(domain.Invalid, "claim requires an ownership-token hash and supported provider")
		}
		if err := domain.CheckText(req.Model, "claim model", 256, false); err != nil {
			return domain.Run{}, err
		}
		if kind == domain.VerificationRun {
			if !eligibleApprover(a, p, t, true) || a.ID == t.AssigneeID {
				return domain.Run{}, domain.Fail(domain.Denied, "verification runs require independent current project authority satisfying task policy")
			}
			if t.SpecGeneration != req.SpecGeneration || t.ResultGeneration != req.ResultGeneration || p.Epoch != req.ProjectEpoch {
				return domain.Run{}, domain.Fail(domain.Conflict, "verification specification, result or project authority changed")
			}
			if t.ArchivedAt != 0 || t.Repetitive || t.ActiveRun != nil || t.Waiting != nil || t.Status != domain.Completed || t.Result == nil || t.Result.SpecGeneration != t.SpecGeneration {
				return domain.Run{}, domain.Fail(domain.Prerequisite, "verification requires an unblocked current completed result and no active run")
			}
		} else {
			if req.ResultGeneration != 0 || req.ProjectEpoch != 0 {
				return domain.Run{}, domain.Fail(domain.Invalid, "result and project epoch fields are only valid for verification claims")
			}
			if t.AssigneeID != a.ID {
				return domain.Run{}, domain.Fail(domain.Denied, "only the assigned agent can claim execution")
			}
			if t.SpecGeneration != req.SpecGeneration {
				return domain.Run{}, domain.Fail(domain.Conflict, "specification changed")
			}
			if t.ArchivedAt != 0 || p.Archived || t.Repetitive || t.ActiveRun != nil || (t.Status != domain.Todo && t.Status != domain.Paused) {
				return domain.Run{}, domain.Fail(domain.Prerequisite, "task is not an executable stopped candidate")
			}
			valid, err := s.validApproval(ctx, tx, p, t, t.Readiness, false)
			if err != nil {
				return domain.Run{}, err
			}
			if t.WorkspaceRef == "" || t.Waiting != nil || !valid {
				return domain.Run{}, domain.Fail(domain.Prerequisite, "current readiness approval, workspace, and resolved blockers are required")
			}
			if t.Clarity == nil || t.Clarity.ActorID != a.ID || t.Clarity.SpecGeneration != t.SpecGeneration || t.Clarity.DiscussionGeneration != t.DiscussionGeneration || t.Clarity.ProjectEpoch != p.Epoch {
				return domain.Run{}, domain.Fail(domain.Prerequisite, "assigned worker must review the latest specification and discussion")
			}
			if (t.Provider != "" && t.Provider != req.Provider) || (t.Model != "" && t.Model != req.Model) {
				return domain.Run{}, domain.Fail(domain.Prerequisite, "provider/model does not match the approved task")
			}
			if err := s.retryReady(ctx, tx, p, t); err != nil {
				return domain.Run{}, err
			}
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE json_extract(data, '$.active_run.agent_id') = ?", a.ID).Scan(&active); err != nil {
			return domain.Run{}, err
		}
		if active != 0 {
			return domain.Run{}, domain.Fail(domain.Conflict, "this agent already owns an active task")
		}
		run := domain.Run{ID: domain.NewID("run"), Kind: kind, TaskID: t.ID, AgentID: a.ID, SpecGeneration: t.SpecGeneration,
			Attempt: t.Attempts + 1, Provider: req.Provider, Model: req.Model, State: "running", Evidence: []string{}, StartedAt: s.timestamp()}
		if kind == domain.VerificationRun {
			run.Attempt, run.ResultGeneration, run.DiscussionGeneration, run.ProjectEpoch, run.AgentRevision = 1, t.ResultGeneration, t.DiscussionGeneration, p.Epoch, a.Revision
		}
		data, err := encode(run)
		if err != nil {
			return run, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO runs(id, data, task_id, agent_id, token_hash) VALUES(?, ?, ?, ?, ?)", run.ID, data, t.ID, a.ID, req.TokenHash); err != nil {
			return run, err
		}
		t.ActiveRun = &domain.ActiveRun{ID: run.ID, Kind: kind, AgentID: a.ID, SpecGeneration: t.SpecGeneration, Attempt: run.Attempt}
		if kind == domain.ExecutionRun {
			t.LastRunID, t.Attempts, t.Status = run.ID, run.Attempt, domain.Running
			t.RetryApproval = nil
		}
		return run, s.saveTask(ctx, tx, &t, a.ID, "claim")
	})
}

func (s *Store) saveRun(ctx context.Context, tx *sql.Tx, run domain.Run) error {
	data, err := encode(run)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE runs SET data = ? WHERE id = ?", data, run.ID)
	return err
}

func (s *Store) Report(ctx context.Context, actor, taskID string, req domain.ReportRequest) (domain.ReportOutcome, error) {
	return mutate(ctx, s, actor, "task.report:"+taskID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ReportOutcome, error) {
		var outcome domain.ReportOutcome
		if !slices.Contains([]string{"completed", "paused", "failed", "stopped", "interrupted"}, req.Outcome) {
			return outcome, domain.Fail(domain.Invalid, "unsupported report outcome")
		}
		if err := domain.CheckText(req.Summary, "report summary", 32768, true); err != nil {
			return outcome, err
		}
		if len(req.Evidence) > 64 {
			return outcome, domain.Fail(domain.Invalid, "too many evidence references")
		}
		for _, evidence := range req.Evidence {
			if err := domain.CheckText(evidence, "evidence reference", 8192, true); err != nil {
				return outcome, err
			}
		}
		if err := domain.CheckText(req.Checkpoint, "opaque checkpoint", 256, false); err != nil {
			return outcome, err
		}
		t, err := get[domain.Task](ctx, tx, "tasks", taskID)
		if err != nil {
			return outcome, err
		}
		run, err := get[domain.Run](ctx, tx, "runs", req.RunID)
		if err != nil {
			return outcome, err
		}
		var tokenHash string
		if err := tx.QueryRowContext(ctx, "SELECT token_hash FROM runs WHERE id = ?", run.ID).Scan(&tokenHash); err != nil {
			return outcome, err
		}
		if run.AgentID != a.ID || run.TaskID != t.ID || len(req.Token) != 43 ||
			subtle.ConstantTimeCompare([]byte(tokenHash), []byte(domain.Hash(req.Token))) != 1 {
			return outcome, domain.Fail(domain.Denied, "report ownership does not match")
		}
		if t.ActiveRun == nil || t.ActiveRun.ID != run.ID || run.State != "running" || req.SpecGeneration != run.SpecGeneration {
			return outcome, domain.Fail(domain.Conflict, "run is no longer the active ownership generation")
		}
		if run.Kind == domain.VerificationRun {
			return s.reportVerification(ctx, tx, a, t, run, req)
		}
		if req.ResultGeneration != 0 || req.Decision != "" || req.ApproveRework {
			return outcome, domain.Fail(domain.Invalid, "verification fields are not valid for an execution report")
		}
		if req.Waiting != nil {
			if err := s.validateWaiting(ctx, tx, t, a.ID, req.Waiting); err != nil {
				return outcome, err
			}
			req.Outcome = "paused"
		}
		project, err := get[domain.Project](ctx, tx, "projects", t.ProjectID)
		if err != nil {
			return outcome, err
		}
		approved, err := s.validApproval(ctx, tx, project, t, t.Readiness, false)
		if err != nil {
			return outcome, err
		}
		stale := t.SpecGeneration != run.SpecGeneration || t.AssigneeID != a.ID || t.ActiveRun.StopRequested || !approved || project.Archived
		run.State, run.Summary, run.Evidence, run.Checkpoint, run.FinishedAt = req.Outcome, req.Summary, req.Evidence, req.Checkpoint, s.timestamp()
		afterStop := t.ActiveRun.AfterStop
		t.ActiveRun, t.Clarity = nil, nil
		if stale {
			run.State = "stale"
			t.Status, t.UnsafeRetry = domain.Paused, true
			t.Waiting = &domain.Waiting{Kind: "retry", Reason: "stopped/stale run evidence retained; review side effects before another attempt", Since: s.timestamp()}
			if afterStop == domain.CancelledStatus {
				t.Status, t.Waiting = domain.CancelledStatus, nil
			}
		} else {
			switch req.Outcome {
			case "completed":
				t.Status, t.Waiting, t.UnsafeRetry = domain.Completed, nil, false
				t.ResultGeneration++
				t.Result = &domain.Result{RunID: run.ID, SpecGeneration: run.SpecGeneration, Summary: req.Summary, Evidence: req.Evidence}
			case "paused", "stopped":
				t.Status = domain.Paused
				t.Waiting = req.Waiting
				if t.Waiting == nil {
					t.Waiting = &domain.Waiting{Kind: "retry", Reason: req.Summary, Since: s.timestamp()}
					t.UnsafeRetry = !req.SafeToRetry
				} else {
					t.Waiting.Since = s.timestamp()
				}
			case "failed", "interrupted":
				t.Failures++
				t.Status, t.UnsafeRetry = domain.Paused, !req.SafeToRetry || req.Outcome == "interrupted"
				t.Waiting = &domain.Waiting{Kind: "retry", Reason: req.Summary, Since: s.timestamp()}
				delay := t.RetryPolicy.BackoffSeconds << max(0, t.Failures-1)
				t.NextAttemptAt = s.timestamp() + min(delay, 86400)
			}
		}
		if err := s.saveRun(ctx, tx, run); err != nil {
			return outcome, err
		}
		if err := s.saveTask(ctx, tx, &t, a.ID, "report"); err != nil {
			return outcome, err
		}
		return domain.ReportOutcome{Accepted: !stale, Stale: stale, Task: t, Run: run}, nil
	})
}

func (s *Store) Ownership(ctx context.Context, actor string) ([]domain.Task, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM tasks WHERE json_extract(data, '$.active_run.agent_id') = ? ORDER BY id", actor)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Task](rows)
}

func (s *Store) Runs(ctx context.Context, actor, taskID string) ([]domain.Run, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}

	if _, err := get[domain.Task](ctx, s.db, "tasks", taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM runs WHERE task_id = ? ORDER BY id", taskID)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Run](rows)
}
