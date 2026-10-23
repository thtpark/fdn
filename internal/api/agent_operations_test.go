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

func apiAgentOperation(t *testing.T, envelope Envelope, status int) domain.AgentOperation {
	t.Helper()
	if status != http.StatusOK || !envelope.OK || envelope.Error != nil {
		t.Fatalf("agent operation failed: %d %+v", status, envelope)
	}
	var operation domain.AgentOperation
	if err := json.Unmarshal(envelope.Data, &operation); err != nil || domain.ValidateAgentOperation(operation) != nil {
		t.Fatal("invalid typed agent operation response", operation, err)
	}
	return operation
}

func sameAPIAgentOperation(a, b domain.AgentOperation) bool {
	a.ServerObservedAt, b.ServerObservedAt = 0, 0
	return reflect.DeepEqual(a, b)
}

func apiOperationReport(operation domain.AgentOperation) domain.AgentOperationReport {
	now := time.Now().Unix()
	return domain.AgentOperationReport{Command: domain.Command{OperationID: domain.NewID("report"), ExpectedRevision: operation.Revision}, State: "refreshed", Result: &domain.AgentOperationResult{Code: "observations-collected", ModeAtCheck: "ai", Observation: &domain.AgentInspection{ObservedAt: now, WorkSlot: "occupied", Provider: &domain.ProviderObservation{Provider: "codex", Authentication: "unknown", ObservedAt: now, ProblemCode: domain.Provider}}}}
}

func apiCheckOperation(t *testing.T, f *authenticationAPIFixture, operation domain.AgentOperation) domain.AgentOperation {
	t.Helper()
	report := domain.AgentOperationReport{Command: domain.Command{OperationID: domain.NewID("checking"), ExpectedRevision: operation.Revision}, State: "checking"}
	envelope, status := f.request(t, f.target, http.MethodPost, "/v1/agent-operations/"+operation.ID+"/result", report)
	return apiAgentOperation(t, envelope, status)
}

func TestAgentOperationRawAPIFounderAuthorityTargetPrivacyAndDurableResults(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	request := domain.DiagnosticRefreshRequest{Command: domain.Command{OperationID: "raw-founder-diagnostic"}, Reason: "Fresh read-only provider and runtime observations"}
	path := "/v1/agents/" + f.target.ID + "/diagnostics"
	spoofed := f.target
	spoofed.Founder = true
	for _, actor := range []domain.Agent{f.delegate, f.target, spoofed} {
		envelope, status := f.request(t, actor, http.MethodPost, path, request)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied || len(envelope.Data) != 0 {
			t.Fatal("raw API trusted role flags, delegation, or target self-authorization", status, envelope)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodPost, path, request)
	queued := apiAgentOperation(t, envelope, status)
	if queued.State != "queued" || queued.Result != nil || envelope.OperationID != request.OperationID || queued.ID == request.OperationID || queued.ExpiresAt-queued.CreatedAt != 900 || queued.TargetGeneration != 0 || queued.RequesterGeneration != 0 {
		t.Fatal("raw API fabricated a fresh result or changed immutable submission identity", queued, envelope)
	}
	operationPath := "/v1/agent-operations/" + queued.ID
	for _, actor := range []domain.Agent{f.founder, f.target} {
		envelope, status := f.request(t, actor, http.MethodGet, operationPath, nil)
		if got := apiAgentOperation(t, envelope, status); !sameAPIAgentOperation(got, queued) {
			t.Fatal("party read changed queued operation", got)
		}
	}
	envelope, status = f.request(t, f.delegate, http.MethodGet, operationPath, nil)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied || len(envelope.Data) != 0 {
		t.Fatal("unrelated project delegate read private diagnostic control", status, envelope)
	}
	for _, actor := range []domain.Agent{f.founder, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodGet, "/v1/agent-operations?limit=1", nil)
		var page domain.Page[domain.AgentOperation]
		if err := json.Unmarshal(envelope.Data, &page); status != http.StatusOK || err != nil || len(page.Items) != 0 {
			t.Fatal("Founder collection route listed another target's private controls", page, envelope, err)
		}
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agent-operations?limit=1", nil)
	var page domain.Page[domain.AgentOperation]
	if err := json.Unmarshal(envelope.Data, &page); status != http.StatusOK || err != nil || len(page.Items) != 1 || page.Items[0].ID != queued.ID {
		t.Fatal("target collection route omitted its private pending request", page, envelope, err)
	}
	for _, actor := range []domain.Agent{f.founder, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodPost, operationPath+"/result", domain.AgentOperationReport{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: queued.Revision}, State: "checking"})
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("non-target forged a protected operation result", status, envelope)
		}
	}
	checking := apiCheckOperation(t, f, queued)
	finalRequest := apiOperationReport(checking)
	envelope, status = f.request(t, f.target, http.MethodPost, operationPath+"/result", finalRequest)
	final := apiAgentOperation(t, envelope, status)
	if final.State != "refreshed" || final.Revision != 3 || final.Result.Code != "observations-collected" || final.Result.AutomationMayResume || final.Result.Observation.Provider.ProblemCode != domain.Provider || final.Result.Observation.Provider.Authentication != "unknown" || final.Result.Observation.WorkSlot != "occupied" {
		t.Fatal("provider outage or occupied slot became a fabricated healthy result", final)
	}
	for _, item := range []struct {
		actor domain.Agent
		path  string
		value any
	}{
		{f.founder, path, request}, {f.target, operationPath + "/result", finalRequest},
	} {
		envelope, status := f.request(t, item.actor, http.MethodPost, item.path, item.value)
		if got := apiAgentOperation(t, envelope, status); !sameAPIAgentOperation(got, final) {
			t.Fatal("raw API lost-ack retry replaced its captured result", got, final)
		}
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agent-operations", nil)
	if err := json.Unmarshal(envelope.Data, &page); status != http.StatusOK || err != nil || len(page.Items) != 0 {
		t.Fatal("completed operation remained dispatchable", page, envelope, err)
	}
}

