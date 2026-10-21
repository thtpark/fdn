//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

func TestVerificationAndExecutionShareServerSlot(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	otherTask := h.ready(t)
	otherTask = h.change(t, h.founder, otherTask.ID, "assign", func(r *domain.TaskChange) { r.AgentID = h.otherAgent.ID; r.Provider = "copilot" })
	otherTask = h.change(t, h.other, otherTask.ID, "workspace", func(r *domain.TaskChange) { r.WorkspaceRef = "reviewer_workspace" })
	otherTask = h.change(t, h.founder, otherTask.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = otherTask.SpecGeneration })
	otherTask = h.change(t, h.other, otherTask.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = otherTask.SpecGeneration })
	ctx := context.Background()
	token := domain.NewToken()
	if _, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", otherTask.ID, "claim"), domain.ClaimRequest{Command: domain.Command{OperationID: domain.NewID("second"), ExpectedRevision: otherTask.Revision}, SpecGeneration: otherTask.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "copilot"}); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("execution was admitted alongside verification on the same computer", err)
	}
}

func TestVerificationPoliciesAndFounderPauseAreServerEnforced(t *testing.T) {
	for _, policy := range []string{"named", "self", "paused", "epoch"} {
		t.Run(policy, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			ctx := context.Background()
			switch policy {
			case "named":
				task = h.change(t, h.founder, task.ID, "policy", func(r *domain.TaskChange) {
					r.Policy = &domain.ApprovalPolicy{RequiredActors: []string{h.founderAgent.ID}, IndependentVerification: true}
				})
			case "self":
				task = h.change(t, h.founder, task.ID, "assign", func(r *domain.TaskChange) { r.AgentID = h.otherAgent.ID; r.Provider = "copilot" })
			case "paused":
				if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.otherAgent.ID, "mode"), domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("pause"), ExpectedRevision: h.otherAgent.Revision}, Mode: "manual", Reason: "hold reviews"}); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				p.Epoch--
			}
			_, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, domain.NewToken()))
			if err == nil {
				t.Fatal("verification bypassed current policy", policy)
			}
			want := domain.Denied
			if policy == "paused" {
				want = domain.Prerequisite
			} else if policy == "epoch" {
				want = domain.Conflict
			}
			if domain.AsFault(err).Code != want {
				t.Fatal("unexpected policy error", err)
			}
		})
	}
}

func TestExecutionReportCannotMakeVerificationDecision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.ready(t)
	token := domain.NewToken()
	run, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), domain.ClaimRequest{Command: domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	report := verificationReport(task, run, token, "accept")
	if _, err := api.Call[domain.ReportOutcome](ctx, h.worker, "POST", api.Path("tasks", task.ID, "report"), report); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("execution accepted its own result", err)
	}
}

func TestVerificationRejectsMalformedDecisionsWithoutReleasingOwnership(t *testing.T) {
	for _, invalid := range []string{"missing-decision", "missing-evidence", "safe-retry", "assignee-blocker", "unrelated-approval"} {
		t.Run(invalid, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			ctx := context.Background()
			token := domain.NewToken()
			run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
			if err != nil {
				t.Fatal(err)
			}
			report := verificationReport(task, run, token, "accept")
			switch invalid {
			case "missing-decision":
				report["decision"] = ""
			case "missing-evidence":
				report["evidence"] = []string{}
			case "safe-retry":
				report["safe_to_retry"] = true
			case "assignee-blocker":
				report["waiting"] = domain.Waiting{Kind: "clarification", Reason: "not an assignee report"}
			case "unrelated-approval":
				report["approve_rework"] = true
			}
			if _, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report); err == nil || domain.AsFault(err).Code != domain.Invalid {
				t.Fatal("malformed verification was accepted", invalid, err)
			}
			owned, err := api.Call[[]domain.Task](ctx, h.other, "GET", "/v1/ownership", nil)
			if err != nil || len(owned) != 1 || owned[0].ActiveRun == nil || owned[0].ActiveRun.ID != run.ID || owned[0].Status != domain.Completed {
				t.Fatal("rejected report partially changed ownership or completion", err)
			}
		})
	}
}

