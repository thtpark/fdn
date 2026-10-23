//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

func waitPublicStatus(t *testing.T, client *api.Client, id, text string) domain.AgentTextStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		agent, err := api.Call[domain.Agent](context.Background(), client, "GET", api.Path("agents", id), nil)
		if err != nil {
			t.Fatal(err)
		}
		if agent.Status != nil && agent.Status.Text == text {
			return *agent.Status
		}
		if time.Now().After(deadline) {
			t.Fatal("public status did not converge", text, agent.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPublicAgentActivityIsReadableThrottledAndPrivateByConstruction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	release := make(chan struct{})
	x := &executor{block: release, entered: make(chan struct{})}
	e := newEngine(t, h, x)
	skipHealthOnboarding(t, e)
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Body: "PRIVATE_MESSAGE_BODY_NEVER_IN_PUBLIC_STATUS", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	startOperationWorker(t, e)
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture reply never started")
	}
	status := waitPublicStatus(t, h.other, h.workerAgent.ID, "Replying to a message")
	if status.State != "working" || status.Source != "controller" || status.Stale || status.UpdatedAt <= 0 || status.ObservedAt <= 0 {
		t.Fatal("public activity lacks provenance/freshness", status)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), message.Body) || strings.Contains(string(data), message.ID) {
		t.Fatal("private message context leaked into team status", string(data))
	}
	text := "Running parser tests"
	receipt, err := api.Call[domain.AgentStatusReceipt](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "status"), domain.AgentStatusRequest{Command: command(), Text: &text})
	if err != nil || !receipt.Pending || receipt.NextUpdateAt < status.UpdatedAt+domain.AgentStatusInterval {
		t.Fatal("normal authored update bypassed60-second interval", receipt, err)
	}
	text = "Checking the latest parser result"
	if _, err := api.Call[domain.AgentStatusReceipt](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "status"), domain.AgentStatusRequest{Command: command(), Text: &text}); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		m, err := api.Call[domain.Message](ctx, h.worker, "GET", api.Path("messages", message.ID), nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.State == "handled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture reply did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := e.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: "session_fixture_status"}); err != nil {
		t.Fatal(err)
	}
	blocked := waitPublicStatus(t, h.other, h.workerAgent.ID, "Waiting for session recovery")
	if blocked.State != "blocked" || blocked.Source != "controller" || blocked.UpdatedAt-status.UpdatedAt >= domain.AgentStatusInterval {
		t.Fatal("important blocker waited for ordinary publication or preserved stale note", blocked)
	}
	if err := e.State.Put(ctx, "session-fence", remote.Fence{}); err != nil {
		t.Fatal(err)
	}
	recovered := waitPublicStatus(t, h.other, h.workerAgent.ID, "Idle; no eligible work")
	if recovered.State != "idle" || recovered.Source != "controller" || recovered.Stale {
		t.Fatal("observed recovery was not reported", recovered)
	}
	if x.count("message") != 1 {
		t.Fatal("status reporting started/repeated model calls", x.count("message"))
	}
}

func TestAgentStatusRawAPICannotSpoofOtherAgentOrAuthority(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	text := "Reviewing public project requirements"
	path := api.Path("agents", h.workerAgent.ID, "status")
	for _, client := range []*api.Client{h.founder, h.other} {
		if _, err := api.Call[domain.AgentStatusReceipt](ctx, client, "POST", path, domain.AgentStatusRequest{Command: command(), Text: &text}); err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("another actor forged status", err)
		}
	}
	for _, field := range []string{"priority", "source", "state", "updated_at", "observed_at", "mode", "agent_id"} {
		if _, err := api.Call[domain.AgentStatusReceipt](ctx, h.worker, "POST", path, map[string]any{"operation_id": command().OperationID, "text": text, field: "forged"}); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("protected status input accepted", field, err)
		}
	}
	for _, invalid := range []string{"private\nmultiline", strings.Repeat("x", domain.AgentStatusMaxRunes+1), "value\x00control"} {
		if _, err := api.Call[domain.AgentStatusReceipt](ctx, h.worker, "POST", path, domain.AgentStatusRequest{Command: command(), Text: &invalid}); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("unbounded/multiline public status accepted", err)
		}
	}
	written, err := api.Call[domain.AgentStatusReceipt](ctx, h.worker, "POST", path, domain.AgentStatusRequest{Command: command(), Text: &text})
	if err != nil || written.Status == nil {
		t.Fatal(written, err)
	}
	visible, err := api.Call[domain.Agent](ctx, h.other, "GET", api.Path("agents", h.workerAgent.ID), nil)
	if err != nil || visible.Status == nil || visible.Status.Text != text || visible.Mode != "ai" {
		t.Fatal("team cannot see intentional public status or mode changed", visible, err)
	}
}
