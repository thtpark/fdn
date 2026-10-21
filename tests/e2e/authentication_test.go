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
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/work"
)

func TestNativeDaemonRequestsSignInBeforeOnboardingWithoutInference(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t, name)
			home := filepath.Join(filepath.Dir(l.worker.configFile), "home")
			if err := os.WriteFile(filepath.Join(home, ".fixture-login-required"), []byte("fixture signed out"), 0600); err != nil {
				t.Fatal(err)
			}
			agent := l.startAgent(t)
			inbox := func() []domain.Message {
				t.Helper()
				page, err := api.Call[domain.Page[domain.Message]](context.Background(), l.founder.client, "GET", "/v1/messages?limit=200", nil)
				if err != nil {
					t.Fatal(err)
				}
				return page.Items
			}
			waitFor(t, 10*time.Second, func() bool { return len(inbox()) != 0 }, "sign-in notice in manual Founder's inbox")
			messages := inbox()
			if len(messages) != 1 || messages[0].Kind != "authentication" || messages[0].Origin != "service" ||
				messages[0].ResponseRequest || messages[0].SenderID != l.worker.agent.ID ||
				!strings.Contains(messages[0].Body, "assistance open GRANT --provider "+name) {
				t.Fatal("native missing-login request is not protected/actionable", messages)
			}
			status := must[struct {
				Work work.Status `json:"work"`
			}](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "status"}, nil)
			if len(status.Work.AuthenticationRequests) != 1 || status.Work.AuthenticationRequests[0].State != "requested" {
				t.Fatal("native work status did not expose confirmed handoff", status)
			}
			time.Sleep(1200 * time.Millisecond)
			agent.stop(t)
			restarted := l.startAgent(t)
			time.Sleep(1200 * time.Millisecond)
			restarted.stop(t)
			messages = inbox()
			if len(messages) != 1 || messages[0].ID != status.Work.AuthenticationRequests[0].Deliveries[0].MessageID {
				t.Fatal("poll/restart duplicated a provider-authentication request", messages)
			}
			state, err := local.Open(context.Background(), l.worker.config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			journals, err := state.Journals(context.Background(), false)
			state.Close()
			if err != nil || len(journals) != 0 {
				t.Fatal("native handoff launched provider inference", journals, err)
			}
			if _, err := os.Stat(filepath.Join(l.worker.config.StateDir, "invocations")); !os.IsNotExist(err) {
				t.Fatal("native missing-login path started an invocation")
			}
			if task := l.cli(t, l.worker, []string{"mode", "show"}, nil); !strings.Contains(string(task), `"ai"`) {
				t.Fatal("handoff changed the target's mode")
			}
			sessions, err := api.Call[[]domain.Session](context.Background(), l.worker.client, "GET", "/v1/sessions", nil)
			if err != nil || len(sessions) != 0 {
				t.Fatal("native handoff opened/consented to a session", sessions, err)
			}
		})
	}
}