func TestVerificationUncertainReportsBlockAutomaticRetry(t *testing.T) {
	for _, outcome := range []string{"failed", "paused", "interrupted", "stopped"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			ctx := context.Background()
			token := domain.NewToken()
			run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
			if err != nil {
				t.Fatal(err)
			}
			report := verificationReport(task, run, token, "")
			report["outcome"] = outcome
			out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report)
			if err != nil || out.Task.Status != domain.Completed || out.Task.Waiting == nil || out.Task.Waiting.Kind != "verification" || out.Task.ActiveRun != nil || out.Task.Failures != task.Failures || out.Task.ResultGeneration != task.ResultGeneration {
				t.Fatal("uncertain review did not preserve completed evidence and block retry", err)
			}
			if _, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(out.Task, p, domain.NewToken())); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
				t.Fatal("uncertain external verification was retried", err)
			}
			current := h.change(t, h.founder, task.ID, "verify", func(r *domain.TaskChange) {
				r.Decision = "accept"
				r.SpecGeneration = task.SpecGeneration
				r.ResultGeneration = task.ResultGeneration
			})
			if current.Status != domain.Done || current.Waiting != nil {
				t.Fatal("explicit inspection/acceptance could not resolve review uncertainty")
			}
		})
	}
}

func TestVerificationCannotApproveReworkForRevokedAssignee(t *testing.T) {
	for _, timing := range []string{"before-claim", "after-claim"} {
		t.Run(timing, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			ctx := context.Background()
			revoke := func() {
				t.Helper()
				if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "revoke"), domain.AgentRequest{Command: command()}); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before-claim" {
				revoke()
				var err error
				task, err = api.Call[domain.Task](ctx, h.other, "GET", api.Path("tasks", task.ID), nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			token := domain.NewToken()
			run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
			if err != nil {
				t.Fatal(err)
			}
			if timing == "after-claim" {
				revoke()
			}
			out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), verificationReport(task, run, token, "rework"))
			if err != nil || out.Accepted || !out.Stale || out.Task.ActiveRun != nil || out.Task.Status != domain.Completed || out.Task.Waiting == nil || out.Task.Waiting.Kind != "verification" || (out.Task.Readiness != nil && out.Task.Readiness.ActorID == h.otherAgent.ID) {
				t.Fatal("rework gave fresh readiness to a revoked assignee", err)
			}
		})
	}
}

func TestRevokingVerifierStopsOwnershipUntilExplicitRelease(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	token := domain.NewToken()
	run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.otherAgent.ID, "revoke"), domain.AgentRequest{Command: command()}); err != nil {
		t.Fatal(err)
	}
	current, err := api.Call[domain.Task](ctx, h.founder, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || current.ActiveRun == nil || current.ActiveRun.ID != run.ID || !current.ActiveRun.StopRequested {
		t.Fatal("revocation did not stop the active verifier while retaining its process fence", err)
	}
	if _, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), verificationReport(task, run, token, "accept")); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("revoked verifier could accept work", err)
	}
	current = h.change(t, h.founder, task.ID, "release", func(r *domain.TaskChange) { r.RunID = run.ID; r.StoppedAcknowledged = true })
	if current.ActiveRun != nil || current.Status != domain.Completed || current.Waiting == nil || current.Waiting.Kind != "verification" {
		t.Fatal("revoked verifier ownership was not explicitly reconciled")
	}
}

func TestVerificationPauseAndResumeStillInvalidatesPriorClaim(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	token := domain.NewToken()
	run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
	if err != nil {
		t.Fatal(err)
	}
	agent := h.otherAgent
	for _, mode := range []string{"manual", "ai"} {
		agent, err = api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", agent.ID, "mode"), domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("mode"), ExpectedRevision: agent.Revision}, Mode: mode, Reason: "pause then resume must not resurrect old review authority"})
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), verificationReport(task, run, token, "accept"))
	if err != nil || out.Accepted || !out.Stale || out.Task.Status == domain.Done || out.Task.ActiveRun != nil {
		t.Fatal("pause/resume resurrected an earlier verification claim", err)
	}
}

