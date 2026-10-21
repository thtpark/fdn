//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
	"github.com/thtpark/fdn/tests/scenario"
)

func assertScenarioResults(t *testing.T, l *lab, delegate *worker, github *scenario.GitHub, approved, final domain.Task, workerTrace, delegateTrace scenario.Trace, answerIDs []string) {
	t.Helper()
	if final.Status != domain.Done || final.ActiveRun != nil || final.Waiting != nil || final.SpecGeneration != approved.SpecGeneration+1 || final.ResultGeneration != 2 || final.Attempts != 1 {
		t.Fatalf("wrong final task fences/status: status=%s spec=%d result=%d attempts=%d", final.Status, final.SpecGeneration, final.ResultGeneration, final.Attempts)
	}
	if final.CreatedBy != l.founder.agent.ID || final.AssigneeID != l.worker.agent.ID || final.Provider != l.worker.config.Provider || final.Model != "fixture-worker-model" || final.WorkspaceRef != l.binding.Ref {
		t.Fatal("delegate verification changed the original worker/provider/workspace assignment")
	}
	if final.Description != approved.Description || final.AcceptanceCriteria != approved.AcceptanceCriteria {
		t.Fatal("in-scope rework silently changed the agreed task text")
	}
	if final.Readiness == nil || final.Readiness.ActorID != delegate.agent.ID || final.Readiness.SpecGeneration != final.SpecGeneration || final.Readiness.ProjectEpoch != l.project.Epoch {
		t.Fatal("in-scope rework did not record a fresh exact delegate readiness approval")
	}
	if final.Verification == nil || final.Verification.ActorID != delegate.agent.ID || final.Verification.SpecGeneration != final.SpecGeneration || final.Verification.ResultGeneration != final.ResultGeneration || final.Verification.ProjectEpoch != l.project.Epoch {
		t.Fatal("done was not independently accepted against exact spec/result/authority generations")
	}
	executions, verifications := callsFor(workerTrace, "execution"), callsFor(delegateTrace, "verification")
	if len(executions) != 2 || len(verifications) != 2 || len(callsFor(workerTrace, "verification")) != 0 || len(callsFor(delegateTrace, "execution")) != 0 {
		t.Fatal("workflow did not use exactly two worker executions and two independent delegate verifications")
	}
	for _, trace := range []scenario.Trace{workerTrace, delegateTrace} {
		for _, call := range trace.Calls {
			if !call.Completed {
				t.Fatal("a fixture invocation was interrupted, failed, or reinvoked")
			}
			if call.Coding != (call.Phase == "execution" || call.Phase == "verification") {
				t.Fatal("text review was tool-enabled or a coding/review call was tool-disabled")
			}
		}
	}
	for index, call := range executions {
		if call.ActorID != l.worker.agent.ID || call.Provider != l.worker.config.Provider || call.Model != "fixture-worker-model" || call.Workspace != l.workspace || call.Task == nil || call.Task.SpecGeneration != approved.SpecGeneration+int64(index) {
			t.Fatal("worker execution attribution, workspace, model, or exact specification was lost")
		}
		if call.Task.Readiness == nil || call.Task.Readiness.SpecGeneration != call.Task.SpecGeneration || call.Task.Clarity == nil || call.Task.Clarity.DiscussionGeneration != call.Task.DiscussionGeneration || call.Task.Clarity.SpecGeneration != call.Task.SpecGeneration || call.Task.Clarity.ActorID != l.worker.agent.ID {
			t.Fatal("worker executed without exact current readiness and discussion-bound clarity")
		}
		approver := l.founder.agent.ID
		if index == 1 {
			approver = delegate.agent.ID
		}
		if call.Task.Readiness.ActorID != approver {
			t.Fatal("initial execution bypassed Founder or rework reused stale Founder approval")
		}
	}
	for index, call := range verifications {
		if call.ActorID != delegate.agent.ID || call.Provider != delegate.config.Provider || call.Model != "fixture-review-model" || call.Task == nil || call.Task.AssigneeID != l.worker.agent.ID || call.Task.Provider != l.worker.config.Provider || call.Task.Model != "fixture-worker-model" || call.Task.Status != domain.Completed || call.Task.SpecGeneration != approved.SpecGeneration+int64(index) || call.Task.ResultGeneration != int64(index+1) {
			t.Fatal("verification did not preserve the task assignment while using the delegate's own provider/model and exact result")
		}
		if call.Workspace == l.workspace || !strings.HasPrefix(call.Workspace, delegate.config.WorkspaceRoot+"/") {
			t.Fatal("independent reviews did not use the delegate's own private checkout")
		}
		var reply provider.Reply
		if err := json.Unmarshal(call.Reply, &reply); err != nil {
			t.Fatal(err)
		}
		decision := "rework"
		if index == 1 {
			decision = "accept"
		}
		if reply.Outcome != "reviewed" || reply.Decision != decision || reply.Summary == "" || len(reply.Evidence) == 0 || len(reply.Questions) != 0 || reply.Description != nil || reply.AcceptanceCriteria != nil {
			t.Fatal("verification returned a text/preparation or question-based shortcut instead of an intentional reviewed decision")
		}
	}
	incorporated := []string{}
	for _, call := range workerTrace.Calls {
		if call.Rule != "incorporate-founder-reply" {
			continue
		}
		var reply provider.Reply
		if err := json.Unmarshal(call.Reply, &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.AnswerIDs) != 1 || reply.Description == nil || reply.AcceptanceCriteria == nil {
			t.Fatal("answer refinement did not reference the actual requested reply")
		}
		incorporated = append(incorporated, reply.AnswerIDs...)
	}
	if !slices.Equal(incorporated, answerIDs) || len(incorporated) != 4 {
		t.Fatal("three-to-four productive clarification rounds did not incorporate the actual Founder replies in order")
	}
	entries := scenarioDiscussion(t, l, final.ID)
	reworkCount, acceptanceCount := 0, 0
	var reworkEntry domain.Entry
	for _, entry := range entries {
		if entry.Kind != "comment" || entry.AuthorID != delegate.agent.ID {
			continue
		}
		if strings.Contains(entry.Body, retryReview) {
			reworkCount++
			reworkEntry = entry
			if !strings.Contains(entry.Body, "@{"+l.worker.agent.ID+"}") || !slices.Contains(entry.Mentions, l.worker.agent.ID) {
				t.Fatal("actionable delegate rework omitted the assignee's literal mention")
			}
		}
		if strings.Contains(entry.Body, retryAcceptance) {
			acceptanceCount++
		}
	}
	if reworkCount != 1 || acceptanceCount != 1 {
		t.Fatal("atomic delegate decisions did not publish exactly one intentional review comment each")
	}
	for _, call := range []scenario.Call{executions[1], verifications[1]} {
		found := false
		for _, entry := range call.Discussion {
			if entry.ID == reworkEntry.ID && entry.Body == reworkEntry.Body {
				found = true
			}
		}
		if !found {
			t.Fatal("rework execution or corrected-head verification omitted the delegate's full discussion comment")
		}
	}
	var runs []domain.Run
	if err := json.Unmarshal(l.cli(t, l.founder, []string{"task", "runs", final.ID}, nil), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatal("execution and verification claims were not all durably projected", len(runs))
	}
	journalByRun := map[string]struct {
		kind, provider, model string
		launch                work.Launch
	}{}
	for _, identity := range []*worker{l.worker, delegate} {
		for _, journal := range scenarioJournals(t, identity) {
			if journal.RunID == "" {
				continue
			}
			var launch work.Launch
			if err := json.Unmarshal(journal.Context, &launch); err != nil {
				t.Fatal(err)
			}
			journalByRun[journal.RunID] = struct {
				kind, provider, model string
				launch                work.Launch
			}{journal.Kind, journal.Provider, journal.Model, launch}
		}
	}
	verificationRuns, executionRuns := 0, 0
	for _, run := range runs {
		journal, found := journalByRun[run.ID]
		if !found || journal.kind != run.Kind || journal.provider != run.Provider || journal.model != run.Model || journal.launch.Task == nil || journal.launch.Task.SpecGeneration != run.SpecGeneration || run.TaskID != final.ID || run.FinishedAt == 0 || run.FinishedAt < run.StartedAt {
			t.Fatal("shared run lost its exact durable local journal, provider/model, specification, or finished ownership")
		}
		switch run.Kind {
		case "execution":
			executionRuns++
			if run.AgentID != l.worker.agent.ID || run.Provider != l.worker.config.Provider || run.Model != "fixture-worker-model" || run.Attempt != 1 {
				t.Fatal("worker execution projection attribution or per-specification attempt accounting was lost")
			}
		case "verification":
			verificationRuns++
			if run.AgentID != delegate.agent.ID || run.Provider != delegate.config.Provider || run.Model != "fixture-review-model" || run.ResultGeneration != journal.launch.Task.ResultGeneration || run.DiscussionGeneration != journal.launch.Task.DiscussionGeneration || run.ProjectEpoch != l.project.Epoch {
				t.Fatal("verification projection did not bind independent actor/provider and exact result/discussion/project epoch")
			}
		default:
			t.Fatal("new workflow run omitted its execution/verification kind")
		}
	}
	if executionRuns != 2 || verificationRuns != 2 {
		t.Fatal("workflow run kinds did not reflect execution-review-rework-review")
	}
	assertScenarioGitHub(t, github, l.worker, delegate)
}

