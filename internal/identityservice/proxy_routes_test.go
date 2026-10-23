package identityservice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestPublicIdentityStatusAndFounderRevocationRemainOrdinaryRoutes(t *testing.T) {
	for _, item := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/v1/identity/status", true},
		{http.MethodPost, "/v1/agents/agent_fixture/revoke", true},
		{http.MethodPost, "/v1/agents/agent_fixture/authentication", true},
		{http.MethodGet, "/v1/agents/agent_fixture/screens", true},
		{http.MethodGet, "/v1/agents/agent_fixture/diagnostics", true},
		{http.MethodGet, "/v1/messages/message_fixture/diagnostics", true},
		{http.MethodPost, "/v1/messages/message_fixture/read", true},
		{http.MethodGet, "/v1/messages/message_fixture/read", false},
		{http.MethodPut, "/v1/messages/message_fixture/read", false},
		{http.MethodPost, "/v1/messages/message_fixture/read/extra", false},
		{http.MethodPost, "/v1/messages/message_fixture/read?read_at=1", false},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics", true},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs", true},
		{http.MethodPost, "/v1/agents/agent_fixture/status", true},
		{http.MethodGet, "/v1/agents/agent_fixture/status", false},
		{http.MethodPut, "/v1/agents/agent_fixture/status", false},
		{http.MethodPost, "/v1/agents/agent_fixture/status/extra", false},
		{http.MethodPost, "/v1/agents/agent_fixture/status?force=true", false},
		{http.MethodGet, "/v1/agent-operations", true},
		{http.MethodGet, "/v1/agent-operations?limit=10&cursor=opaque_cursor", true},
		{http.MethodGet, "/v1/agent-operations/message_fixture", true},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result", true},
		{http.MethodPost, "/v1/messages/message_fixture/diagnostics", false},
		{http.MethodPut, "/v1/agents/agent_fixture/diagnostics", false},
		{http.MethodDelete, "/v1/agents/agent_fixture/diagnostics", false},
		{http.MethodGet, "/v1/messages/message_fixture/diagnostics/credentials", false},
		{http.MethodGet, "/v1/agents/agent_fixture/diagnostics/owner", false},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics/refresh", false},
		{http.MethodGet, "/v1/agents/agent_fixture/repairs", false},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs/session_fixture", false},
		{http.MethodPost, "/v1/agent-operations", false},
		{http.MethodPost, "/v1/agent-operations/message_fixture", false},
		{http.MethodGet, "/v1/agent-operations/message_fixture/result", false},
		{http.MethodPut, "/v1/agent-operations/message_fixture/result", false},
		{http.MethodGet, "/v1/agent-operations/message_fixture/private", false},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result/extra", false},
		{http.MethodGet, "/v1/agent-operations?actor=agent_other", false},
		{http.MethodGet, "/v1/agent-operations?limit=10&limit=20", false},
		{http.MethodGet, "/v1/agent-operations?limit=%zz", false},
		{http.MethodGet, "/v1/agent-operations/message_fixture?credentials=1", false},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result?force=1", false},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics?provider=codex", false},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs?acknowledge_stopped=1", false},
		{http.MethodPost, "/v1/agents/agent_fixture/screens", true},
		{http.MethodPost, "/v1/assistance/requests", true},
		{http.MethodPost, "/v1/identity/register", true},
		{http.MethodPost, "/v1/identity/agent_fixture/recover", true},
		{http.MethodGet, "/v1/identity/owner", false},
		{http.MethodGet, "/v1/identity/challenge", false},
		{http.MethodPost, "/v1/identity/activate", false},
		{http.MethodPost, "/v1/identity/recover-founder", false},
		{http.MethodPost, "/local/restart", false},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			u, err := url.Parse(item.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := ordinaryPath(item.method, u); got != item.allowed {
				t.Fatalf("ordinary route allowed=%t, want%t", got, item.allowed)
			}
		})
	}
}