func TestVerificationReleasePreservesResultAndDoesNotChargeWorkerRetry(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, domain.NewToken()))
	if err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.founder, task.ID, "release", func(r *domain.TaskChange) { r.RunID = run.ID; r.StoppedAcknowledged = true })
	if task.Status != domain.Completed || task.ActiveRun != nil || task.Waiting == nil || task.Waiting.Kind != "verification" || task.Result == nil || task.Failures != 0 || task.UnsafeRetry {
		t.Fatal("review release corrupted the worker's execution/result state")
	}
}

func TestVerificationPreparedReplyAndInterruptedRecovery(t *testing.T) {
	for _, phase := range []string{"reply-prepared", "claim-prepared", "invoking"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			reviewer := newEngine(t, h, &executor{})
			reviewer.Client = h.other
			ctx := context.Background()
			token := domain.NewToken()
			request := verificationClaim(task, p, token)
			bytes, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var claim domain.ClaimRequest
			if err := json.Unmarshal(bytes, &claim); err != nil {
				t.Fatal(err)
			}
			run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), claim)
			if err != nil {
				t.Fatal(err)
			}
			launch, err := json.Marshal(work.Launch{Phase: "verification", AgentID: h.otherAgent.ID, Task: &task, Project: &p})
			if err != nil {
				t.Fatal(err)
			}
			j := local.Journal{ID: "verification-recovery", Kind: "verification", State: phase, TaskID: task.ID, RunID: run.ID, Claim: &claim, Token: token, SpecGeneration: task.SpecGeneration, Context: launch, Reply: json.RawMessage(`{"outcome":"reviewed","decision":"accept","summary":"merge and checks confirmed","evidence":["merged exact reviewed head"]}`)}
			if err := reviewer.State.SaveJournal(ctx, j); err != nil {
				t.Fatal(err)
			}
			if phase == "invoking" {
				if _, err := reviewer.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
					t.Fatal("interrupted verification was not fenced", err)
				}
				if err := reviewer.Recover(ctx, j.ID, true); err != nil {
					t.Fatal(err)
				}
			} else if _, err := reviewer.Step(ctx, false); err != nil {
				t.Fatal(err)
			}
			current, err := api.Call[domain.Task](ctx, h.other, "GET", api.Path("tasks", task.ID), nil)
			if err != nil || current.ActiveRun != nil || current.Result == nil || current.ResultGeneration != task.ResultGeneration {
				t.Fatal("verification recovery lost evidence or ownership", err)
			}
			if phase == "reply-prepared" && current.Status != domain.Done {
				t.Fatal("prepared verification reply was not reported without inference")
			}
			if phase != "reply-prepared" && (current.Status != domain.Completed || current.Waiting == nil || current.Waiting.Kind != "verification") {
				t.Fatal("uncertain recovered verification became retryable")
			}
		})
	}
}

func TestVerificationLostReworkAcknowledgementReplaysExactApprovalAndComment(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	token := domain.NewToken()
	run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
	if err != nil {
		t.Fatal(err)
	}
	report := verificationReport(task, run, token, "rework")
	for range 2 {
		out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report)
		if err != nil || !out.Accepted || out.Stale || out.Task.Status != domain.Todo || out.Task.SpecGeneration != task.SpecGeneration+1 || out.Task.Readiness == nil || out.Task.Readiness.ActorID != h.otherAgent.ID {
			t.Fatal("rework replay invalidated its own fresh approval", err)
		}
	}
	entries, err := api.Call[domain.Page[domain.Entry]](ctx, h.other, "GET", api.Path("tasks", task.ID, "entries"), nil)
	if err != nil || len(entries.Items) != 1 {
		t.Fatal("review replay duplicated its task feedback", err, len(entries.Items))
	}
}

type stoppingVerificationExecutor struct {
	verificationExecutor
	failure bool
	entered chan struct{}
}

func (x *stoppingVerificationExecutor) Invoke(ctx context.Context, inv provider.Invocation) (provider.Reply, error) {
	if !strings.Contains(inv.Prompt, `"phase":"verification"`) {
		return x.verificationExecutor.Invoke(ctx, inv)
	}
	x.calls++
	close(x.entered)
	if x.failure {
		return provider.Reply{}, errors.New("private verifier failure")
	}
	<-ctx.Done()
	return provider.Reply{}, ctx.Err()
}

