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

func TestAuthenticationAndPermissionHelpNeverSendToTheRequestingActor(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	f.project.Delegates = append(f.project.Delegates, f.engine.Client.Credential.AgentID)
	request := AuthenticationRequest{AgentID: f.engine.Client.Credential.AgentID, ProjectID: f.project.ID}
	recipients, err := f.engine.authenticationRecipients(ctx, &request)
	if err != nil {
		t.Fatal(err)
	}
	for _, recipient := range recipients {
		if recipient == request.AgentID {
			t.Fatal("self-delegation created a self help message", recipients)
		}
	}
	request.AgentID, request.ProjectID = f.agents[0].ID, ""
	if _, err := f.engine.authenticationRecipients(ctx, &request); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("an unavailable external recipient was reported as delivered self-help", err)
	}
	if len(f.attempts) != 0 {
		t.Fatal("recipient discovery sent messages", f.attempts)
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

func TestAuthenticationReauthenticationNoticeAndResolutionSemantics(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	created := int64(1800000000)
	reauth := AuthenticationRequest{
		FormatVersion:          authenticationFormat,
		ID:                     "auth_request_reauth",
		AgentID:                "agent_worker",
		AgentName:              "Agent2",
		Provider:               "antigravity",
		ModeAtRequest:          "ai",
		ObservedAuthentication: "authenticated",
		RefreshID:              "message_refresh_antigravity",
		State:                  "requested",
		CreatedAt:              created,
		ExpiresAt:              created + domain.AuthenticationRequestTTL,
	}
	body := authenticationMessage(reauth)
	for _, text := range []string{
		"Provider sign-in requested by Founder (currently authenticated).",
		"account, or API key can be replaced",
		"Existing work is not blocked until a session is opened.",
		"Antigravity GEMINI_API_KEY / Google OAuth",
		"assistance open GRANT --provider antigravity",
	} {
		if !strings.Contains(body, text) {
			t.Fatal("re-authentication notice omitted expected guidance", text, body)
		}
	}
	if strings.Contains(body, "Authentication is blocking this computer") {
		t.Fatal("re-authentication notice falsely reported an active sign-out block", body)
	}

	if err := state.Put(ctx, authenticationKey, []AuthenticationRequest{reauth}); err != nil {
		t.Fatal(err)
	}
	c := config.DefaultWorker(state.Root)
	engine := New(c, "", nil, state, nil)
	now := created + 60
	engine.Now = func() time.Time { return time.Unix(now, 0) }
	if err := engine.resolveAuthentication(ctx, "antigravity"); err != nil {
		t.Fatal(err)
	}
	requests, err := engine.authenticationRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].State != "requested" {
		t.Fatal("routine readiness check prematurely resolved an active Founder re-sign-in request", requests, err)
	}

	// After the voluntary re-sign-in notice expires, a subsequent real sign-out must still issue a new automatic request.
	now = created + domain.AuthenticationRequestTTL + 10
	requests, err = engine.authenticationRequests(ctx)
	if err != nil || requests[0].State != "expired" {
		t.Fatal("re-authentication request did not expire at its deadline", requests, err)
	}
	f := newPermissionFixture(t)
	if err := f.engine.State.Put(ctx, authenticationKey, []AuthenticationRequest{reauth}); err != nil {
		t.Fatal(err)
	}
	f.engine.Now = func() time.Time { return time.Unix(now, 0) }
	_, _ = f.engine.requestAuthentication(ctx, c, "antigravity", nil)
	afterSignOut, err := f.engine.authenticationRequests(ctx)
	if err != nil || len(afterSignOut) != 1 || afterSignOut[0].ID == reauth.ID || afterSignOut[0].Supersedes != reauth.ID || afterSignOut[0].RefreshID != "" {
		t.Fatal("expired voluntary re-sign-in request suppressed a subsequent real sign-out request", afterSignOut, err)
	}
}
