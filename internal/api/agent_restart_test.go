package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
)

func TestAgentNativeRestartRawAndBinaryAPIDenyAuthorityAndInputOverrides(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	path := "/v1/agents/" + f.target.ID + "/restart"
	for _, actor := range []domain.Agent{f.target, f.delegate} {
		envelope, code := f.request(t, actor, http.MethodPost, path, domain.AgentRestartRequest{Command: domain.Command{
			OperationID: domain.NewID("op")}, Reason: "Not Founder"})
		if code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal(envelope, code)
		}
	}
	for _, key := range []string{"command", "policy_file", "host", "force", "erase_identity", "mode", "acknowledge_stopped"} {
		envelope, code := f.request(t, f.founder, http.MethodPost, path, map[string]any{
			"operation_id": domain.NewID("op"), "reason": "No arbitrary native options", key: true})
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal(key, envelope, code)
		}
	}
	envelope, code := f.request(t, f.founder, http.MethodPost, path+"?force=true",
		domain.AgentRestartRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Reason: "No query override"})
	if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
		t.Fatal(envelope, code)
	}
	client := messageFixtureClient(t, f.server, f.target.ID)
	_, err := Call[domain.AgentOperation](context.Background(), client, http.MethodPost, path,
		domain.AgentRestartRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Reason: "Cannot self-restart"})
	if domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native binary caller bypassed Founder-only authority", err)
	}
}

func TestAgentNativeRestartLeaseAndReportsAreRootPrivateEvenForFounder(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ca, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.server.identity.ca, f.server.identity.key = ca.CA, ca.CAKey
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	operation, err := Call[domain.AgentOperation](ctx, founder, http.MethodPost, Path("agents", f.target.ID, "restart"),
		domain.AgentRestartRequest{Command: domain.Command{OperationID: "native-root-restart"}, Reason: "Restart through the independently pinned receiver"})
	if err != nil || operation.State != "queued" {
		t.Fatal(operation, err)
	}
	for _, name := range []string{"query.maintenance.agent-restarts", "command.maintenance.agent-restart-result", "command.maintenance.agent-restart-lease"} {
		if _, err := founder.Message(ctx, message.Request{Operation: name}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("ordinary Founder traffic acquired native supervisor authority", name, err)
		}
	}
	var workers sync.WaitGroup
	root := NewMessageClient("fixture://private-maintenance", func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		workers.Go(func() { defer left.Close(); _ = f.server.serveMaintenanceMessages(ctx, left) })
		return right, nil
	})
	defer func() { root.Close(); cancel(); workers.Wait() }()
	raw, err := root.Message(ctx, message.Request{Operation: "query.maintenance.agent-restarts"})
	if err != nil {
		t.Fatal(err)
	}
	var permits []domain.AgentRestartPermit
	if err := message.UnmarshalPayload(raw, &permits); err != nil || len(permits) != 1 || permits[0].ID != operation.ID {
		t.Fatal(permits, err)
	}
	report := domain.AgentRestartReport{Command: domain.Command{OperationID: "native-root-checking", ExpectedRevision: operation.Revision},
		ID: operation.ID, State: "checking"}
	payload, err := message.MarshalPayload(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Message(ctx, message.Request{Operation: "command.maintenance.agent-restart-result", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	leaseRequest := domain.AgentRestartLeaseRequest{Command: domain.Command{OperationID: "native-root-lease"}, ID: operation.ID,
		Nonce: domain.NewToken(), PermitSHA256: domain.Hash(permits[0].Signature)}
	payload, err = message.MarshalPayload(leaseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Message(ctx, message.Request{Operation: "command.maintenance.agent-restart-lease", Target: f.target.ID, Payload: payload}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("root restart lease accepted caller scope overrides", err)
	}
	if _, err := root.Message(ctx, message.Request{Operation: "command.maintenance.agent-restart-lease", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	report.Command = domain.Command{OperationID: "native-root-completed", ExpectedRevision: operation.Revision + 1}
	report.State = "restarted"
	report.Result = &domain.AgentOperationResult{Code: "native-services-restarted", ModeAtCheck: "manual",
		NativeRuntime: &domain.NativeRuntimeObservation{Version: "0.3.41", Commit: strings.Repeat("a", 40),
			IdentityGeneration: operation.TargetGeneration, ServicesRunning: true}}
	payload, err = message.MarshalPayload(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Message(ctx, message.Request{Operation: "command.maintenance.agent-restart-result", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	result, err := Call[domain.AgentOperation](ctx, founder, http.MethodGet, Path("agent-operations", operation.ID), nil)
	if err != nil || result.State != "restarted" || result.Result == nil || result.Result.NativeRuntime == nil ||
		!result.Result.NativeRuntime.ServicesRunning {
		t.Fatal(result, err)
	}
}
