package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
)

func TestProvisioningRejectsRetiredIntentAndSkipsActiveOrBannedIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "server", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	public, _, authPEM, err := device.Generate("provision fixture")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := store.BootstrapIdentity(ctx, public, "provision-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	calls := 0
	service.identity.options.Provision = func(_ context.Context, identity domain.AgentIdentity) (domain.AgentIdentity, error) {
		calls++
		return identity, nil
	}
	if _, err := service.provisionIdentity(ctx, registered); err != nil || calls != 1 {
		t.Fatal("pending registration not provisioned", err)
	}
	for _, mutate := range []func(*domain.AgentIdentity){
		func(i *domain.AgentIdentity) { i.Generation-- },
		func(i *domain.AgentIdentity) { i.RequestID = "retired-request" },
		func(i *domain.AgentIdentity) { i.SSHFingerprint = "retired-key" },
		func(i *domain.AgentIdentity) { i.AuthFingerprint = "retired-authentication-key" },
	} {
		stale := registered
		mutate(&stale)
		if _, err := service.provisionIdentity(ctx, stale); err == nil {
			t.Fatal("retired intent reached OS provisioner")
		}
	}
	if calls != 1 {
		t.Fatal("stale receipt changed managed accounts")
	}
	bound, err := store.BindIdentityAccount(ctx, registered.AgentID, registered.SSHUser, 24001, registered.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != "provisioned" {
		t.Fatalf("unexpected native binding state: %s", bound.State)
	}
	if current, err := service.provisionIdentity(ctx, registered); err != nil || current.State != "provisioned" || current.AgentID != bound.AgentID || current.UID != bound.UID || calls != 2 {
		t.Fatal("lost-response retry rejected the retained provisioned identity", current, err)
	}
	challenge, err := store.ChallengeIdentity(ctx, bound.UID)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := device.ParseSigner(authPEM)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(signer, challenge)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ActivateIdentity(ctx, bound.UID, domain.ActivateIdentityRequest{Command: domain.Command{OperationID: "provision-activate"}, Nonce: challenge.Nonce, Proof: proof})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := service.provisionIdentity(ctx, registered); err != nil || current.State != "active" || calls != 2 {
		t.Fatal("registration replay reprovisioned an active identity", current, err)
	}
	if err := store.BanIdentity(ctx, active.AgentID, "fixture explicit security hold"); err != nil {
		t.Fatal(err)
	}
	banned, err := store.Identity(ctx, active.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.provisionIdentity(ctx, banned); err == nil || calls != 2 {
		t.Fatal("banned identity reached OS provisioner")
	}
}

func TestFounderCannotInvalidateOwnResponseChannelWithOnlineKeyRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "server", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewAuthenticated(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func(*http.Request) (domain.Agent, error) {
		return domain.Agent{ID: "agent_founder_fixture", Founder: true, Active: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	request := httptest.NewRequest(http.MethodPost, "https://localhost/v1/identity/agent_founder_fixture/recover", strings.NewReader(`{"operation_id":"self-recovery"}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	var envelope Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Prerequisite || !strings.Contains(envelope.Error.Message, "local OS-operator") {
		t.Fatalf("unsafe self-replacement admitted: %+v", envelope)
	}
}
