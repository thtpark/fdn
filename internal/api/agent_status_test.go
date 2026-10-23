package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func statusAPIReceipt(t *testing.T, envelope Envelope, code int) domain.AgentStatusReceipt {
	t.Helper()
	if code != http.StatusOK || !envelope.OK || envelope.Error != nil {
		t.Fatal("unexpected status API response", code, envelope)
	}
	var receipt domain.AgentStatusReceipt
	if err := json.Unmarshal(envelope.Data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestAgentStatusAPISelfPublicationVisibleToAllActiveViewersAndExactReplay(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	path := "/v1/agents/" + f.target.ID + "/status"
	req := map[string]any{"operation_id": "intentional-public-status", "text": "  Reviewing the approved change  "}
	envelope, code := f.request(t, f.target, http.MethodPost, path, req)
	first := statusAPIReceipt(t, envelope, code)
	if envelope.OperationID != req["operation_id"] || first.AgentID != f.target.ID || first.Status == nil || first.Status.Text != "Reviewing the approved change" || first.Status.Source != "agent" || first.Status.State != "unknown" || !first.Status.Stale || first.Pending || first.NextUpdateAt != 0 {
		t.Fatal("self publication contract failed", envelope, first)
	}
	for _, viewer := range []domain.Agent{f.founder, f.delegate, f.target} {
		envelope, code = f.request(t, viewer, http.MethodGet, "/v1/agents/"+f.target.ID, nil)
		var agent domain.Agent
		if err := json.Unmarshal(envelope.Data, &agent); code != http.StatusOK || err != nil || !reflect.DeepEqual(agent.Status, first.Status) {
			t.Fatal("public agent projection missing", agent, envelope, err)
		}
		envelope, code = f.request(t, viewer, http.MethodGet, "/v1/agents", nil)
		var agents []domain.Agent
		if err := json.Unmarshal(envelope.Data, &agents); code != http.StatusOK || err != nil {
			t.Fatal(code, envelope, err)
		}
		found := false
		for _, a := range agents {
			if a.ID == f.target.ID {
				found = reflect.DeepEqual(a.Status, first.Status)
			}
		}
		if !found {
			t.Fatal("public roster did not expose deliberate status", agents)
		}
	}
	envelope, code = f.request(t, f.target, http.MethodGet, "/v1/me", nil)
	var me domain.Agent
	if err := json.Unmarshal(envelope.Data, &me); code != http.StatusOK || err != nil || !reflect.DeepEqual(me.Status, first.Status) || me.Revision != f.target.Revision || me.LastSeen != f.target.LastSeen || me.Mode != f.target.Mode {
		t.Fatal("status changed self authority or heartbeat", me, envelope, err)
	}
	envelope, code = f.request(t, f.delegate, http.MethodGet, "/v1/agents/"+f.target.ID+"/diagnostics", nil)
	if code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("public status broadened private diagnostics", envelope, code)
	}
	for _, viewer := range []domain.Agent{f.founder, f.target} {
		envelope, code = f.request(t, viewer, http.MethodGet, "/v1/agents/"+f.target.ID+"/diagnostics", nil)
		var diagnosis domain.AgentDiagnostics
		if err := json.Unmarshal(envelope.Data, &diagnosis); code != http.StatusOK || err != nil || !reflect.DeepEqual(diagnosis.Status, first.Status) {
			t.Fatal("authorized diagnostics lost status projection", diagnosis, code, err)
		}
	}
	for _, actor := range []domain.Agent{f.founder, f.delegate} {
		envelope, code = f.request(t, actor, http.MethodPost, path, map[string]any{"operation_id": domain.NewID("op"), "text": "Cannot publish another identity"})
		if code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("Founder or delegation overrode self-only publication", code, envelope)
		}
	}
	envelope, code = f.request(t, f.target, http.MethodPost, path, map[string]any{"operation_id": "latest-pending-status", "text": "Unpublished future note"})
	pending := statusAPIReceipt(t, envelope, code)
	if !pending.Pending || pending.NextUpdateAt != first.Status.UpdatedAt+domain.AgentStatusInterval || !reflect.DeepEqual(pending.Status, first.Status) {
		t.Fatal("rapid authored text was not coalesced", pending)
	}
	envelope, code = f.request(t, f.delegate, http.MethodGet, "/v1/agents", nil)
	if code != http.StatusOK || strings.Contains(string(envelope.Data), "Unpublished future note") || strings.Contains(string(envelope.Data), "context_key") || strings.Contains(string(envelope.Data), "next_update_at") {
		t.Fatal("public roster exposed unpublished or service-owned status state", code, string(envelope.Data))
	}
	envelope, code = f.request(t, f.target, http.MethodPost, path, req)
	if replayed := statusAPIReceipt(t, envelope, code); !reflect.DeepEqual(replayed, first) {
		t.Fatal("exact API replay returned mutable status or refreshed its clock", first, replayed)
	}
	changed := map[string]any{"operation_id": req["operation_id"], "text": "Reviewing the approved change"}
	envelope, code = f.request(t, f.target, http.MethodPost, path, changed)
	if code != http.StatusConflict || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
		t.Fatal("normalizing text weakened exact request fingerprints", code, envelope)
	}
	if _, err := f.store.Revoke(context.Background(), f.founder.ID, f.target.ID, domain.AgentRequest{Command: domain.Command{OperationID: "revoke-status-writer"}}); err != nil {
		t.Fatal(err)
	}
	envelope, code = f.request(t, f.target, http.MethodPost, path, req)
	if code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("revoked identity replayed an accepted status operation", code, envelope)
	}
}

