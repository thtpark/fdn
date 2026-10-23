package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentStatusCLIOnlyPublishesOwnIntentionalNonSecretText(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if r.Method != http.MethodPost || len(parts) != 5 || parts[2] != "agents" || parts[4] != "status" {
			t.Error("unexpected status publication route", r.Method, r.URL.Path)
		}
		var req domain.AgentStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == nil || *req.Text != "Running parser tests" || req.ExpectedRevision != 0 {
			t.Error("unexpected public status body", req, err)
		}
		diagnosticsEnvelope(t, w, domain.AgentStatusReceipt{AgentID: parts[3], Status: &domain.AgentTextStatus{Text: *req.Text, Source: "agent", State: "working", UpdatedAt: 1800000000, ObservedAt: 1800000000}}, nil)
	})
	input := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(input, []byte(`{"text":"Running parser tests"}`), 0600); err != nil {
		t.Fatal(err)
	}
	envelope, code := invoke(t, "agent", []string{"--config", file, "--operation-id", "self-status-1", "agent", "status", "--input", input}, nil)
	var receipt domain.AgentStatusReceipt
	if err := json.Unmarshal(envelope.Data, &receipt); code != 0 || !envelope.OK || err != nil || receipt.Status == nil || receipt.Status.Text != "Running parser tests" {
		t.Fatal(envelope, code, receipt, err)
	}
	calls := requests()
	if len(calls) != 2 || calls[0].method != http.MethodGet || calls[0].path != "/v1/me" || calls[1].method != http.MethodPost || !strings.HasSuffix(calls[1].path, "/status") {
		t.Fatal("status used extra inference or impersonation paths", calls)
	}
}

func TestAgentStatusCLIDeniesTargetOrPriorityOverride(t *testing.T) {
	for _, body := range []string{`{"text":"working","target_id":"agent_other"}`, `{"text":"working","priority":"urgent"}`, `{"text":"working","updated_at":1800000000}`, `{"text":"working","source":"controller"}`} {
		_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("protected status input reached service", r.Method, r.URL.Path)
			diagnosticsEnvelope(t, w, nil, nil)
		})
		input := filepath.Join(t.TempDir(), "status.json")
		if err := os.WriteFile(input, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if envelope, code := invoke(t, "agent", []string{"--config", file, "agent", "status", "--input", input}, nil); code != 2 || envelope.OK {
			t.Fatal("protected status input accepted", body, envelope, code)
		}
		for _, request := range requests() {
			if request.method != http.MethodGet {
				t.Fatal("invalid status changed state", request)
			}
		}
	}
}
