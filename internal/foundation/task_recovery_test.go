package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func stoppedRecoveryTask(t *testing.T, f *fixture) (domain.Task, domain.Run) {
	t.Helper()
	ctx := context.Background()
	task := f.ready(t)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(token),
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "stopped", Summary: "Authority feed interrupted; owned process cleanup completed.",
		Checkpoint: "invocation_owned",
	})
	if err != nil {
		t.Fatal(err)
	}
	return outcome.Task, outcome.Run
}

func recoveryClaim(task domain.Task, source domain.Run, token string) domain.ClaimRequest {
	return domain.ClaimRequest{
		Command: domain.Command{OperationID: domain.NewID("reconcile"), ExpectedRevision: task.Revision},
		Kind:    domain.RecoveryRun, SourceRunID: source.ID, SpecGeneration: task.SpecGeneration,
		ProjectEpoch: task.Readiness.ProjectEpoch, Provider: source.Provider, Model: source.Model, TokenHash: domain.Hash(token),
	}
}

func TestTaskRecoveryReconcilesWithoutHumanApprovalOrCompletingExecution(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source := stoppedRecoveryTask(t, f)
	_, err := f.s.ChangeTask(ctx, f.worker.ID, task.ID, domain.TaskChange{
		Command: domain.Command{OperationID: domain.NewID("unsafe"), ExpectedRevision: task.Revision},
		Kind:    "retry-approve", Reason: "must not self-authorize an unknown replay",
		SpecGeneration: task.SpecGeneration, RunID: source.ID, Attempt: 2,
	})
	assertCode(t, err, domain.Denied)
	token := domain.NewToken()
	request := recoveryClaim(task, source, token)
	recovery, err := f.s.Claim(ctx, f.worker.ID, task.ID, request)
	if err != nil {
		t.Fatal("current assignee could not inspect its interrupted work", err)
	}
	replayed, err := f.s.Claim(ctx, f.worker.ID, task.ID, request)
	if err != nil || replayed.ID != recovery.ID {
		t.Fatal("lost recovery claim acknowledgement duplicated a run", replayed, err)
	}
	report := domain.ReportRequest{
		Command: op(), RunID: recovery.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "completed", Summary: "must not pretend reconciliation completed execution",
	}
	_, err = f.s.Report(ctx, f.worker.ID, task.ID, report)
	assertCode(t, err, domain.Invalid)
	report.Command, report.Outcome, report.SafeToRetry = op(), "reconciled", true
	report.Summary = "Verified the retained input and partial local output; no external operation needs replay."
	report.Evidence, report.Checkpoint = []string{"input SHA-256 verified", "partial output inspected"}, "invocation_recovery"
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Accepted || outcome.Stale || outcome.Task.UnsafeRetry || outcome.Task.Waiting != nil ||
		outcome.Task.Status != domain.Paused || outcome.Task.Result != nil || outcome.Task.ResultGeneration != 0 ||
		outcome.Task.LastRunID != source.ID || outcome.Task.Attempts != 1 || outcome.Task.RecoveryAttempts != 1 {
		t.Fatal("reconciliation changed execution accounting or claimed completion", outcome)
	}
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(request *domain.TaskChange) {
		request.SpecGeneration = task.SpecGeneration
	})
	executionToken := domain.NewToken()
	execution, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("continue"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(executionToken),
	})
	if err != nil || execution.Kind != domain.ExecutionRun || execution.Attempt != 2 || execution.TaskID != source.TaskID {
		t.Fatal("safe continuation did not use the same task and next execution attempt", execution, err)
	}
	if _, err := f.s.Report(ctx, f.worker.ID, task.ID, report); err != nil {
		t.Fatal("exact recovery report replay failed after execution continued", err)
	}
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.ActiveRun == nil || current.ActiveRun.ID != execution.ID || current.Attempts != 2 {
		t.Fatal("replaying reconciliation changed the newer execution", current, err)
	}
}

func TestTaskRecoveryRejectsChangedIdentityScopeProviderAndRun(t *testing.T) {
	cases := []struct {
		name   string
		actor  func(*fixture) string
		change func(*domain.ClaimRequest)
	}{
		{"different-assignee", func(f *fixture) string { return f.other.ID }, func(*domain.ClaimRequest) {}},
		{"different-run", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.SourceRunID = "run_different" }},
		{"different-specification", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.SpecGeneration++ }},
		{"different-project-epoch", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.ProjectEpoch++ }},
		{"different-provider", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.Provider = "copilot" }},
		{"different-model", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.Model = "unapproved-model" }},
		{"verification-result-field", func(f *fixture) string { return f.worker.ID }, func(r *domain.ClaimRequest) { r.ResultGeneration = 1 }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			f := newFixture(t)
			task, source := stoppedRecoveryTask(t, f)
			request := recoveryClaim(task, source, domain.NewToken())
			item.change(&request)
			if _, err := f.s.Claim(context.Background(), item.actor(f), task.ID, request); err == nil {
				t.Fatal("changed recovery context was accepted")
			}
			current, err := get[domain.Task](context.Background(), f.s.db, "tasks", task.ID)
			if err != nil || current.RecoveryAttempts != 0 || current.Attempts != 1 || !current.UnsafeRetry {
				t.Fatal("denied recovery changed existing fences or accounting", current, err)
			}
		})
	}
}

