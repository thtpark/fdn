//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type verificationExecutor struct {
	t         *testing.T
	decisions []string
	calls     int
}

func (x *verificationExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/synthetic/reviewer", Authentication: "authenticated", Models: []string{"review-model"}}, nil
}

func (x *verificationExecutor) Invoke(ctx context.Context, inv provider.Invocation) (provider.Reply, error) {
	_, raw, _ := strings.Cut(inv.Prompt, "\nFDN_CONTEXT_JSON\n")
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	if launch.Phase == "onboarding" {
		return provider.Reply{Outcome: "reviewed", Summary: "reviewer capabilities checked"}, nil
	}
	if launch.Phase != "verification" {
		x.t.Fatalf("unexpected reviewer phase: %s", launch.Phase)
	}
	if !inv.Coding || inv.Provider != "copilot" || inv.Model != "review-model" || inv.Workspace == "" {
		x.t.Fatal("verification must use the reviewer's tools, provider, model and private workspace", inv.Provider, inv.Model, inv.Coding)
	}
	if info, err := os.Stat(inv.Workspace); err != nil || !info.IsDir() {
		x.t.Fatal("verification workspace was not prepared", err)
	}
	if launch.Task == nil || launch.Task.Status != domain.Completed || launch.Task.Result == nil || launch.Project == nil {
		x.t.Fatal("verification omitted current completion context")
	}
	if x.calls >= len(x.decisions) {
		x.t.Fatal("verification repeated without a new result")
	}
	decision := x.decisions[x.calls]
	x.calls++
	data, err := json.Marshal(map[string]any{"outcome": "reviewed", "decision": decision, "summary": "Review: cover the missing boundary case before merging", "evidence": []string{"reviewed the exact result and pull request head"}, "questions": []any{}, "answer_ids": []string{}})
	if err != nil {
		return provider.Reply{}, err
	}
	var reply provider.Reply
	err = json.Unmarshal(data, &reply)
	return reply, err
}

