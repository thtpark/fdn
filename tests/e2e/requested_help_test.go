//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/work"
)

func TestNativeRequestedTaskEscalatesSignInBeforeOnboarding(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t, name)
			home := filepath.Join(filepath.Dir(l.worker.configFile), "home")
			if err := os.WriteFile(filepath.Join(home, ".fixture-login-required"), []byte("fixture signed out"), 0600); err != nil {
				t.Fatal(err)
			}
			project := must[domain.Project](t, "agent-cli", []string{"--config", l.founder.configFile, "project", "create", "--input", "-"}, domain.ProjectRequest{Name: "requested-help"})
			task := must[domain.Task](t, "agent-cli", []string{"--config", l.founder.configFile, "task", "create", "--input", "-"}, domain.TaskRequest{
				ProjectID: project.ID, Title: "Review requested work before sign-in", Description: "Ask meaningful questions before coding", AcceptanceCriteria: "Use the currently authorized workflow"})
			task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "assign", AgentID: l.worker.agent.ID, Reason: "Assign autonomous requested work"})
			task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "status", Status: domain.Todo, Reason: "Request task preparation"})
			inbox := func() []domain.Message {
				t.Helper()
				page, err := api.Call[domain.Page[domain.Message]](context.Background(), l.founder.client, "GET", "/v1/messages?limit=100", nil)
				if err != nil {
					t.Fatal(err)
				}
				return page.Items
			}
			agent := l.startAgent(t)
			waitFor(t, 10*time.Second, func() bool { return len(inbox()) != 0 }, "scoped help before onboarding")
			agent.stop(t)
			messages := inbox()
			if len(messages) != 1 || messages[0].Kind != "authentication" || messages[0].SenderID != l.worker.agent.ID || messages[0].ResponseRequest ||
				!strings.Contains(messages[0].Body, task.ID) || !strings.Contains(messages[0].Body, project.ID) || !strings.Contains(messages[0].Body, name) {
				t.Fatal("native task request was swallowed by computer-local onboarding", messages)
			}
			status := must[struct {
				Work work.Status `json:"work"`
			}](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "status"}, nil)
			if status.Work.InitialReviewDone || len(status.Work.AuthenticationRequests) != 1 || status.Work.AuthenticationRequests[0].TaskID != task.ID ||
				status.Work.AuthenticationRequests[0].ActionKey == "" || status.Work.AuthenticationRequests[0].ExpiresAt-status.Work.AuthenticationRequests[0].CreatedAt != 900 {
				t.Fatal("native task-scoped authentication status is incomplete", status)
			}
			restarted := l.startAgent(t)
			time.Sleep(1200 * time.Millisecond)
			restarted.stop(t)
			if len(inbox()) != 1 {
				t.Fatal("native restart repeated requested-work help")
			}
			if _, err := os.Stat(filepath.Join(l.worker.config.StateDir, "invocations")); !os.IsNotExist(err) {
				t.Fatal("native blocked task started provider inference")
			}
		})
	}
}