func TestTaskRecoveryRequiresGroundedEvidenceAndDoesNotRepeatBlockedReconciliation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source := stoppedRecoveryTask(t, f)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, token))
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "reconciled", SafeToRetry: true, Summary: "An unsupported assertion is not enough.",
		Checkpoint: "invocation_recovery",
	}
	_, err = f.s.Report(ctx, f.worker.ID, task.ID, report)
	assertCode(t, err, domain.Invalid)
	report.Command, report.Outcome, report.SafeToRetry = op(), "paused", false
	report.Summary = "The external mutation's result remains unknown; reconcile its original operation before continuing."
	report.Evidence = []string{"retained operation inspected; no authoritative result"}
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || !outcome.Task.UnsafeRetry || outcome.Task.Attempts != 1 || outcome.Task.RecoveryAttempts != 1 {
		t.Fatal("blocked reconciliation retired the original fence", outcome, err)
	}
	for range 3 {
		request := recoveryClaim(outcome.Task, source, domain.NewToken())
		_, err := f.s.Claim(ctx, f.worker.ID, task.ID, request)
		assertCode(t, err, domain.Prerequisite)
	}
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.RecoveryAttempts != 1 || current.Failures != task.Failures || current.RetryApproval != nil {
		t.Fatal("new operations reset the reconciliation or failure budget", current, err)
	}
}

func TestTaskRecoveryCannotClearAStopOrNewerSpecification(t *testing.T) {
	for _, kind := range []string{"stop", "edit"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			task, source := stoppedRecoveryTask(t, f)
			token := domain.NewToken()
			run, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, token))
			if err != nil {
				t.Fatal(err)
			}
			f.change(t, f.founder.ID, task.ID, kind, func(request *domain.TaskChange) {
				if kind == "edit" {
					description := "A newer specification must not inherit old recovery authority."
					request.Description = &description
				}
			})
			outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
				Command: op(), RunID: run.ID, Token: token, SpecGeneration: run.SpecGeneration,
				Outcome: "reconciled", SafeToRetry: true, Summary: "Old recovery completed after the change.",
				Evidence: []string{"old input checked"}, Checkpoint: "invocation_recovery",
			})
			if err != nil || !outcome.Stale || outcome.Accepted || !outcome.Task.UnsafeRetry || outcome.Task.Result != nil {
				t.Fatal("old reconciliation overrode newer stop/scope intent", outcome, err)
			}
		})
	}
}

func TestTaskRecoveryHonorsBackoffAndFailureBudget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	task = f.change(t, f.founder.ID, task.ID, "retry-policy", func(request *domain.TaskChange) {
		request.RetryPolicy = &domain.RetryPolicy{MaxRetries: 0, BackoffSeconds: 30}
	})
	task = f.change(t, f.founder.ID, task.ID, "approve", func(request *domain.TaskChange) { request.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(request *domain.TaskChange) { request.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(token),
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: run.SpecGeneration,
		Outcome: "failed", Summary: "Failed with retained effects.", Checkpoint: "invocation_owned",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	_, err = f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(outcome.Task, outcome.Run, domain.NewToken()))
	assertCode(t, err, domain.Prerequisite)
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.Failures != 1 || current.RecoveryAttempts != 0 || !current.UnsafeRetry {
		t.Fatal("reconciliation reset an exhausted failure budget", current, err)
	}
}

func TestUnusedRecoveryClaimDoesNotConsumeAnInvocationOrEraseExecutionEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source := stoppedRecoveryTask(t, f)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, token))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
		Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: domain.RecoveryNotStarted, Summary: "Exact claim reconciled before any provider invocation.",
		Checkpoint: "invocation_unused_recovery",
	})
	if err != nil || outcome.Task.RecoveryAttempts != 0 || outcome.Task.RecoveryRunID != "" ||
		!outcome.Task.UnsafeRetry || outcome.Task.Attempts != 1 || outcome.Task.LastRunID != source.ID {
		t.Fatal("unused reservation charged inference or retired original uncertainty", outcome, err)
	}
	next, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(outcome.Task, source, domain.NewToken()))
	if err != nil || next.ID == run.ID || next.Attempt != 1 || next.SourceRunID != source.ID {
		t.Fatal("an exact never-invoked claim stranded safe reconciliation", next, err)
	}
}

func TestTaskRecoveryIsBoundedAcrossSuccessiveInterruptedExecutions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, source := stoppedRecoveryTask(t, f)
	for attempt := 1; attempt <= domain.MaxTaskRecoveries; attempt++ {
		token := domain.NewToken()
		recovery, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, token))
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
			Command: op(), RunID: recovery.ID, Token: token, SpecGeneration: task.SpecGeneration,
			Outcome: "reconciled", SafeToRetry: true, Summary: "Inspected retained local progress.",
			Evidence: []string{"actual local outputs compared"}, Checkpoint: domain.NewID("invocation"),
		})
		if err != nil || outcome.Task.RecoveryAttempts != attempt {
			t.Fatal("recovery count did not retain the measured attempt", outcome, err)
		}
		task = f.change(t, f.worker.ID, task.ID, "clarity", func(request *domain.TaskChange) {
			request.SpecGeneration = task.SpecGeneration
		})
		token = domain.NewToken()
		execution, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
			Command:        domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision},
			SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(token),
		})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err = f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
			Command: op(), RunID: execution.ID, Token: token, SpecGeneration: task.SpecGeneration,
			Outcome: "stopped", Summary: "Another interrupted execution.", Checkpoint: domain.NewID("invocation"),
		})
		if err != nil {
			t.Fatal(err)
		}
		task, source = outcome.Task, outcome.Run
	}
	_, err := f.s.Claim(ctx, f.worker.ID, task.ID, recoveryClaim(task, source, domain.NewToken()))
	assertCode(t, err, domain.Prerequisite)
	current, err := get[domain.Task](ctx, f.s.db, "tasks", task.ID)
	if err != nil || current.RecoveryAttempts != domain.MaxTaskRecoveries || current.Attempts != domain.MaxTaskRecoveries+1 ||
		current.Failures != 0 || !current.UnsafeRetry {
		t.Fatal("automatic recovery exceeded its exact limit or rewrote original accounting", current, err)
	}
}