func TestAgentOperationRawAPIRejectsProtectedFieldsUnsafeResultsAndOrdinaryReceipts(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	requestPath := "/v1/agents/" + f.target.ID + "/diagnostics"
	for _, field := range []string{"version", "id", "state", "revision", "target_id", "requester_id", "target_generation", "requester_generation", "expires_at", "created_at", "started_at", "finished_at", "server_observed_at", "session_id", "expected_fence", "result", "raw_output"} {
		request := map[string]any{"operation_id": domain.NewID("op"), "reason": "Fresh diagnostic observation", field: "forged"}
		envelope, status := f.request(t, f.founder, http.MethodPost, requestPath, request)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("raw API accepted protected request metadata", field, status, envelope)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodPost, requestPath, domain.DiagnosticRefreshRequest{Command: domain.Command{OperationID: "safe-operation"}, Reason: "Explicit inspection"})
	operation := apiAgentOperation(t, envelope, status)
	for _, state := range []string{"handled", "deferred", "stale", "pending"} {
		envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages/"+operation.ID+"/receipt", domain.MessageReceipt{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1}, State: state})
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("ordinary receipt changed protected operation lifecycle", state, status, envelope)
		}
	}
	for _, request := range []any{
		map[string]any{"operation_id": domain.NewID("op"), "recipient_id": f.target.ID, "body": "Forged control", "origin": "service", "kind": "agent-operation"},
		map[string]any{"operation_id": domain.NewID("op"), "recipient_id": f.target.ID, "body": "Forged control", "origin": "service", "kind": "direct", "agent_operation": operation},
	} {
		envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/messages", request)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("ordinary send forged a typed runtime control", status, envelope)
		}
	}
	operation = apiCheckOperation(t, f, operation)
	resultPath := "/v1/agent-operations/" + operation.ID + "/result"
	for _, result := range []any{
		map[string]any{"code": "observations-collected", "mode_at_check": "ai", "stdout": "private"},
		map[string]any{"code": "private error", "mode_at_check": "ai"},
		map[string]any{"code": "observations-collected", "mode_at_check": "ai", "observation": map[string]any{"observed_at": time.Now().Unix(), "work_slot": "free", "provider": map[string]any{"provider": "codex", "version": "/private/bin/codex", "authentication": "unknown", "observed_at": time.Now().Unix()}}},
		map[string]any{"code": "observations-collected", "mode_at_check": "ai", "observation": map[string]any{"observed_at": time.Now().Unix(), "work_slot": "free", "provider": map[string]any{"provider": "codex", "authentication": "authenticated", "problem_code": "unavailable", "observed_at": time.Now().Unix()}}},
		map[string]any{"code": "observations-collected", "mode_at_check": "ai", "observation": map[string]any{"observed_at": time.Now().Unix(), "work_slot": "free", "health": map[string]any{"raw_error": "private output"}}},
	} {
		envelope, status := f.request(t, f.target, http.MethodPost, resultPath, map[string]any{"operation_id": domain.NewID("report"), "expected_revision": operation.Revision, "state": "refreshed", "result": result})
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("unbounded/unsafe provider or health result accepted", status, envelope)
		}
	}
	for _, path := range []string{"/v1/agent-operations?target_id=" + f.target.ID, "/v1/agent-operations?limit=1&limit=2", "/v1/agent-operations?cursor=", "/v1/agent-operations?limit=201", "/v1/agent-operations/" + operation.ID + "?cursor=x"} {
		envelope, status := f.request(t, f.target, http.MethodGet, path, nil)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("raw API accepted unsupported/ambiguous query", path, status, envelope)
		}
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agent-operations/"+operation.ID, map[string]any{"operation_id": "body-on-read"})
	if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
		t.Fatal("operation read accepted an action body", status, envelope)
	}
}