func assertScenarioGitHub(t *testing.T, github *scenario.GitHub, worker, delegate *worker) {
	t.Helper()
	pr, events := github.Snapshot()
	kinds := []string{}
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	if !slices.Equal(kinds, []string{"created", "review-rework", "head-updated", "review-accept", "merged"}) || pr.Number != 1 || !pr.Merged || pr.State != "closed" || pr.Head.Ref != "fdn/retry" || pr.Base.Ref != "main" {
		t.Fatal("same-PR head/review/merge order was not preserved", kinds)
	}
	if events[0].Head == events[2].Head || events[0].Head != events[1].Head || events[2].Head != events[3].Head || events[3].Head != events[4].Head || pr.Head.SHA != events[4].Head {
		t.Fatal("delegate did not review the original and corrected heads before merge")
	}
	for index, event := range events {
		actor, provider, model := delegate.agent.ID, delegate.config.Provider, "fixture-review-model"
		if event.Kind == "created" || event.Kind == "head-updated" {
			actor, provider, model = worker.agent.ID, worker.config.Provider, "fixture-worker-model"
		}
		if event.ActorID != actor || event.Provider != provider || event.Model != model || event.Sequence != index+1 {
			t.Fatal("GitHub fixture actor/provider attribution or event ordering was lost")
		}
	}
	git := func(args ...string) string {
		t.Helper()
		output, err := scenario.Git(context.Background(), github.Seed, append([]string{"--git-dir", github.Origin}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if got := git("show", "main:src/openai/_retry.py"); got+"\n" != scenario.FixedRetry {
		t.Fatal("merged main does not contain the actual corrected retry code")
	}
	if got := git("show", "main:tests/test_retry.py"); got+"\n" != scenario.FixedTests {
		t.Fatal("merged main does not contain the negative-attempt regression assertion")
	}
	if got := git("diff", "--name-only", pr.Base.SHA, pr.Head.SHA); got != "src/openai/_retry.py\ntests/test_retry.py" {
		t.Fatal("actual Git diff escaped the two agreed files", got)
	}
	if got := git("rev-list", "--count", pr.Base.SHA+".."+pr.Head.SHA); got != "2" {
		t.Fatal("rework did not append exactly one commit on the same PR branch", got)
	}
	parents := strings.Fields(git("rev-list", "--parents", "-n", "1", "main"))
	if len(parents) != 3 || parents[0] != pr.MergeCommitSHA || parents[1] != pr.Base.SHA || parents[2] != pr.Head.SHA {
		t.Fatal("fixture merge did not actually merge the reviewed Git head")
	}
	git("diff", "--check", pr.Base.SHA, pr.Head.SHA)
}
