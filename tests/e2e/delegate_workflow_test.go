//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
	"github.com/thtpark/fdn/tests/scenario"
)

func TestNativeDelegatedPullRequestWorkflow(t *testing.T) {
	for _, providers := range []struct{ worker, delegate string }{{"codex", "copilot"}, {"copilot", "codex"}} {
		t.Run(providers.worker+"-worker", func(t *testing.T) {
			l := newLab(t, providers.worker)
			invitation := filepath.Join(l.root, "transfer", "delegate-invitation.json")
			l.cli(t, l.founder, []string{"invitation", "create", "--output", invitation}, nil)
			delegate := l.newWorker(t, "delegate", providers.delegate, invitation, filepath.Join(l.root, "foundation"))
			if err := json.Unmarshal(l.cli(t, l.founder, []string{"project", "delegate", l.project.ID, "--input", "-"}, domain.GrantRequest{AgentID: delegate.agent.ID}), &l.project); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(l.project.Delegates, l.worker.agent.ID) || !slices.Contains(l.project.Delegates, delegate.agent.ID) {
				t.Fatal("task worker must be a non-delegate, distinct from its verifier")
			}
			github, err := scenario.NewGitHub(filepath.Join(l.root, "github"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(github.Close)
			const workerModel, delegateModel = "fixture-worker-model", "fixture-review-model"
			workerToken := github.Authorize(scenario.GitHubIdentity{ActorID: l.worker.agent.ID, Provider: providers.worker, Model: workerModel})
			delegateToken := github.Authorize(scenario.GitHubIdentity{ActorID: delegate.agent.ID, Provider: providers.delegate, Model: delegateModel, Reviewer: true})
			for _, preference := range []struct {
				identity *worker
				model    string
			}{{l.worker, workerModel}, {delegate, delegateModel}} {
				l.cli(t, preference.identity, []string{"preferences", "set", "--input", "-"}, map[string]string{"provider": preference.identity.config.Provider, "model": preference.model})
			}
			if err := scenario.WritePlan(workerHome(l.worker), workerPlan(l, l.worker, github, workerToken, workerModel)); err != nil {
				t.Fatal(err)
			}
			if err := scenario.WritePlan(workerHome(delegate), delegatePlan(delegate, github, delegateToken, delegateModel)); err != nil {
				t.Fatal(err)
			}
			const workerNote, delegateNote = "PRIVATE_WORKER_RETRY_RESEARCH_NOT_SHARED", "PRIVATE_DELEGATE_REVIEW_NOT_SHARED"
			for _, note := range []struct {
				identity *worker
				text     string
			}{{l.worker, workerNote}, {delegate, delegateNote}} {
				l.cli(t, note.identity, []string{"knowledge", "put", "--input", "-"}, local.KnowledgeChange{Kind: "memory", Name: "private-scenario-note", Content: note.text, Selected: true})
				if err := os.WriteFile(filepath.Join(workerHome(note.identity), ".fixture-login-required"), []byte("fixture identity signed out"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			const description = "Implement deterministic bounded retry delay in the isolated Git repository modeling https://github.com/thtpark/openai-python. Clarify productive decisions with Founder before coding."
			const criteria = "Make a real code and regression-test diff on one PR. An independent delegate must review, request any in-scope correction, review the corrected head, and merge before task acceptance."
			var task domain.Task
			if err := json.Unmarshal(l.cli(t, l.founder, []string{"task", "create", "--input", "-"}, domain.TaskRequest{ProjectID: l.project.ID, Title: "Bound retry delay and independently review one PR", Description: description, AcceptanceCriteria: criteria}), &task); err != nil {
				t.Fatal(err)
			}
			task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "assign", Reason: "Founder assigns implementation to the non-delegate worker", AgentID: l.worker.agent.ID, Provider: providers.worker, Model: workerModel})
			task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "policy", Reason: "Founder selects independent review and delegate-authorized in-scope rework", Policy: &domain.ApprovalPolicy{RequiredActors: []string{l.founder.agent.ID, delegate.agent.ID}, IndependentReadiness: true, IndependentVerification: true}})
			task = l.change(t, l.worker, task.ID, domain.TaskChange{Kind: "workspace", Reason: "Bind the worker's private Git checkout", WorkspaceRef: l.binding.Ref})
			task = l.change(t, l.worker, task.ID, domain.TaskChange{Kind: "status", Reason: "Prepare and clarify; this is not execution approval", Status: domain.Todo})
			preparationSpec := task.SpecGeneration
			workerDaemon := l.startScenarioIdentity(t, l.worker)
			delegateDaemon := l.startScenarioIdentity(t, delegate)
			waitFor(t, 10*time.Second, func() bool {
				page := must[domain.Page[domain.Message]](t, "agent-cli", []string{"--config", l.founder.configFile, "message", "inbox"}, nil)
				seen := map[string]bool{}
				for _, message := range page.Items {
					if message.Kind == "authentication" && message.Origin == "service" && !message.ResponseRequest {
						seen[message.SenderID] = true
					}
				}
				return seen[l.worker.agent.ID] && seen[delegate.agent.ID]
			}, "both private identities requesting Founder-arranged mock provider sign-in")
			for _, identity := range []*worker{l.worker, delegate} {
				if len(scenarioTrace(t, identity).Calls) != 0 || len(scenarioJournals(t, identity)) != 0 {
					t.Fatal("provider inference began before mock sign-in")
				}
				fixtureSignIn(t, identity)
				health := must[provider.Health](t, "agent-cli", []string{"--config", identity.configFile, "provider", "status", "--provider", identity.config.Provider}, nil)
				if health.Authentication != "authenticated" && health.Authentication != "credentials-present-unverified" {
					t.Fatal("mock sign-in did not make the selected fixture provider ready")
				}
			}
			answered := map[string]bool{}
			answerIDs := []string{}
			for index, answer := range retryAnswers {
				waitFor(t, 15*time.Second, func() bool {
					current := l.task(t, task.ID)
					return current.Waiting != nil && len(current.Waiting.QuestionIDs) == 1 && !answered[current.Waiting.QuestionIDs[0]]
				}, fmt.Sprintf("productive clarification round %d", index+1))
				current := l.task(t, task.ID)
				questionID := current.Waiting.QuestionIDs[0]
				questionOK := false
				for _, entry := range scenarioDiscussion(t, l, task.ID) {
					if entry.ID == questionID && entry.AuthorID == l.worker.agent.ID && strings.Contains(entry.Body, retryQuestions[index]) && slices.Contains(entry.Mentions, l.founder.agent.ID) {
						questionOK = true
					}
				}
				if !questionOK || !slices.Equal(current.Waiting.RequestedIDs, []string{l.founder.agent.ID}) {
					t.Fatal("clarification was not authored by the assigned worker and routed to Founder")
				}
				before := len(scenarioTrace(t, l.worker).Calls)
				time.Sleep(1100 * time.Millisecond)
				if after := len(scenarioTrace(t, l.worker).Calls); after != before {
					t.Fatal("an unanswered question caused repeated inference")
				}
				assertNoScenarioCoding(t, github, l.worker, delegate)
				var entry domain.Entry
				if err := json.Unmarshal(l.cli(t, l.founder, []string{"comment", "add", task.ID, "--input", "-"}, domain.EntryRequest{ParentID: questionID, Body: answer}), &entry); err != nil {
					t.Fatal(err)
				}
				if entry.AuthorID != l.founder.agent.ID {
					t.Fatal("the actual clarification reply was not Founder's")
				}
				answerIDs = append(answerIDs, entry.ID)
				answered[questionID] = true
				waitFor(t, 12*time.Second, func() bool {
					current := l.task(t, task.ID)
					return strings.Contains(current.Description, answer) && strings.Contains(current.AcceptanceCriteria, answer)
				}, "actual Founder answer incorporated into specification and acceptance criteria")
				if current := l.task(t, task.ID); current.SpecGeneration != preparationSpec+int64(index+1) || current.Readiness != nil || current.ResultGeneration != 0 || current.ActiveRun != nil {
					t.Fatal("clarification changed unexpected generations or granted execution")
				}
			}
			waitFor(t, 15*time.Second, func() bool {
				current := l.task(t, task.ID)
				if current.Waiting != nil || current.WorkspaceRef != l.binding.Ref {
					return false
				}
				page := must[domain.Page[domain.Message]](t, "agent-cli", []string{"--config", l.founder.configFile, "message", "inbox"}, nil)
				for _, message := range page.Items {
					if strings.Contains(message.Body, task.ID) && strings.Contains(message.Body, fmt.Sprintf("specification %d", current.SpecGeneration)) && strings.Contains(message.Body, "No timed expiry") {
						return true
					}
				}
				return false
			}, "exact refined-specification signoff request, not an inferred approval")
			current := l.task(t, task.ID)
			wantDescription, wantCriteria := description, criteria
			for _, answer := range retryAnswers {
				wantDescription += "\nFounder decision: " + answer
				wantCriteria += "\nFounder acceptance: " + answer
			}
			if current.Description != wantDescription || current.AcceptanceCriteria != wantCriteria {
				t.Fatal("productive decisions were fabricated, omitted, or repeatedly appended")
			}
			assertNoScenarioCoding(t, github, l.worker, delegate)
			if files, err := os.ReadDir(l.workspace); err != nil || len(files) != 0 {
				t.Fatal("the code workspace changed before exact Founder approval", err)
			}
			envelope, code, _ := run(t, "agent-cli", []string{"--config", l.founder.configFile, "task", "change", task.ID, "--input", "-"}, domain.TaskChange{Kind: "approve", Reason: "stale specification must not authorize code", SpecGeneration: current.SpecGeneration - 1})
			if code == 0 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
				t.Fatal("stale Founder signoff was accepted")
			}
			approved := l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "approve", Reason: "Founder approves this exact refined specification and delegates only in-scope review rework", SpecGeneration: current.SpecGeneration})
			if approved.Readiness == nil || approved.Readiness.ActorID != l.founder.agent.ID || approved.Readiness.SpecGeneration != preparationSpec+4 {
				t.Fatal("initial signoff did not bind the actual Founder and exact refined generation")
			}
			final := waitScenarioDone(t, l, delegate, github, task.ID)
			waitFor(t, 10*time.Second, func() bool {
				for _, identity := range []*worker{l.worker, delegate} {
					status := must[struct {
						Work work.Status `json:"work"`
					}](t, "agent-cli", []string{"--config", identity.configFile, "work", "status"}, nil)
					if status.Work.State != "idle" || status.Work.PendingReports != 0 {
						return false
					}
				}
				return true
			}, "both native controllers idle with durable reports acknowledged")
			workerTrace, delegateTrace := scenarioTrace(t, l.worker), scenarioTrace(t, delegate)
			_, eventsBefore := github.Snapshot()
			time.Sleep(1200 * time.Millisecond)
			if len(scenarioTrace(t, l.worker).Calls) != len(workerTrace.Calls) || len(scenarioTrace(t, delegate).Calls) != len(delegateTrace.Calls) {
				t.Fatal("idle controllers repeated provider inference")
			}
			workerDaemon.stop(t)
			delegateDaemon.stop(t)
			workerDaemon = l.startScenarioIdentity(t, l.worker)
			delegateDaemon = l.startScenarioIdentity(t, delegate)
			time.Sleep(2200 * time.Millisecond)
			workerDaemon.stop(t)
			delegateDaemon.stop(t)
			if len(scenarioTrace(t, l.worker).Calls) != len(workerTrace.Calls) || len(scenarioTrace(t, delegate).Calls) != len(delegateTrace.Calls) {
				t.Fatal("restart replayed provider inference instead of durable receipts")
			}
			pr, eventsAfter := github.Snapshot()
			if len(eventsAfter) != len(eventsBefore) || pr.Number != 1 || !pr.Merged {
				t.Fatal("idle/restart duplicated PR, review, or merge side effects")
			}
			assertScenarioResults(t, l, delegate, github, approved, final, workerTrace, delegateTrace, answerIDs)
			tokens := []string{workerToken, delegateToken, l.worker.client.Credential.Token, delegate.client.Credential.Token, l.founder.client.Credential.Token}
			assertPrivateScenarioJournals(t, l.worker, workerNote, delegateNote, tokens)
			assertPrivateScenarioJournals(t, delegate, delegateNote, workerNote, tokens)
			assertSharedScenarioPrivacy(t, l, final, delegate, append(tokens, workerNote, delegateNote, l.root)...)
		})
	}
}

