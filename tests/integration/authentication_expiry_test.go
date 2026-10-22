//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/work"
)

func founderAuthenticationRefresh(t *testing.T, h *harness, provider, project string) domain.Message {
	t.Helper()
	message, err := api.Call[domain.Message](context.Background(), h.founder, "POST", api.Path("agents", h.workerAgent.ID, "authentication"),
		domain.AuthenticationRefreshRequest{Command: command(), Provider: provider, ProjectID: project, Reason: "Founder requests a fresh protected sign-in notice"})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func requireAuthenticationRefresh(t *testing.T, engine *work.Engine) work.Status {
	t.Helper()
	status, err := engine.Step(context.Background(), false)
	if err != nil || status.State != "authentication-refreshed" {
		t.Fatal("Founder sign-in refresh was not processed without inference", status, err)
	}
	return status
}

func TestAuthenticationExpiryWaitsForFounderAndSupersedesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	first := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	if first.ExpiresAt-first.CreatedAt != domain.AuthenticationRequestTTL {
		t.Fatal("wrong sign-in request lifetime", first)
	}
	message, err := api.Call[domain.Message](context.Background(), h.founder, "GET", api.Path("messages", first.Deliveries[0].MessageID), nil)
	if err != nil || message.ExpiresAt != first.ExpiresAt {
		t.Fatal("private notice and local episode have different deadlines", message, err)
	}
	engine.Now = func() time.Time { return time.Unix(first.ExpiresAt, 0) }
	for range 3 {
		status := requireAuthenticationBlock(t, engine, domain.Prerequisite)
		if status.AuthenticationRequests[0].ID != first.ID || status.AuthenticationRequests[0].State != "expired" ||
			!strings.Contains(status.Blockers[len(status.Blockers)-1].Reason, "Founder must explicitly request") {
			t.Fatal("expiry silently renewed or hid the required Founder action", status)
		}
	}
	engine = restartAuthenticationEngine(t, engine)
	control := founderAuthenticationRefresh(t, h, "codex", "")
	second := requireAuthenticationRefresh(t, engine).AuthenticationRequests[0]
	if second.ID == first.ID || second.Supersedes != first.ID || second.RefreshID != control.ID || second.State != "requested" || second.ExpiresAt-second.CreatedAt != 900 {
		t.Fatal("explicit refresh did not create one fresh superseding request", first, second)
	}
	if x.invocations != 0 || len(authenticationInbox(t, h.founder)) != 2 {
		t.Fatal("expiry/refresh duplicated notifications or invoked a model", x.invocations)
	}
	engine = restartAuthenticationEngine(t, engine)
	for range 2 {
		next := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
		if next.ID != second.ID {
			t.Fatal("restart/poll repeated a handled refresh", next)
		}
	}
	receipt, err := api.Call[domain.Message](context.Background(), h.founder, "GET", api.Path("messages", control.ID), nil)
	if err != nil || receipt.State != "handled" {
		t.Fatal("Founder cannot observe target refresh completion", receipt, err)
	}
}

func TestFounderSignInRefreshWorksWhilePausedWithoutResumingOrInference(t *testing.T) {
	for _, authentication := range []string{"login-required", "credentials-present-unverified", "unknown"} {
		t.Run(authentication, func(t *testing.T) {
			h := newHarness(t)
			x := &authenticationExecutor{authentication: authentication}
			engine := authenticationEngine(t, h, x)
			pause := domain.ModeRequest{Command: command(), Mode: "manual", Reason: "Founder pauses before arranging sign-in"}
			pause.ExpectedRevision = h.workerAgent.Revision
			if _, err := api.Call[domain.Agent](context.Background(), h.founder, "POST", api.Path("agents", h.workerAgent.ID, "mode"), pause); err != nil {
				t.Fatal(err)
			}
			control := founderAuthenticationRefresh(t, h, "copilot", "")
			status, err := engine.Step(context.Background(), false)
			if authentication == "unknown" {
				if err == nil || len(status.AuthenticationRequests) != 0 || len(authenticationInbox(t, h.founder)) != 0 {
					t.Fatal("unknown readiness manufactured a sign-in request", status, err)
				}
			} else if err != nil || status.State != "authentication-refreshed" {
				t.Fatal("paused worker did not process explicit Founder control", status, err)
			}
			if authentication == "login-required" {
				if len(status.AuthenticationRequests) != 1 || status.AuthenticationRequests[0].Provider != "copilot" || status.AuthenticationRequests[0].RefreshID != control.ID {
					t.Fatal("refresh used the configured rather than requested provider", status)
				}
			} else if len(status.AuthenticationRequests) != 0 {
				t.Fatal("healthy/unknown readiness invented missing authentication", status)
			}
			if x.invocations != 0 || status.Mode != "manual" || len(x.checks) != 1 || x.checks[0] != "copilot" {
				t.Fatal("explicit readiness refresh resumed automation or invoked a model", status, x)
			}
			sessions, err := api.Call[[]domain.Session](context.Background(), h.worker, "GET", "/v1/sessions", nil)
			if err != nil || len(sessions) != 0 {
				t.Fatal("notice refresh started a login session", sessions, err)
			}
		})
	}
}

func TestFounderAuthenticationRefreshDeliveryLostAckReusesPreparedEpisode(t *testing.T) {
	for _, mode := range []string{"unavailable", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			x := &authenticationExecutor{authentication: "login-required"}
			engine := authenticationEngine(t, h, x)
			control := founderAuthenticationRefresh(t, h, "codex", "")
			original := h.worker.HTTP.Transport
			transport := &authenticationTransport{base: original, mode: mode}
			h.worker.HTTP.Transport = transport
			defer func() { h.worker.HTTP.Transport = original }()
			first := requireAuthenticationBlock(t, engine, domain.Unavailable).AuthenticationRequests[0]
			if first.RefreshID != control.ID || first.State != "delivery-pending" {
				t.Fatal("failed refreshed notice lost its exact control binding", first)
			}
			engine = restartAuthenticationEngine(t, engine)
			second := requireAuthenticationRefresh(t, engine).AuthenticationRequests[0]
			if second.ID != first.ID || second.ExpiresAt != first.ExpiresAt || second.State != "requested" || x.invocations != 0 || len(x.checks) != 1 ||
				len(authenticationInbox(t, h.founder)) != 1 || len(transport.operations) != 2 || transport.operations[0] != transport.operations[1] {
				t.Fatal("refresh recovery repeated the probe/episode/message or lost its deadline", first, second, transport.operations)
			}
		})
	}
}

type authenticationReceiptLoss struct {
	base http.RoundTripper
	lost bool
}

func (transport *authenticationReceiptLoss) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil && !transport.lost && request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/receipt") {
		transport.lost = true
		response.Body.Close()
		return nil, domain.Fail(domain.Unavailable, "fixture lost refresh receipt acknowledgement")
	}
	return response, err
}

func TestFounderAuthenticationRefreshReceiptLostAckDoesNotRepeatNotification(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	founderAuthenticationRefresh(t, h, "codex", "")
	original := h.worker.HTTP.Transport
	h.worker.HTTP.Transport = &authenticationReceiptLoss{base: original}
	defer func() { h.worker.HTTP.Transport = original }()
	first := requireAuthenticationBlock(t, engine, domain.Unavailable).AuthenticationRequests[0]
	engine = restartAuthenticationEngine(t, engine)
	second := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	if second.ID != first.ID || len(authenticationInbox(t, h.founder)) != 1 || x.invocations != 0 {
		t.Fatal("lost control receipt repeated the sign-in notification", first, second)
	}
}
