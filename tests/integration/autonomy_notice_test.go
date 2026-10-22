//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/work"
)

func TestLegacySignInInstructionsRequireFounderRefreshWithoutRewritingOldOperations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	old, err := api.Call[domain.Message](ctx, h.worker, "POST", "/v1/messages", domain.MessageRequest{Command: domain.Command{OperationID: "old-auth-operation"},
		RecipientID: h.founderAgent.ID, Origin: "service", Kind: "authentication", Body: "Older instructions expected an operator at the target."})
	if err != nil {
		t.Fatal(err)
	}
	previous := work.AuthenticationRequest{ID: "auth_request_legacy", AgentID: h.workerAgent.ID, AgentName: h.workerAgent.Name, Provider: "codex", ModeAtRequest: "ai", State: "requested", CreatedAt: 1,
		Deliveries: []work.AuthenticationDelivery{{RecipientID: h.founderAgent.ID, OperationID: "old-auth-operation", MessageID: old.ID, State: "delivered"}}}
	if err := engine.State.Put(ctx, "authentication-requests", []work.AuthenticationRequest{previous}); err != nil {
		t.Fatal(err)
	}
	expired := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	if expired.ID != previous.ID || expired.State != "expired" || expired.ExpiresAt != previous.CreatedAt+900 {
		t.Fatal("legacy request was renewed instead of expiring from its original timestamp", expired)
	}
	founderAuthenticationRefresh(t, h, "codex", "")
	first := requireAuthenticationRefresh(t, engine).AuthenticationRequests[0]
	if first.ID == previous.ID || first.Supersedes != previous.ID || first.FormatVersion != 3 {
		t.Fatal("old human-target workflow was not superseded explicitly", first)
	}
	for range 2 {
		next := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
		if next.ID != first.ID {
			t.Fatal("instruction migration spammed new requests")
		}
	}
	messages := authenticationInbox(t, h.founder)
	if len(messages) != 2 || x.invocations != 0 {
		t.Fatal("legacy notice migration duplicated inference or notification", messages, x.invocations)
	}
	for _, message := range messages {
		if message.ID == old.ID {
			if message.Body != old.Body {
				t.Fatal("immutable original request was rewritten")
			}
		} else if !strings.Contains(message.Body, previous.ID) || !strings.Contains(message.Body, "no local person or repeated target consent") {
			t.Fatal("replacement omitted its supersession/autonomy boundary", message)
		}
	}
}
