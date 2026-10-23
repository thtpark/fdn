package identityservice

import (
	"context"
	"net/http"
	"net/url"
	"testing"

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
		{http.MethodPost, "/v1/agents/agent_fixture/diagnostics", false},
		{http.MethodPost, "/v1/messages/message_fixture/diagnostics", false},
		{http.MethodDelete, "/v1/agents/agent_fixture/diagnostics", false},
		{http.MethodGet, "/v1/messages/message_fixture/diagnostics/credentials", false},
		{http.MethodGet, "/v1/agents/agent_fixture/diagnostics/owner", false},
		{http.MethodPost, "/v1/agents/agent_fixture/screens", true},
		{http.MethodPost, "/v1/assistance/requests", true},
		{http.MethodPost, "/v1/identity/register", true},
		{http.MethodPost, "/v1/identity/agent_fixture/recover", true},
		{http.MethodGet, "/v1/identity/owner", false},
		{http.MethodGet, "/v1/identity/challenge", false},
		{http.MethodPost, "/v1/identity/activate", false},
		{http.MethodPost, "/v1/identity/recover-founder", false},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			if got := ordinaryPath(item.method, &url.URL{Path: item.path}); got != item.allowed {
				t.Fatalf("ordinary route allowed=%t, want%t", got, item.allowed)
			}
		})
	}
}

func TestWorkerProxyForwardsOnlyReadOnlyDiagnostics(t *testing.T) {
	f := newFixture(t)
	_, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, path := range []string{"/v1/agents/agent_fixture/diagnostics", "/v1/messages/message_fixture/diagnostics"} {
		if err := client.Request(context.Background(), http.MethodGet, path, nil, nil); err != nil {
			t.Fatal("diagnostic read was blocked before server authorization", path, err)
		}
		forwarded := <-f.remote.requests
		if forwarded.Method != http.MethodGet || forwarded.URL.Path != path {
			t.Fatal("diagnostic route changed at the identity boundary", forwarded.Method, forwarded.URL.Path)
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if err := client.Request(context.Background(), method, path, domain.Command{OperationID: "denied-diagnostics-write"}, nil); domain.AsFault(err).Code != domain.Denied {
				t.Fatal("diagnostic forwarding widened mutation authority", method, path, err)
			}
		}
		select {
		case request := <-f.remote.requests:
			t.Fatal("denied diagnostic mutation reached the server", request.Method, request.URL.Path)
		default:
		}
	}
}