func waitScenarioDone(t *testing.T, l *lab, delegate *worker, github *scenario.GitHub, taskID string) domain.Task {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var task domain.Task
	for time.Now().Before(deadline) {
		task = l.task(t, taskID)
		if task.Status == domain.Done {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pr, _ := github.Snapshot(); pr.Merged {
		assertScenarioGitHub(t, github, l.worker, delegate)
		t.Log("actual same-PR head correction, ordered independent reviews, diff/content, and merge assertions passed")
	}
	for _, identity := range []*worker{l.worker, delegate} {
		for _, call := range scenarioTrace(t, identity).Calls {
			if !call.Completed {
				t.Logf("unfinished private fixture invocation: actor=%s phase=%s rule=%s action=%d kind=%s", identity.agent.Name, call.Phase, call.Rule, call.Action, call.ActionKind)
			}
			if call.Phase == "verification" && call.Task != nil {
				t.Logf("verification discussion fence: bound=%d current=%d completed=%t", call.Task.DiscussionGeneration, task.DiscussionGeneration, call.Completed)
			}
		}
		for _, journal := range scenarioJournals(t, identity) {
			if journal.Kind == "execution" || journal.Kind == "verification" {
				t.Logf("native run journal: actor=%s kind=%s state=%s problem=%s", identity.agent.Name, journal.Kind, journal.State, journal.Problem)
			}
		}
	}
	t.Fatalf("automatic independent delegate review/rework/merge did not finish: status=%s spec=%d result=%d worker executions=%d delegate verification invocations=%d; no manual verify was used", task.Status, task.SpecGeneration, task.ResultGeneration, len(callsFor(scenarioTrace(t, l.worker), "execution")), len(callsFor(scenarioTrace(t, delegate), "verification")))
	return task
}

func assertSharedScenarioPrivacy(t *testing.T, l *lab, task domain.Task, delegate *worker, forbidden ...string) {
	t.Helper()
	runs := l.cli(t, l.founder, []string{"task", "runs", task.ID}, nil)
	history := l.cli(t, l.founder, []string{"task", "history", task.ID}, nil)
	watch := l.cli(t, l.founder, []string{"watch", "--once", "--task", task.ID}, nil)
	shared, err := json.Marshal([]any{task, l.project, scenarioDiscussion(t, l, task.ID), runs, history, watch})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range append(forbidden, workerHome(l.worker), workerHome(delegate), l.workspace, delegate.config.StateDir) {
		if value != "" && strings.Contains(string(shared), value) {
			t.Fatal("private paths, notes, or secrets leaked into shared records")
		}
	}
	sessions, err := api.Call[[]domain.Session](context.Background(), l.founder.client, "GET", "/v1/sessions", nil)
	if err != nil || len(sessions) != 0 {
		t.Fatal("fixture workflow unexpectedly opened a protected remote session", err)
	}
}