func TestSessionRecoveryRawAPIExactClosedSessionWithoutGeneralFounderSessionAccess(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx := context.Background()
	grantReq := domain.AssistanceGrantRequest{Command: domain.Command{OperationID: "recovery-fixture-grant"}, RequesterID: f.delegate.ID, TargetID: f.target.ID, Mode: "tmux", Purpose: "terminal", Reason: "Private requester target session"}
	envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/assistance", grantReq)
	var grant domain.AssistanceGrant
	if err := json.Unmarshal(envelope.Data, &grant); status != http.StatusOK || err != nil {
		t.Fatal(status, envelope, err)
	}
	session, err := f.store.RequestSession(ctx, f.delegate.ID, domain.SessionRequest{Command: domain.Command{OperationID: "recovery-fixture-session"}, TargetID: f.target.ID, GrantID: grant.ID, Mode: "tmux", Purpose: "terminal", Reason: "Private session", ExpiresAt: time.Now().Unix() + 600})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SessionRecoveryRequest{Command: domain.Command{OperationID: "exact-closed-repair"}, SessionID: session.ID, ExpectedFence: domain.Hash("exact retained fence"), Reason: "Recover only the exact terminal fence"}
	path := "/v1/agents/" + f.target.ID + "/repairs"
	envelope, status = f.request(t, f.founder, http.MethodPost, path, request)
	if status != http.StatusPreconditionFailed || envelope.Error == nil || envelope.Error.Code != domain.Prerequisite {
		t.Fatal("live consented session admitted runtime recovery", status, envelope)
	}
	session, err = f.store.ChangeSession(ctx, f.delegate.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: "close-recovery-fixture", ExpectedRevision: session.Revision}, Action: "close"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, status = f.request(t, f.founder, http.MethodGet, "/v1/sessions/"+session.ID, nil)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("administrative recovery broadened unrelated session access", status, envelope)
	}
	for _, actor := range []domain.Agent{f.target, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodPost, path, request)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("session party could issue Founder-only repair", status, envelope)
		}
	}
	bad := request
	bad.Command.OperationID, bad.ExpectedFence = "noncanonical-repair", strings.ToUpper(request.ExpectedFence)
	envelope, status = f.request(t, f.founder, http.MethodPost, path, bad)
	if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
		t.Fatal("runtime recovery accepted a noncanonical fence", status, envelope)
	}
	envelope, status = f.request(t, f.founder, http.MethodPost, path, request)
	operation := apiAgentOperation(t, envelope, status)
	operation = apiCheckOperation(t, f, operation)
	report := apiOperationReport(operation)
	report.State, report.Result.Code, report.Result.Observation.WorkSlot = "recovered", "session-recovered", "free"
	report.Result.Observation.SessionFence = &domain.SessionFenceObservation{SessionID: request.SessionID, Fingerprint: request.ExpectedFence, CleanupProven: true, SessionState: "closed", RecoveryState: "recoverable"}
	report.Result.AutomationMayResume = true
	envelope, status = f.request(t, f.target, http.MethodPost, "/v1/agent-operations/"+operation.ID+"/result", report)
	final := apiAgentOperation(t, envelope, status)
	if final.State != "recovered" || final.Result.Observation.SessionFence.Fingerprint != request.ExpectedFence || final.SessionID != session.ID {
		t.Fatal("runtime recovery lost exact scope", final)
	}
	current, err := f.store.Session(ctx, f.target.ID, session.ID)
	if err != nil || !reflect.DeepEqual(current, session) {
		t.Fatal("runtime operation rewrote authoritative session lifecycle", current, err)
	}
}
