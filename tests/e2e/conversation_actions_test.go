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
)

func TestNativeConversationActionCreatesAdhocUsesSkillAndReportsCheckedResult(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t, name)
			skills := filepath.Join(l.workspace, ".agents", "skills", "fixture-action")
			if err := os.MkdirAll(skills, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(skills, "SKILL.md"), []byte("---\nname: fixture-action\ndescription: Check fixture results\n---\nVerify result.txt before reporting.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			root := must[domain.Message](t, "agent-cli", []string{"--config", l.founder.configFile, "message", "send", "--input", "-"},
				map[string]any{"recipient_id": l.worker.agent.ID, "body": "Earlier relevant thread context", "response_requested": false})
			source := must[domain.Message](t, "agent-cli", []string{"--config", l.founder.configFile, "message", "send", "--input", "-"},
				map[string]any{"recipient_id": l.worker.agent.ID, "body": "fixture-conversation-action " + l.binding.Ref, "reply_to_id": root.ID})
			if !source.ResponseRequest || source.ThreadID != root.ID {
				t.Fatal("compiled CLI lost default response or later-thread reference", source)
			}
			agent := l.startAgent(t)
			defer agent.stop(t)
			waitFor(t, 15*time.Second, func() bool {
				current, err := api.Call[domain.Message](context.Background(), l.founder.client, "GET", api.Path("messages", source.ID), nil)
				return err == nil && current.State == "handled"
			}, "actual recorded conversation action and response")
			result, err := os.ReadFile(filepath.Join(l.workspace, "result.txt"))
			if err != nil || string(result) != "verified fixture result\n" {
				t.Fatal("compiled provider did not perform/check its actual authorized action", string(result), err)
			}
			count, err := os.ReadFile(filepath.Join(l.workspace, "invocations.count"))
			if err != nil || strings.Count(string(count), "\n") != 1 {
				t.Fatal("conversation action invoked tools more than once", string(count), err)
			}
			tasks, err := api.Call[domain.Page[domain.Task]](context.Background(), l.worker.client, "GET", "/v1/tasks?project="+domain.AdhocProjectID, nil)
			if err != nil || len(tasks.Items) != 1 || tasks.Items[0].Status != domain.Completed ||
				tasks.Items[0].AssigneeID != l.worker.agent.ID || tasks.Items[0].Readiness == nil ||
				tasks.Items[0].Readiness.ActorID != l.worker.agent.ID {
				t.Fatal("compiled controller did not create/admit its own Adhoc record", tasks, err)
			}
			thread, err := api.Call[[]domain.Message](context.Background(), l.founder.client, "GET", api.Path("messages", source.ID, "thread"), nil)
			if err != nil || len(thread) != 3 {
				t.Fatal("result was not delivered to the existing thread", thread, err)
			}
			found := false
			for _, message := range thread {
				if message.ReplyToID == source.ID && message.Origin == "ai" && message.ResponseRequest &&
					strings.Contains(message.Body, "fixture-action skill read") && strings.Contains(message.Body, tasks.Items[0].ID) {
					found = true
				}
			}
			if !found {
				t.Fatal("actual AI reply omitted skill/check/task evidence or continuation", thread)
			}
		})
	}
}