func delegateProject(t *testing.T, h *harness, task domain.Task) domain.Project {
	t.Helper()
	ctx := context.Background()
	p, err := api.Call[domain.Project](ctx, h.founder, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err = api.Call[domain.Project](ctx, h.founder, "POST", api.Path("projects", p.ID, "delegates"), domain.GrantRequest{Command: domain.Command{OperationID: domain.NewID("delegate"), ExpectedRevision: p.Revision}, AgentID: h.otherAgent.ID})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func completedForVerification(t *testing.T, h *harness) (domain.Task, domain.Project) {
	t.Helper()
	ctx := context.Background()
	task := h.ready(t)
	p := delegateProject(t, h, task)
	task = h.change(t, h.founder, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	task = h.change(t, h.worker, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	run, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), domain.ClaimRequest{Command: domain.Command{OperationID: domain.NewID("execute"), ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := api.Call[domain.ReportOutcome](ctx, h.worker, "POST", api.Path("tasks", task.ID, "report"), domain.ReportRequest{Command: command(), RunID: run.ID, SpecGeneration: task.SpecGeneration, Token: token, Outcome: "completed", Summary: "pull request opened", Evidence: []string{"https://github.com/example/repository/pull/1"}})
	if err != nil {
		t.Fatal(err)
	}
	return out.Task, p
}

func verificationClaim(task domain.Task, p domain.Project, token string) map[string]any {
	return map[string]any{"operation_id": domain.NewID("verify"), "expected_revision": task.Revision, "kind": "verification", "spec_generation": task.SpecGeneration, "result_generation": task.ResultGeneration, "project_epoch": p.Epoch, "token_hash": domain.Hash(token), "provider": "copilot", "model": "review-model"}
}

func verificationReport(task domain.Task, run domain.Run, token, decision string) map[string]any {
	return map[string]any{"operation_id": domain.NewID("report"), "run_id": run.ID, "spec_generation": task.SpecGeneration, "result_generation": task.ResultGeneration, "token": token, "outcome": "completed", "decision": decision, "approve_rework": decision == "rework", "summary": "Review: cover the missing boundary case before merging", "evidence": []string{"verified current pull request head"}}
}

func TestDelegateControllerReviewsReworkAndAcceptsCurrentResult(t *testing.T) {
	for _, feedbackState := range []string{"pending", "deferred", "lost-ack"} {
		t.Run(feedbackState, func(t *testing.T) {
			h := newHarness(t)
			workerExecutor := &executor{}
			worker := newEngine(t, h, workerExecutor)
			task := readyForEngine(t, h, worker)
			delegateProject(t, h, task)
			task = h.change(t, h.founder, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
			ctx := context.Background()
			for range 3 {
				if _, err := worker.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			reviewer := newEngine(t, h, &executor{})
			reviewer.Client = h.other
			reviewer.Config.Provider, reviewer.Config.Model = "copilot", "review-model"
			x := &verificationExecutor{t: t, decisions: []string{"rework", "accept"}}
			reviewer.Executor = x
			for range 2 {
				if _, err := reviewer.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
			if err != nil {
				t.Fatal(err)
			}
			if x.calls != 1 || current.Status != domain.Todo || current.Readiness == nil || current.Readiness.ActorID != h.otherAgent.ID || current.Readiness.SpecGeneration != current.SpecGeneration {
				t.Fatalf("delegate did not request and authorize exact in-scope rework: calls=%d status=%s readiness=%v", x.calls, current.Status, current.Readiness)
			}
			entries, err := api.Call[domain.Page[domain.Entry]](ctx, h.worker, "GET", api.Path("tasks", task.ID, "entries"), nil)
			if err != nil {
				t.Fatal(err)
			}
			feedbackID := ""
			for _, entry := range entries.Items {
				if entry.Kind == "comment" && entry.AuthorID == h.otherAgent.ID && strings.Contains(entry.Body, "missing boundary case") && strings.Contains(entry.Body, "@{"+h.workerAgent.ID+"}") {
					feedbackID = entry.ID
				}
			}
			if feedbackID == "" {
				t.Fatal("delegate review was not published as actionable task feedback")
			}
			if feedbackState == "deferred" {
				if _, err := api.Call[domain.Attention](ctx, h.worker, "POST", api.Path("attention", feedbackID), domain.AttentionRequest{Command: command(), Generation: 1, State: "deferred"}); err != nil {
					t.Fatal(err)
				}
			}
			question, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{Command: command(), Kind: "comment", Body: "Separately explain the check to @{" + h.workerAgent.ID + "}"})
			if err != nil {
				t.Fatal(err)
			}
			steps := 2
			if feedbackState == "lost-ack" {
				original := h.worker.HTTP.Transport
				h.worker.HTTP.Transport = &lostVerificationAck{base: original, suffix: api.Path("attention", feedbackID)}
				defer func() { h.worker.HTTP.Transport = original }()
				before := workerExecutor.count("preflight")
				if _, err := worker.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Unavailable {
					t.Fatal("lost feedback acknowledgement was hidden", err)
				}
				if _, err := worker.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
				if workerExecutor.count("preflight") != before+1 {
					t.Fatal("feedback receipt replay repeated semantic review")
				}
				steps = 1
			}
			for range steps {
				if _, err := worker.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			attention, err := api.Call[domain.Page[domain.Attention]](ctx, h.worker, "GET", "/v1/attention", nil)
			if err != nil || len(attention.Items) != 1 || attention.Items[0].EntryID != question.ID {
				t.Fatal("preflight did not consume handled review feedback, or discarded an ordinary requested reply", feedbackID, attention.Items, err)
			}
			if _, err := reviewer.Step(ctx, false); err != nil {
				t.Fatal(err)
			}
			current, err = api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
			if err != nil || current.Status != domain.Done || current.Verification == nil || current.Verification.ActorID != h.otherAgent.ID || current.ResultGeneration != 2 || x.calls != 2 || workerExecutor.count("execution") != 2 {
				t.Fatal("revised result was not independently accepted", current.Status, err)
			}
			restarted := work.New(reviewer.Config, "", h.other, reviewer.State, x)
			for range 3 {
				if _, err := restarted.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			if x.calls != 2 {
				t.Fatal("accepted result caused duplicate verification after restart")
			}
		})
	}
}

func TestVerificationClaimOwnershipPolicyAndExactFences(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	token := domain.NewToken()
	request := verificationClaim(task, p, token)
	if _, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), request); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("non-delegate claimed independent verification", err)
	}
	request["result_generation"] = task.ResultGeneration + 1
	if _, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), request); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("review claimed a different result", err)
	}
	request["result_generation"] = task.ResultGeneration
	run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), request)
	if err != nil || replay.ID != run.ID {
		t.Fatal("verification claim did not replay exactly", err)
	}
	owned, err := api.Call[[]domain.Task](ctx, h.other, "GET", "/v1/ownership", nil)
	if err != nil || len(owned) != 1 || owned[0].ID != task.ID || owned[0].Status != domain.Completed {
		t.Fatal("verification did not own the serial server slot while retaining completed evidence", err)
	}
	if _, err := api.Call[domain.Task](ctx, h.founder, "POST", api.Path("tasks", task.ID, "change"), domain.TaskChange{Command: domain.Command{OperationID: domain.NewID("bypass"), ExpectedRevision: owned[0].Revision}, Kind: "verify", Reason: "bypass active review", Decision: "accept", SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration}); err == nil {
		t.Fatal("manual acceptance bypassed an active verifier")
	}
	report := verificationReport(task, run, token, "accept")
	if _, err := api.Call[domain.ReportOutcome](ctx, h.worker, "POST", api.Path("tasks", task.ID, "report"), report); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another actor reported the review", err)
	}
	report["result_generation"] = task.ResultGeneration + 1
	if _, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("report accepted a different result", err)
	}
	report["result_generation"] = task.ResultGeneration
	out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report)
	if err != nil || !out.Accepted || out.Task.Status != domain.Done || out.Task.ActiveRun != nil {
		t.Fatal("current review was not accepted", err)
	}
	again, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), report)
	if err != nil || again.Run.ID != out.Run.ID || again.Task.ResultGeneration != out.Task.ResultGeneration {
		t.Fatal("review report replay changed completion", err)
	}
}

