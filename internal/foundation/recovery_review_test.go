package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func settledRecoveryReview(t *testing.T, f *fixture) (domain.Task, domain.Run, domain.Run) {
	t.Helper()
	task, source := stoppedRecoveryTask(t, f)
	token := domain.NewToken()
	run, err := f.s.Claim(context.Background(), f.worker.ID, task.ID, recoveryClaim(task, source, token))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.s.Report(context.Background(), f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "paused", Summary: "Old review cited task metadata without inspecting retained work.",
		Evidence: []string{"Prior response retained"}, Checkpoint: "invocation_prior_review",
	})
	if err != nil {
		t.Fatal(err)
	}
	return outcome.Task, source, outcome.Run
}

func recoveryReviewRequest(task domain.Task) domain.TaskChange {
	return domain.TaskChange{
		Command: domain.Command{OperationID: domain.NewID("review"), ExpectedRevision: task.Revision},
		Kind:    "recovery-review", Reason: "Correct the inadequate retained review through native inspection, not execution replay.",
		SpecGeneration: task.SpecGeneration, RunID: task.RecoveryRunID, Attempt: task.RecoveryAttempts + 1,
	}
}

func TestRecoveryReviewPreservesHistoryCountsAndExactReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source, previous := settledRecoveryReview(t, f)
	request := recoveryReviewRequest(task)
	_, err := f.s.ChangeTask(ctx, f.worker.ID, task.ID, request)
	assertCode(t, err, domain.Denied)
	authorized, err := f.s.ChangeTask(ctx, f.founder.ID, task.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if authorized.RecoveryRunID != "" || authorized.RecoveryAttempts != 1 || authorized.Attempts != 1 ||
		authorized.Failures != 0 || !authorized.UnsafeRetry || authorized.LastRunID != source.ID ||
		authorized.RetryApproval != nil || authorized.SpecGeneration != task.SpecGeneration || authorized.Readiness == nil {
		t.Fatal("corrective authorization reset accounting or approved execution", authorized)
	}
	retained, err := get[domain.Run](ctx, f.s.db, "runs", previous.ID)
	if err != nil || retained.State != previous.State || retained.Summary != previous.Summary || retained.Checkpoint != previous.Checkpoint {
		t.Fatal("corrective authorization rewrote the prior recovery", retained, err)
	}
	token := domain.NewToken()
	review, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(authorized, source, token))
	if err != nil || review.Attempt != 2 || review.SourceRunID != source.ID {
		t.Fatal("corrective review did not retain the exact source and spent count", review, err)
	}
	if _, err := f.s.ChangeTask(ctx, f.founder.ID, task.ID, request); err != nil {
		t.Fatal("lost authorization acknowledgement did not replay", err)
	}
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.RecoveryRunID != review.ID || current.ActiveRun == nil || current.ActiveRun.ID != review.ID {
		t.Fatal("replaying authorization erased the newer recovery", current, err)
	}
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: review.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "reconciled", SafeToRetry: true, Summary: "Actual retained input and local output were inspected.",
		Evidence: []string{"Exact native inspection and provider result retained"}, Checkpoint: "invocation_corrective_review",
	})
	if err != nil || !outcome.Accepted || outcome.Task.UnsafeRetry || outcome.Task.Attempts != 1 ||
		outcome.Task.Failures != 0 || outcome.Task.RecoveryAttempts != 2 || outcome.Task.Result != nil {
		t.Fatal("corrective reconciliation completed execution or reset counts", outcome, err)
	}
}

func TestRecoveryReviewCannotRenewASecondBlockedReview(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source, _ := settledRecoveryReview(t, f)
	task, err := f.s.ChangeTask(ctx, f.founder.ID, task.ID, recoveryReviewRequest(task))
	if err != nil {
		t.Fatal(err)
	}
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, token))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "paused", Summary: "The original external effect still has no authoritative receipt.",
		Evidence: []string{"Native inspection performed; original effect remains unresolved"}, Checkpoint: "invocation_corrective_review",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ChangeTask(ctx, f.founder.ID, task.ID, recoveryReviewRequest(outcome.Task))
	assertCode(t, err, domain.Prerequisite)
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.RecoveryAttempts != 2 || !current.UnsafeRetry || current.RecoveryRunID != run.ID {
		t.Fatal("a new request renewed corrective inference or cleared uncertainty", current, err)
	}
}

func TestRecoveryReviewRejectsStaleAndUnrelatedRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*domain.TaskChange)
		code   domain.Code
	}{
		{"stale-revision", func(r *domain.TaskChange) { r.ExpectedRevision-- }, domain.Conflict},
		{"stale-specification", func(r *domain.TaskChange) { r.SpecGeneration-- }, domain.Conflict},
		{"unrelated-recovery", func(r *domain.TaskChange) { r.RunID = "run_other" }, domain.Conflict},
		{"reset-count", func(r *domain.TaskChange) { r.Attempt = 1 }, domain.Conflict},
		{"execution-fields", func(r *domain.TaskChange) { r.Status = domain.Todo }, domain.Invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			task, _, _ := settledRecoveryReview(t, f)
			request := recoveryReviewRequest(task)
			test.change(&request)
			_, err := f.s.ChangeTask(context.Background(), f.founder.ID, task.ID, request)
			assertCode(t, err, test.code)
			current, err := get[domain.Task](context.Background(), f.s.db, "tasks", task.ID)
			if err != nil || current.Revision != task.Revision || current.RecoveryRunID != task.RecoveryRunID ||
				current.RecoveryAttempts != task.RecoveryAttempts || !current.UnsafeRetry {
				t.Fatal("denied corrective review mutated the task", current, err)
			}
		})
	}
}
