package work

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestAuthenticationExpiryProjectsLegacyRequestsWithoutRenewal(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	created := int64(1800000000)
	original := []AuthenticationRequest{{FormatVersion: 2, ID: "auth_request_legacy", Provider: "codex", State: "delivery-pending", CreatedAt: created,
		Deliveries: []AuthenticationDelivery{{RecipientID: "agent_founder", OperationID: "legacy-delivery", State: "pending"}}}}
	if err := state.Put(ctx, authenticationKey, original); err != nil {
		t.Fatal(err)
	}
	c := config.DefaultWorker(state.Root)
	engine := New(c, "", nil, state, nil)
	now := created + domain.AuthenticationRequestTTL - 1
	engine.Now = func() time.Time { return time.Unix(now, 0) }
	requests, err := engine.authenticationRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].State != "delivery-pending" || requests[0].ExpiresAt != created+900 {
		t.Fatal("legacy deadline was not derived from its original creation", requests, err)
	}
	now++
	for range 3 {
		request, err := engine.requestAuthentication(ctx, c, "codex", nil)
		if err != nil || request.ID != original[0].ID || request.State != "expired" || request.CreatedAt != created || request.ExpiresAt != created+900 {
			t.Fatal("expiry renewed, extended or attempted to deliver an old request", request, err)
		}
	}
	requests, err = engine.authenticationRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].Deliveries[0].MessageID != "" {
		t.Fatal("expired pending delivery was fabricated", requests, err)
	}
	if err := engine.resolveAuthentication(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	now += 900
	requests, err = engine.authenticationRequests(ctx)
	if err != nil || requests[0].State != "resolved" || requests[0].ResolvedAt == 0 {
		t.Fatal("expiry replaced a real resolution", requests, err)
	}
}

func TestAuthenticationNoticeHasDeadlineAndExplicitFounderRefresh(t *testing.T) {
	request := AuthenticationRequest{ID: "auth_request_fixture", AgentID: "agent_fixture", AgentName: "Fixture", Provider: "copilot", ModeAtRequest: "manual",
		CreatedAt: 1800000000, ExpiresAt: 1800000900, Supersedes: "auth_request_old"}
	body := authenticationMessage(request)
	for _, text := range []string{time.Unix(request.ExpiresAt, 0).UTC().Format(time.RFC3339), "15 minutes", "will not automatically renew", "agent request-sign-in", "Provider device-code expiry", "codex login --device-auth", "copilot login --device-code", "not itself a privacy violation", "secret-bearing", request.Supersedes} {
		if !strings.Contains(body, text) {
			t.Fatal("notice omitted its expiry/refresh boundary", text, body)
		}
	}
	if strings.Contains(body, "No timed expiry") || strings.Contains(body, "%!") {
		t.Fatal("notice has obsolete expiry or invalid formatting", body)
	}
}
