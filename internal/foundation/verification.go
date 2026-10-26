package foundation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) reportVerification(ctx context.Context, tx *sql.Tx, a domain.Agent, t domain.Task, run domain.Run, req domain.ReportRequest) (domain.ReportOutcome, error) {
	var outcome domain.ReportOutcome
	if req.ResultGeneration != run.ResultGeneration {
		return outcome, domain.Fail(domain.Conflict, "verification report must identify its exact claimed result")
	}
	if req.Waiting != nil || req.SafeToRetry || (req.ApproveRework && (req.Outcome != "completed" || req.Decision != "rework")) {
		return outcome, domain.Fail(domain.Invalid, "verification cannot resolve assignee blockers, declare safe retries or approve unrelated work")
	}
	if req.Outcome == "completed" {
		if (req.Decision != "accept" && req.Decision != "rework") || len(req.Evidence) == 0 {
			return outcome, domain.Fail(domain.Invalid, "completed verification requires an accept/rework decision and evidence")
		}
	} else if req.Decision != "" {
		return outcome, domain.Fail(domain.Invalid, "only completed verification may decide accept or rework")
	}
	p, err := get[domain.Project](ctx, tx, "projects", t.ProjectID)
	if err != nil {
		return outcome, err
	}
	stale := t.SpecGeneration != run.SpecGeneration || t.ResultGeneration != run.ResultGeneration ||
		t.DiscussionGeneration != run.DiscussionGeneration || p.Epoch != run.ProjectEpoch ||
		t.ActiveRun.StopRequested || !eligibleApprover(a, p, t, true) || a.ID == t.AssigneeID ||
		(!a.Founder && a.Mode != "ai") || a.Revision != run.AgentRevision || t.Result == nil || t.Result.SpecGeneration != run.SpecGeneration || t.ArchivedAt != 0
	if !stale && req.ApproveRework {
		if !eligibleApprover(a, p, t, false) {
			return outcome, domain.Fail(domain.Denied, "rework approval requires current readiness authority and policy")
		}
		if _, err := activeIdentitySubject(ctx, tx, t.AssigneeID); err != nil {
			var fault *domain.Fault
			if !errors.As(err, &fault) || (fault.Code != domain.Denied && fault.Code != domain.NotFound) {
				return outcome, err
			}
			stale = true
		}
	}
	afterStop := t.ActiveRun.AfterStop
	t.ActiveRun = nil
	run.State, run.Decision, run.Summary, run.Evidence, run.Checkpoint, run.FinishedAt = req.Outcome, req.Decision, req.Summary, req.Evidence, req.Checkpoint, s.timestamp()
	if stale {
		run.State = "stale"
	}
	if err := s.saveRun(ctx, tx, run); err != nil {
		return outcome, err
	}
	if stale || req.Outcome != "completed" {
		if t.Result != nil && t.Result.SpecGeneration == t.SpecGeneration && t.ResultGeneration == run.ResultGeneration {
			t.Status = domain.Completed
			t.Waiting = &domain.Waiting{Kind: "verification", Reason: "verification stopped or became uncertain; project authority must inspect external effects before accepting or requesting rework", Since: s.timestamp()}
		}
		if afterStop == domain.CancelledStatus {
			t.Status, t.Waiting = domain.CancelledStatus, nil
		}
	} else {
		// Entry publication advances discussion; reload before the final decision so
		// neither the feedback nor its generation is overwritten by the old task.
		if err := s.saveTask(ctx, tx, &t, a.ID, "verification-stopped"); err != nil {
			return outcome, err
		}
		body := fmt.Sprintf("Verification of result %d (%s), %s: %s", run.ResultGeneration, run.ID, req.Decision, req.Summary)
		if req.Decision == "rework" {
			body += " @{" + t.AssigneeID + "}"
		}
		entry := domain.Entry{ID: domain.NewID("comment"), TaskID: t.ID, Kind: "comment", Body: body,
			Review:   &domain.ReviewReference{RunID: run.ID, SpecGeneration: run.SpecGeneration, ResultGeneration: run.ResultGeneration, Decision: req.Decision},
			AuthorID: a.ID, Generation: 1, Revision: 1,
			CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		if err := s.saveEntry(ctx, tx, &entry, a.ID, "verification"); err != nil {
			return outcome, err
		}
		t, err = get[domain.Task](ctx, tx, "tasks", t.ID)
		if err != nil {
			return outcome, err
		}
		if req.Decision == "accept" {
			t.Status, t.Waiting = domain.Done, nil
			t.Verification = &domain.Approval{ActorID: a.ID, SpecGeneration: t.SpecGeneration, ResultGeneration: t.ResultGeneration, ProjectEpoch: p.Epoch, At: s.timestamp(), OperationID: req.OperationID}
		} else {
			invalidate(&t)
			t.Status = domain.Todo
			if req.ApproveRework {
				t.Readiness = &domain.Approval{ActorID: a.ID, SpecGeneration: t.SpecGeneration, ProjectEpoch: p.Epoch, At: s.timestamp(), OperationID: req.OperationID}
			}
		}
	}
	if err := s.saveTask(ctx, tx, &t, a.ID, "verification-report", req.Summary); err != nil {
		return outcome, err
	}
	return domain.ReportOutcome{Accepted: !stale, Stale: stale, Task: t, Run: run}, nil
}
