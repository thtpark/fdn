package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func apiHealthObservation() domain.WorkerHealth {
	now := time.Now().Unix()
	return domain.WorkerHealth{Version: domain.WorkerHealthVersion, Sequence: 1, ObservedAt: now,
		Mode: "ai", Provider: "codex", PreferredProvider: "copilot", ProviderVersion: "1.2.3", Model: "fixture-model",
		Authentication: "authenticated", Phase: "idle", PhaseStartedAt: now}
}

func TestHealthAPIHeartbeatCompatibilityValidationAndPrivacy(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx := context.Background()
	for _, request := range []any{domain.Command{OperationID: "legacy-heartbeat"}, map[string]any{}} {
		envelope, status := f.request(t, f.target, http.MethodPost, "/v1/heartbeat", request)
		var agent domain.Agent
		if err := json.Unmarshal(envelope.Data, &agent); status != http.StatusOK || err != nil || agent.ID != f.target.ID || agent.LastSeen == 0 || agent.Revision != f.target.Revision {
			t.Fatal("old command-only heartbeat contract changed", agent, envelope, err)
		}
	}
	private, err := f.store.SendMessage(ctx, f.delegate.ID, domain.MessageRequest{Command: domain.Command{OperationID: "private-health-message"}, RecipientID: f.target.ID, Body: "Private fixture request", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	h := apiHealthObservation()
	h.ActionKind, h.ActionID, h.LastActionKind, h.LastActionID = "message", private.ID, "message", private.ID
	h.JournalID, h.Phase, h.DeadlineAt = "journal_private_fixture", "invoking", h.ObservedAt+60
	request := domain.HeartbeatRequest{Command: domain.Command{OperationID: "health-heartbeat"}, Health: &h}
	envelope, status := f.request(t, f.target, http.MethodPost, "/v1/heartbeat", request)
	if status != http.StatusOK || !envelope.OK || envelope.OperationID != request.OperationID {
		t.Fatal("health heartbeat route failed", status, envelope)
	}
	envelope, status = f.request(t, f.founder, http.MethodGet, "/v1/agents/"+f.target.ID+"/diagnostics", nil)
	var diagnostics domain.AgentDiagnostics
	if err := json.Unmarshal(envelope.Data, &diagnostics); status != http.StatusOK || err != nil || diagnostics.Health == nil || diagnostics.Health.ActionID != "" || diagnostics.Health.LastActionID != "" || diagnostics.Health.JournalID != "" || diagnostics.State != "working" {
		t.Fatal("Founder diagnosis leaked private references or ignored live work", diagnostics, envelope, err)
	}
	if diagnostics.Health.PreferredProvider != "copilot" || diagnostics.Health.Provider != "codex" || diagnostics.Health.Model != "fixture-model" {
		t.Fatal("effective and preferred provider observations were conflated", diagnostics)
	}
	for _, path := range []string{"/v1/agents/" + f.target.ID + "/diagnostics", "/v1/messages/" + private.ID + "/diagnostics"} {
		actor := f.delegate
		if strings.Contains(path, "/messages/") {
			actor = f.founder
		}
		envelope, status := f.request(t, actor, http.MethodGet, path, nil)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied || len(envelope.Data) != 0 {
			t.Fatal("delegation or Founder role bypassed diagnosis privacy", status, envelope)
		}
	}
	envelope, status = f.request(t, f.delegate, http.MethodGet, "/v1/messages/"+private.ID+"/diagnostics", nil)
	var message domain.MessageDiagnostics
	if err := json.Unmarshal(envelope.Data, &message); status != http.StatusOK || err != nil || message.State != "generating" || message.Stage != "invoking" || strings.Contains(string(envelope.Data), h.JournalID) {
		t.Fatal("message party diagnosis failed or exposed recipient journals", message, envelope, err)
	}
	for _, bad := range []map[string]any{
		{"operation_id": "forged-reporter", "agent_id": f.delegate.ID, "health": h},
		{"operation_id": "raw-health", "health": map[string]any{"raw_error": "private output"}},
		{"operation_id": "forged-alert", "recipient_id": f.founder.ID, "body": "Forged alert", "origin": "service", "kind": "health"},
	} {
		path := "/v1/heartbeat"
		if bad["kind"] != nil {
			path = "/v1/messages"
		}
		envelope, status := f.request(t, f.target, http.MethodPost, path, bad)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("protected or unbounded health input was accepted", status, envelope)
		}
	}
	bad := h
	bad.Sequence = 0
	bad.Phase = "unknown"
	bad.ActionID, bad.ActionKind, bad.LastActionID, bad.LastActionKind, bad.JournalID, bad.DeadlineAt = "", "", "", "", "", 0
	envelope, status = f.request(t, f.target, http.MethodPost, "/v1/heartbeat", domain.HeartbeatRequest{Health: &bad})
	if status != http.StatusConflict || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
		t.Fatal("out-of-order health overwrote the current report", status, envelope)
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agents/"+f.target.ID+"/diagnostics", nil)
	if err := json.Unmarshal(envelope.Data, &diagnostics); status != http.StatusOK || err != nil || diagnostics.Health.Sequence != 1 || diagnostics.Health.ActionID != private.ID {
		t.Fatal("invalid reports lost the last valid report", diagnostics, err)
	}
}

func TestHealthAPIMonitorIndependentManualFounderAndCloseJoins(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	if f.founder.Mode != "manual" {
		t.Fatal("fixture must exercise a manual Founder")
	}
	f.server.health.close()
	h := apiHealthObservation()
	h.Phase, h.ActionKind, h.ActionID = "invoking", "onboarding", "onboarding"
	h.PhaseStartedAt, h.DeadlineAt = h.ObservedAt-300, h.ObservedAt-40
	envelope, status := f.request(t, f.target, http.MethodPost, "/v1/heartbeat", domain.HeartbeatRequest{Health: &h})
	if status != http.StatusOK {
		t.Fatal(status, envelope)
	}
	f.server.health = f.server.startHealthMonitor(5*time.Millisecond, time.Second)
	deadline := time.Now().Add(2 * time.Second)
	var notices []domain.Message
	for time.Now().Before(deadline) {
		page, err := f.store.Inbox(context.Background(), f.founder.ID, "", 200, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if m.Kind == "health" && m.SenderID == f.target.ID {
				notices = append(notices, m)
			}
		}
		if len(notices) != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(notices) != 1 || notices[0].ResponseRequest || notices[0].ExpiresAt != 0 || notices[0].Origin != "service" {
		t.Fatal("independent monitor required Founder AI or a target inference step", notices)
	}
	if err := f.server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.server.health.done:
	default:
		t.Fatal("Server.Close returned with its health monitor still running")
	}
	h.Sequence++
	h.Blocker, h.BlockedSinceAt = "invocation-uncertain", h.ObservedAt
	if _, err := f.store.HeartbeatWithHealth(context.Background(), f.target.ID, &h); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	page, err := f.store.Inbox(context.Background(), f.founder.ID, "", 200, false)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("closed API monitor continued mutating the store", page, err)
	}
}