func TestAgentStatusAPIRejectsMalformedProtectedAndCredentialInput(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	path := "/v1/agents/" + f.target.ID + "/status"
	for _, protected := range []string{"source", "priority", "state", "updated_at", "observed_at", "stale", "actor", "actor_id", "agent_id", "origin", "reason", "status", "pending", "next_update_at", "command"} {
		t.Run(protected, func(t *testing.T) {
			envelope, code := f.request(t, f.target, http.MethodPost, path, map[string]any{"operation_id": domain.NewID("op"), "text": "Testing the change", protected: "forged"})
			if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid || len(envelope.Data) != 0 {
				t.Fatal("protected status field accepted", protected, code, envelope)
			}
		})
	}
	for _, payload := range []map[string]any{
		{"operation_id": "missing-text"},
		{"operation_id": "null-text", "text": nil},
		{"operation_id": "numeric-text", "text": 42},
		{"operation_id": "revision", "text": "Testing", "expected_revision": 1},
		{"operation_id": "negative-revision", "text": "Testing", "expected_revision": -1},
		{"operation_id": "multiline", "text": "Testing\nprivate output"},
		{"operation_id": "hidden-text", "text": "Testing\u200boutput"},
		{"operation_id": "oversized-text", "text": strings.Repeat("é", 161)},
		{"operation_id": "credential-form", "text": "token=fixture-not-a-real-credential"},
	} {
		envelope, code := f.request(t, f.target, http.MethodPost, path, payload)
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid || len(envelope.Data) != 0 {
			t.Fatal("invalid status shape accepted", code, envelope)
		}
	}
	envelope, code := f.request(t, f.target, http.MethodPost, path, map[string]any{"operation_id": "explicit-clear", "text": "", "expected_revision": 0})
	clear := statusAPIReceipt(t, envelope, code)
	if clear.Pending || clear.Status == nil || clear.Status.Source != "controller" || clear.Status.State != "unknown" || !clear.Status.Stale {
		t.Fatal("empty required text did not safely restore unknown automatic status", clear)
	}
}

func TestAgentStatusAPIHealthUsesSafeSummaryAndServerObservationTime(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	h := apiHealthObservation()
	h.ObservedAt += 6 * 3600
	h.PhaseStartedAt = h.ObservedAt
	h.Phase, h.ActionKind, h.ActionID, h.JournalID = "invoking", "onboarding", "private_status_action", "private_status_journal"
	h.Mode = "manual"
	before := time.Now().Unix()
	envelope, code := f.request(t, f.target, http.MethodPost, "/v1/heartbeat", domain.HeartbeatRequest{Health: &h})
	var a domain.Agent
	if err := json.Unmarshal(envelope.Data, &a); code != http.StatusOK || err != nil || a.Status == nil || a.Status.Source != "controller" || a.Status.State != "working" || a.Status.Text != "Reviewing computer capabilities" || a.Status.UpdatedAt < before || a.Status.UpdatedAt > time.Now().Unix() || a.Status.ObservedAt != a.Status.UpdatedAt || a.Status.Stale {
		t.Fatal("health status trusted target mode/time or lacked a coarse legacy summary", a, envelope, err)
	}
	envelope, code = f.request(t, f.delegate, http.MethodGet, "/v1/agents", nil)
	for _, private := range []string{h.ActionID, h.JournalID, "context_key", "health_hash", "generation"} {
		if code != http.StatusOK || strings.Contains(string(envelope.Data), private) {
			t.Fatal("public automatic text exposed private context", private, code, string(envelope.Data))
		}
	}
	h.Sequence++
	h.Phase, h.Activity, h.Blocker = "blocked", "approval", "approval-required"
	envelope, code = f.request(t, f.target, http.MethodPost, "/v1/heartbeat", domain.HeartbeatRequest{Health: &h})
	if err := json.Unmarshal(envelope.Data, &a); code != http.StatusOK || err != nil || a.Status == nil || a.Status.State != "blocked" || a.Status.Text != "Waiting for task approval" {
		t.Fatal("meaningful automatic blocker did not publish immediately", a, envelope, err)
	}
}
