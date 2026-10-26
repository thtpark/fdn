//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestNativeProviderFailureAndMidRunQuestions(t *testing.T) {
	for _, title := range []string{"fail", "pause"} {
		t.Run(title, func(t *testing.T) {
			l := newLab(t, "copilot")
			task := l.approved(t, title)
			for range 2 {
				must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			}
			envelope, code, diagnostic := run(t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			current := l.task(t, task.ID)
			if current.Status != domain.Paused || current.ActiveRun != nil || current.Result != nil {
				t.Fatal("partial/failed work became completed")
			}
			if strings.Contains(diagnostic, "SECRET_PROVIDER_DIAGNOSTIC_MUST_STAY_PRIVATE") || strings.Contains(string(envelope.Data), "SECRET_PROVIDER_DIAGNOSTIC_MUST_STAY_PRIVATE") {
				t.Fatal("private provider diagnostics leaked")
			}
			if title == "fail" {
				if code != 8 || envelope.OK || !current.UnsafeRetry || current.Failures != 1 {
					t.Fatal("failure did not require exact unsafe retry", code)
				}
				state, err := local.Open(context.Background(), l.worker.config.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				journals, err := state.Journals(context.Background(), false)
				if err != nil {
					t.Fatal(err)
				}
				state.Close()
				found := false
				for _, journal := range journals {
					if journal.TaskID == task.ID && journal.Kind == "execution" {
						log, err := os.ReadFile(filepath.Join(l.worker.config.StateDir, "invocations", journal.ID, "stderr.log"))
						if err != nil {
							t.Fatal(err)
						}
						found = strings.Contains(string(log), "SECRET_PROVIDER_DIAGNOSTIC_MUST_STAY_PRIVATE")
					}
				}
				if !found {
					t.Fatal("private failure evidence was not retained")
				}
				time.Sleep(1100 * time.Millisecond)
				must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
				count, err := os.ReadFile(filepath.Join(l.workspace, "invocations.count"))
				if err != nil || strings.Count(string(count), "\n") != 1 {
					t.Fatal("unsafe retry occurred without approval", err)
				}
			} else {
				if code != 0 || !envelope.OK || current.Waiting == nil || current.Waiting.Kind != "clarification" || len(current.Waiting.QuestionIDs) != 1 || current.Failures != 0 {
					t.Fatal("questions did not override completed reply", code)
				}
				next := l.approved(t, "success")
				for range 2 {
					must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
				}
				if l.task(t, next.ID).Status != domain.Completed {
					t.Fatal("paused task starved another authorized task")
				}
			}
		})
	}
}

func TestNativeMultiRoundClarificationAndCurrentSignoff(t *testing.T) {
	l := newLab(t, "codex")
	raw := l.cli(t, l.worker, []string{"task", "create", "--input", "-"}, domain.TaskRequest{ProjectID: l.project.ID, Title: "clarify", Description: "Specify the fixture result before coding", AcceptanceCriteria: "Verify the agreed result"})
	var task domain.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "assign", Reason: "assigned preparation, not execution", AgentID: l.worker.agent.ID, Provider: "codex"})
	task = l.change(t, l.worker, task.ID, domain.TaskChange{Kind: "status", Reason: "begin clarification", Status: domain.Todo})
	agent := l.startAgent(t)
	waitFor(t, 15*time.Second, func() bool { return l.task(t, task.ID).Waiting != nil }, "blocking clarification before workspace/readiness")
	current := l.task(t, task.ID)
	if current.WorkspaceRef != "" || current.Readiness != nil || current.ActiveRun != nil {
		t.Fatal("clarification incorrectly required or granted execution prerequisites")
	}
	question := current.Waiting.QuestionIDs[0]
	time.Sleep(1200 * time.Millisecond)
	current = l.task(t, task.ID)
	if len(current.Waiting.QuestionIDs) != 1 || current.Waiting.QuestionIDs[0] != question {
		t.Fatal("unanswered question repeated")
	}
	l.cli(t, l.founder, []string{"comment", "add", task.ID, "--input", "-"}, domain.EntryRequest{ParentID: question, Body: "Use the exact verified fixture result and check its file bytes."})
	waitFor(t, 20*time.Second, func() bool {
		current := l.task(t, task.ID)
		return current.WorkspaceRef != "" && current.Waiting == nil && strings.Contains(current.Description, "Decision incorporated from actual requested replies.")
	}, "actual-answer refinement, separate acceptance review and owned workspace preparation")
	current = l.task(t, task.ID)
	if current.Readiness != nil || current.ActiveRun != nil || current.Result != nil {
		t.Fatal("preparation granted its own approval")
	}
	waitFor(t, 10*time.Second, func() bool {
		inbox := must[domain.Page[domain.Message]](t, "agent-cli", []string{"--config", l.founder.configFile, "message", "inbox"}, nil)
		for _, message := range inbox.Items {
			if strings.Contains(message.Body, task.ID) && strings.Contains(message.Body, "No timed expiry") {
				return true
			}
		}
		return false
	}, "exact-specification signoff notification after workspace preparation")
	current = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "approve", Reason: "reviewed refined current specification", SpecGeneration: current.SpecGeneration})
	waitFor(t, 15*time.Second, func() bool { return l.task(t, task.ID).Status == domain.Completed }, "approved refined task execution")
	agent.stop(t)
	state, err := local.Open(context.Background(), l.worker.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	binding, err := state.Workspace(context.Background(), current.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(binding.Path, "result.txt"))
	if err != nil || string(bytes) != "verified fixture result\n" {
		t.Fatal("generated owned workspace was not used", err)
	}
	journals, err := state.Journals(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	scope, acceptance := false, false
	for _, journal := range journals {
		if journal.TaskID == task.ID {
			scope = scope || journal.Kind == "scope"
			acceptance = acceptance || journal.Kind == "acceptance"
		}
	}
	if !scope || !acceptance {
		t.Fatal("unapproved task skipped distinct scope/acceptance reviews")
	}
}