func TestWorkerProxyForwardsExactDiagnosticAndOperationRoutes(t *testing.T) {
	f := newFixture(t)
	type recordedRequest struct {
		request *http.Request
		body    []byte
	}
	forwarded := make(chan recordedRequest, 1)
	f.remote.ordinary = func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			var err error
			body, err = io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
		}
		forwarded <- recordedRequest{request.Clone(context.Background()), body}
		data, _ := json.Marshal(domain.AgentOperation{Version: domain.AgentOperationVersion, ID: "message_fixture", State: "queued", CreatedAt: 1800000000, ExpiresAt: 1800000900, ServerObservedAt: 1800000002})
		envelope, _ := json.Marshal(api.Envelope{OK: true, Data: data, Warnings: []string{}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}, "X-Fdn-Api-Version": {strconv.Itoa(domain.APIVersion)}, "X-Identity-Proof": {"fixture-private-proof"}, "Set-Cookie": {"fixture-private-cookie"}}, Body: io.NopCloser(bytes.NewReader(envelope)), Request: request}, nil
	}
	_, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	publicText := "Running local tests"
	for _, item := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/agents/agent_fixture/status", domain.AgentStatusRequest{Command: domain.Command{OperationID: "status-fixture"}, Text: &publicText}},
		{http.MethodGet, "/v1/agents/agent_fixture/diagnostics", nil},
		{http.MethodGet, "/v1/messages/message_fixture/diagnostics", nil},
		{http.MethodGet, "/v1/agent-operations?limit=10&cursor=opaque_cursor", nil},
		{http.MethodGet, "/v1/agent-operations/message_fixture", nil},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics", domain.DiagnosticRefreshRequest{Command: domain.Command{OperationID: "refresh-fixture"}, Reason: "Founder requested fresh diagnostics"}},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs", domain.SessionRecoveryRequest{Command: domain.Command{OperationID: "repair-fixture"}, SessionID: "session_fixture", ExpectedFence: domain.Hash("exact fixture fence"), Reason: "Recover observed closed session"}},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result", domain.AgentOperationReport{Command: domain.Command{OperationID: "result-fixture"}, State: "refreshed", Result: &domain.AgentOperationResult{Code: "observed", CapturedAt: 1800000001, ModeAtCheck: "manual"}}},
		{http.MethodPost, "/v1/messages/message_fixture/read", domain.MessageReadRequest{Command: domain.Command{OperationID: "read-fixture"}}},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			var body []byte
			if item.body != nil {
				body, err = json.Marshal(item.body)
				if err != nil {
					t.Fatal(err)
				}
			}
			request, err := http.NewRequestWithContext(ctx, item.method, client.BaseURL+item.path, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/json")
			for _, name := range []string{"Authorization", "Proxy-Authorization", "X-Fdn-Agent", "X-Fdn-Identity", "X-Identity", "X-Client-Cert", "X-Worker-UID", "Forwarded", "X-Forwarded-User", "Cookie"} {
				request.Header.Set(name, "fixture-private-caller-value")
			}
			response, err := client.HTTP.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
				t.Fatal("ordinary operation blocked before server policy", response.StatusCode, readErr, closeErr)
			}
			if response.Header.Get("X-Identity-Proof") != "" || response.Header.Get("Set-Cookie") != "" {
				t.Fatal("remote transport/private metadata reached the runner")
			}
			var got recordedRequest
			select {
			case got = <-forwarded:
			case <-ctx.Done():
				t.Fatal("ordinary operation never reached server transport")
			}
			if got.request.Method != item.method || got.request.URL.RequestURI() != item.path || got.request.Host != "localhost:8443" || !bytes.Equal(got.body, body) {
				t.Fatal("proxy changed exact method/path/query/body", got.request.Method, got.request.URL, string(got.body))
			}
			for name := range request.Header {
				if name != "Accept" && name != "Content-Type" && got.request.Header.Get(name) != "" {
					t.Fatal("proxy forwarded private caller identity metadata", name)
				}
			}
			if got.request.Header.Get("Accept") != "application/json" || got.request.Header.Get("Content-Type") != "application/json" {
				t.Fatal("ordinary content headers changed")
			}
		})
	}
}

func TestWorkerProxyDeniesDiagnosticAndOperationEscapes(t *testing.T) {
	f := newFixture(t)
	_, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, item := range []struct{ method, path string }{
		{http.MethodPost, "/v1/messages/message_fixture/diagnostics"},
		{http.MethodPut, "/v1/agents/agent_fixture/diagnostics"},
		{http.MethodPatch, "/v1/agents/agent_fixture/diagnostics"},
		{http.MethodDelete, "/v1/agents/agent_fixture/diagnostics"},
		{http.MethodHead, "/v1/agents/agent_fixture/diagnostics"},
		{http.MethodOptions, "/v1/agents/agent_fixture/diagnostics"},
		{http.MethodGet, "/v1/agents/agent_fixture/repairs"},
		{http.MethodDelete, "/v1/agents/agent_fixture/repairs"},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs/session_fixture"},
		{http.MethodGet, "/v1/agents/agent_fixture/diagnostics/private"},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics/refresh"},
		{http.MethodPost, "/v1/agent-operations"},
		{http.MethodDelete, "/v1/agent-operations/message_fixture"},
		{http.MethodPost, "/v1/agent-operations/message_fixture"},
		{http.MethodGet, "/v1/agent-operations/message_fixture/result"},
		{http.MethodPut, "/v1/agent-operations/message_fixture/result"},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result/private"},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result?force=1"},
		{http.MethodGet, "/v1/agent-operations?target=agent_other"},
		{http.MethodGet, "/v1/agent-operations?cursor=a&cursor=b"},
		{http.MethodPost, "/v1/agents/agent_fixture/repairs?acknowledge_stopped=1"},
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics?provider=codex"},
		{http.MethodPost, "/v1/agent-operations/message_fixture/../../identity/activate"},
		{http.MethodPost, "/v1/agent-operations/message_fixture/result%2fprivate"},
		{http.MethodPost, "/v1/identity/owner"},
		{http.MethodPost, "/v1/identity/recover-founder"},
		{http.MethodPost, "/local/restart"},
		{http.MethodPost, "/local/service/stop"},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			request, err := http.NewRequest(item.method, client.BaseURL+item.path, bytes.NewReader([]byte(`{"operation_id":"denied-fixture"}`)))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.HTTP.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatal("operation escape was not denied at the Unix proxy", response.StatusCode)
			}
			select {
			case request := <-f.remote.requests:
				t.Fatal("denied control/write reached Foundation", request.Method, request.URL)
			default:
			}
		})
	}
}