func TestVerificationClaimRacesShareOneSlot(t *testing.T) {
	h := newHarness(t)
	task, p := completedForVerification(t, h)
	ctx := context.Background()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, domain.NewToken()))
			if err == nil {
				accepted.Add(1)
			} else if code := domain.AsFault(err).Code; code != domain.Conflict && code != domain.Prerequisite {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("concurrent reviews admitted", accepted.Load())
	}
}

func TestVerificationChangedDiscussionOrAuthorityCannotAccept(t *testing.T) {
	for _, change := range []string{"discussion", "authority", "specification", "withdraw", "mode"} {
		t.Run(change, func(t *testing.T) {
			h := newHarness(t)
			task, p := completedForVerification(t, h)
			ctx := context.Background()
			token := domain.NewToken()
			run, err := api.Call[domain.Run](ctx, h.other, "POST", api.Path("tasks", task.ID, "claim"), verificationClaim(task, p, token))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "discussion":
				_, err = api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{Command: command(), Kind: "comment", Body: "An additional constraint needs review"})
			case "authority":
				_, err = api.Call[domain.Project](ctx, h.founder, "POST", api.Path("projects", p.ID, "delegates"), domain.GrantRequest{Command: domain.Command{OperationID: domain.NewID("revoke"), ExpectedRevision: p.Revision}, AgentID: h.otherAgent.ID, Revoke: true})
			case "specification":
				h.change(t, h.founder, task.ID, "edit", func(r *domain.TaskChange) { value := "changed criteria"; r.AcceptanceCriteria = &value })
			case "withdraw":
				h.change(t, h.founder, task.ID, "withdraw", nil)
			case "mode":
				_, err = api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.otherAgent.ID, "mode"), domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("pause"), ExpectedRevision: h.otherAgent.Revision}, Mode: "manual", Reason: "stop review"})
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := api.Call[domain.ReportOutcome](ctx, h.other, "POST", api.Path("tasks", task.ID, "report"), verificationReport(task, run, token, "accept"))
			if err != nil || out.Accepted || !out.Stale || out.Task.Status == domain.Done || out.Task.ActiveRun != nil || out.Task.Verification != nil {
				t.Fatal("stale verification accepted or retained ownership", change, out.Task.Status, err)
			}
		})
	}
}

type lostVerificationAck struct {
	base   http.RoundTripper
	suffix string
	lost   atomic.Bool
}

func (l *lostVerificationAck) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := l.base.RoundTrip(r)
	suffix := l.suffix
	if suffix == "" {
		suffix = "/report"
	}
	if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, suffix) && l.lost.CompareAndSwap(false, true) {
		_, copyErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		return nil, errors.Join(errors.New("lost review acknowledgement"), copyErr, closeErr)
	}
	return response, err
}

func TestVerificationLostAcknowledgementDoesNotReinvokeTools(t *testing.T) {
	h := newHarness(t)
	task, _ := completedForVerification(t, h)
	reviewer := newEngine(t, h, &executor{})
	reviewer.Client = h.other
	reviewer.Config.Provider, reviewer.Config.Model = "copilot", "review-model"
	x := &verificationExecutor{t: t, decisions: []string{"accept"}}
	reviewer.Executor = x
	ctx := context.Background()
	if _, err := reviewer.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	original := h.other.HTTP.Transport
	h.other.HTTP.Transport = &lostVerificationAck{base: original}
	defer func() { h.other.HTTP.Transport = original }()
	if _, err := reviewer.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("lost verification acknowledgement was hidden", err)
	}
	outbox, err := reviewer.State.Outbox(ctx)
	if err != nil || len(outbox) != 1 {
		t.Fatal("verification report not durably queued", err)
	}
	restarted := work.New(reviewer.Config, "", h.other, reviewer.State, x)
	if _, err := restarted.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	current, err := api.Call[domain.Task](ctx, h.other, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || current.Status != domain.Done || x.calls != 1 {
		t.Fatal("lost acknowledgement repeated merge/review tools", err, x.calls)
	}
	outbox, err = reviewer.State.Outbox(ctx)
	if err != nil || len(outbox) != 0 {
		t.Fatal("replayed verification report not acknowledged", err)
	}
	if _, err := os.Stat(filepath.Join(reviewer.Config.WorkspaceRoot, task.ProjectID)); err != nil {
		t.Fatal("reviewer did not receive its own reusable project workspace", err)
	}
}