func TestVerificationCancellationAndFailureNeverRerunTools(t *testing.T) {
	for _, cancellation := range []string{"failure", "discussion", "mode", "workspace"} {
		t.Run(cancellation, func(t *testing.T) {
			h := newHarness(t)
			task, _ := completedForVerification(t, h)
			reviewer := newEngine(t, h, &executor{})
			reviewer.Client = h.other
			reviewer.Config.Provider, reviewer.Config.Model = "copilot", "review-model"
			x := &stoppingVerificationExecutor{verificationExecutor: verificationExecutor{t: t}, failure: cancellation == "failure", entered: make(chan struct{})}
			reviewer.Executor = x
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if _, err := reviewer.Step(ctx, false); err != nil {
				t.Fatal(err)
			}
			mutated := make(chan struct{})
			if cancellation != "failure" {
				go func() {
					defer close(mutated)
					select {
					case <-x.entered:
					case <-ctx.Done():
						return
					}
					switch cancellation {
					case "discussion":
						_, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{Command: command(), Kind: "comment", Body: "Stop and review this new input"})
						if err != nil {
							t.Error(err)
						}
					case "mode":
						_, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.otherAgent.ID, "mode"), domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("pause"), ExpectedRevision: h.otherAgent.Revision}, Mode: "manual", Reason: "cancel review"})
						if err != nil {
							t.Error(err)
						}
					case "workspace":
						bindings, err := reviewer.State.Bindings(ctx)
						if err != nil || len(bindings) != 1 {
							t.Error("review workspace not bound", err)
							return
						}
						if err := reviewer.State.EnableWorkspace(ctx, bindings[0].Ref, false); err != nil {
							t.Error(err)
						}
					}
				}()
			}
			if _, err := reviewer.Step(ctx, true); err == nil || domain.AsFault(err).Code != domain.Provider {
				t.Fatal("review failure/cancellation not reported", err)
			}
			if cancellation != "failure" {
				<-mutated
			}
			if ctx.Err() != nil {
				t.Fatal("review cancellation relied on the outer test timeout")
			}
			for range 2 {
				if _, err := reviewer.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			current, err := api.Call[domain.Task](ctx, h.other, "GET", api.Path("tasks", task.ID), nil)
			if err != nil || current.Status != domain.Completed || current.ActiveRun != nil || x.calls != 1 || current.Waiting == nil || current.Waiting.Kind != "verification" {
				t.Fatal("uncertain review repeated or became done", err)
			}
		})
	}
}

func TestReviewFeedbackProvenanceCannotBeForgedByOrdinaryComments(t *testing.T) {
	h := newHarness(t)
	task := h.ready(t)
	ctx := context.Background()
	request := map[string]any{"operation_id": domain.NewID("forge"), "kind": "comment", "body": "Not a verified review", "review": map[string]any{"run_id": "invented", "spec_generation": task.SpecGeneration, "result_generation": 1, "decision": "rework"}}
	if _, err := api.Call[domain.Entry](ctx, h.worker, "POST", api.Path("tasks", task.ID, "entries"), request); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("ordinary comment forged protected review provenance", err)
	}
}

func TestVerificationCannotBypassDisabledLocalToolBinding(t *testing.T) {
	h := newHarness(t)
	task, _ := completedForVerification(t, h)
	reviewer := newEngine(t, h, &executor{})
	reviewer.Client = h.other
	reviewer.Config.Provider, reviewer.Config.Model = "copilot", "review-model"
	x := &verificationExecutor{t: t, decisions: []string{"accept"}}
	reviewer.Executor = x
	ctx := context.Background()
	path := filepath.Join(reviewer.Config.WorkspaceRoot, task.ProjectID)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	binding, err := reviewer.State.BindWorkspace(ctx, path, true, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reviewer.State.EnableWorkspace(ctx, binding.Ref, false); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := reviewer.Step(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	if x.calls != 0 {
		t.Fatal("disabled local binding did not prevent verification tools")
	}
	bindings, err := reviewer.State.Bindings(ctx)
	if err != nil || len(bindings) != 1 || bindings[0].Enabled {
		t.Fatal("verification bypassed disabled binding with a replacement", err)
	}
}
