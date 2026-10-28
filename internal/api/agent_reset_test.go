package api

import (
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentResetRawAPIFounderOnlyAndStrictInput(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	path := "/v1/agents/" + f.target.ID + "/reset"
	req := domain.AgentResetRequest{Command: domain.Command{OperationID: "raw-reset"}, Reason: "Reset owned runtime, preserving identity and uncertainty"}
	spoof := f.target
	spoof.Founder = true
	for _, actor := range []domain.Agent{f.target, f.delegate, spoof} {
		for _, target := range []string{f.target.ID, actor.ID, f.founder.ID} {
			envelope, code := f.request(t, actor, http.MethodPost, "/v1/agents/"+target+"/reset", req)
			if code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
				t.Fatal("raw reset accepted target/delegate/spoofed Founder authority", code, envelope)
			}
		}
	}
	for _, extra := range []string{"force", "acknowledge_stopped", "erase_identity", "mode", "session_id", "expected_fence"} {
		body := map[string]any{"operation_id": domain.NewID("op"), "reason": "No caller-supplied reset authority", extra: true}
		envelope, code := f.request(t, f.founder, http.MethodPost, path, body)
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("reset accepted protected or unknown input", extra, code, envelope)
		}
	}
	envelope, code := f.request(t, f.founder, http.MethodPost, path+"?force=true", req)
	if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
		t.Fatal("reset accepted query authority", code, envelope)
	}
	envelope, code = f.request(t, f.founder, http.MethodPost, path, req)
	operation := apiAgentOperation(t, envelope, code)
	if operation.Kind != "runtime-reset" || operation.State != "queued" || operation.Result != nil {
		t.Fatal("reset submission claimed execution", operation)
	}
	checking := apiCheckOperation(t, f, operation)
	report := domain.AgentOperationReport{Command: domain.Command{OperationID: "raw-reset-result", ExpectedRevision: checking.Revision}, State: "reset",
		Result: &domain.AgentOperationResult{Code: "runtime-reset", ModeAtCheck: "ai", Observation: &domain.AgentInspection{ObservedAt: operation.CreatedAt, WorkSlot: "free"}}}
	envelope, code = f.request(t, f.target, http.MethodPost, "/v1/agent-operations/"+operation.ID+"/result", report)
	if got := apiAgentOperation(t, envelope, code); got.State != "reset" {
		t.Fatal(got)
	}
	envelope, code = f.request(t, f.founder, http.MethodPost, "/v1/agents/"+f.founder.ID+"/reset", domain.AgentResetRequest{Command: domain.Command{OperationID: "raw-founder-self-reset"}, Reason: "Founder self runtime reset"})
	if got := apiAgentOperation(t, envelope, code); got.TargetID != f.founder.ID || got.RequesterID != f.founder.ID || got.State != "queued" {
		t.Fatal("Founder self-reset was not durably supported", got)
	}
}
