package identityservice

import (
	"net/http"
	"net/url"
	"testing"
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
