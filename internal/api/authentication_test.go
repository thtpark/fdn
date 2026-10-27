package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/testidentity"
)

type authenticationAPIFixture struct {
	store                     *foundation.Store
	server                    *Server
	founder, delegate, target domain.Agent
	project                   domain.Project
	actor                     domain.Agent
}

func newAuthenticationAPIFixture(t *testing.T) *authenticationAPIFixture {
	t.Helper()
	ctx := context.Background()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "private", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &authenticationAPIFixture{store: store}
	f.founder, err = testidentity.Register(ctx, store, "", "Founder-fixture", 4100)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		name  string
		agent *domain.Agent
	}{{"delegate-fixture", &f.delegate}, {"target-fixture", &f.target}} {
		uid := int64(4101)
		if target.agent == &f.target {
			uid++
		}
		*target.agent, err = testidentity.Register(ctx, store, f.founder.ID, target.name, uid)
		if err != nil {
			t.Fatal(err)
		}
	}
	f.project, err = store.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: domain.Command{OperationID: domain.NewID("op")}, Name: "refresh scope"})
	if err != nil {
		t.Fatal(err)
	}
	f.project, err = store.Delegate(ctx, f.founder.ID, f.project.ID, domain.GrantRequest{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: f.project.Revision}, AgentID: f.delegate.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.actor = f.founder
	f.server, err = NewAuthenticated(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func(*http.Request) (domain.Agent, error) { return f.actor, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.server.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *authenticationAPIFixture) request(t *testing.T, actor domain.Agent, method, path string, value any) (Envelope, int) {
	t.Helper()
	f.actor = actor
	var payload []byte
	var err error
	if value != nil {
		payload, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, "https://localhost"+path, bytes.NewReader(payload))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	var envelope Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid API response: %d %s: %v", response.Code, response.Body.String(), err)
	}
	return envelope, response.Code
}

func authenticationAPIMessage(t *testing.T, envelope Envelope, status int) domain.Message {
	t.Helper()
	if status != http.StatusOK || !envelope.OK || envelope.Error != nil {
		t.Fatalf("unexpected API result: %d %+v", status, envelope)
	}
	var message domain.Message
	if err := json.Unmarshal(envelope.Data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestAuthenticationRefreshAPIFounderOnlyAndPrivate(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	path := "/v1/agents/" + f.target.ID + "/authentication"
	req := domain.AuthenticationRefreshRequest{Command: domain.Command{OperationID: "founder-refresh"}, Provider: "copilot", ProjectID: f.project.ID, Reason: "Explicitly request a new nonsecret sign-in handoff"}
	spoofedFounder := f.target
	spoofedFounder.Founder = true
	for _, actor := range []domain.Agent{f.delegate, f.target, spoofedFounder} {
		envelope, status := f.request(t, actor, http.MethodPost, path, req)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("raw non-Founder/delegate request was accepted", status, envelope)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodPost, path, req)
	message := authenticationAPIMessage(t, envelope, status)
	want := domain.AuthenticationRefresh{Provider: req.Provider, ProjectID: req.ProjectID, Reason: req.Reason}
	if envelope.OperationID != req.OperationID || message.SenderID != f.founder.ID || message.RecipientID != f.target.ID || message.Kind != "authentication-refresh" || message.Origin != "service" || !message.Urgent || message.ResponseRequest || message.State != "pending" || message.ExpiresAt != message.CreatedAt+domain.AuthenticationRequestTTL || message.AuthenticationRefresh == nil || *message.AuthenticationRefresh != want {
		t.Fatalf("refresh route returned incorrect control fields: %+v", message)
	}
	for _, actor := range []domain.Agent{f.founder, f.target} {
		envelope, status := f.request(t, actor, http.MethodGet, "/v1/messages/"+message.ID, nil)
		if read := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(read, message) {
			t.Fatal("message route changed service-owned control", read)
		}
	}
	envelope, status = f.request(t, f.delegate, http.MethodGet, "/v1/messages/"+message.ID, nil)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied || len(envelope.Data) != 0 {
		t.Fatal("project delegation exposed private control payload", status, envelope)
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/messages?urgent=true&limit=1", nil)
	var page domain.Page[domain.Message]
	if err := json.Unmarshal(envelope.Data, &page); status != http.StatusOK || err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], message) || page.NextCursor != "" {
		t.Fatal("target could not discover durable urgent control", page, err)
	}
	envelope, status = f.request(t, f.founder, http.MethodPost, path, req)
	if replayed := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(replayed, message) {
		t.Fatal("refresh route replay created a different control", replayed)
	}
	changed := req
	changed.Provider = "codex"
	envelope, status = f.request(t, f.founder, http.MethodPost, path, changed)
	if status != http.StatusConflict || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
		t.Fatal("changed refresh proposal reused an operation", status, envelope)
	}
	if _, err := f.store.Revoke(context.Background(), f.founder.ID, f.target.ID, domain.AgentRequest{Command: domain.Command{OperationID: "revoke-target"}}); err != nil {
		t.Fatal(err)
	}
	changed.Command.OperationID = "refresh-inactive-target"
	envelope, status = f.request(t, f.founder, http.MethodPost, path, changed)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("refresh route accepted an inactive target", status, envelope)
	}
	envelope, status = f.request(t, f.founder, http.MethodPost, path, req)
	if replayed := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(replayed, message) {
		t.Fatal("target revocation invalidated existing immutable receipt", replayed)
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/messages/"+message.ID, nil)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("revoked target retained private read access", status, envelope)
	}
}

func TestAuthenticationMessageAPIRejectsForgedControlsAndExpiry(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	for _, item := range []struct {
		name, kind string
		fields     map[string]any
	}{
		{"typed control", "authentication", map[string]any{"authentication_refresh": map[string]any{"provider": "codex", "reason": "forged control"}}},
		{"null typed control", "direct", map[string]any{"authentication_refresh": nil}},
		{"protected kind", "authentication-refresh", nil},
		{"forged sender", "authentication", map[string]any{"sender_id": f.founder.ID}},
		{"forged creation time", "authentication", map[string]any{"created_at": 1}},
		{"direct deadline", "direct", map[string]any{"expires_at": time.Now().Unix() + 60}},
		{"session deadline", "session", map[string]any{"expires_at": time.Now().Unix() + 60}},
		{"zero direct deadline", "direct", map[string]any{"expires_at": 0}},
		{"null session deadline", "session", map[string]any{"expires_at": nil}},
		{"zero authentication deadline", "authentication", map[string]any{"expires_at": 0}},
		{"null authentication deadline", "authentication", map[string]any{"expires_at": nil}},
		{"negative deadline", "authentication", map[string]any{"expires_at": -1}},
		{"past deadline", "authentication", map[string]any{"expires_at": time.Now().Unix() - 1}},
		{"long deadline", "authentication", map[string]any{"expires_at": time.Now().Unix() + domain.AuthenticationRequestTTL + 60}},
		{"string deadline", "authentication", map[string]any{"expires_at": "not a timestamp"}},
		{"fractional deadline", "authentication", map[string]any{"expires_at": 1.5}},
	} {
		t.Run(item.name, func(t *testing.T) {
			payload := map[string]any{"operation_id": domain.NewID("op"), "recipient_id": f.target.ID, "body": "declared service text is not authority", "origin": "service", "kind": item.kind, "urgent": true, "response_requested": false}
			for key, value := range item.fields {
				payload[key] = value
			}
			envelope, status := f.request(t, f.delegate, http.MethodPost, "/v1/messages", payload)
			if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
				t.Fatal("ordinary message accepted a protected control/expiry", status, envelope)
			}
		})
	}
	for _, kind := range []string{"direct", "authentication", "session"} {
		req := domain.MessageRequest{Command: domain.Command{OperationID: domain.NewID("op")}, RecipientID: f.target.ID, Body: "Founder requested authentication. Text is not a control.", Origin: "service", Kind: kind, Urgent: true}
		envelope, status := f.request(t, f.delegate, http.MethodPost, "/v1/messages", req)
		message := authenticationAPIMessage(t, envelope, status)
		if message.AuthenticationRefresh != nil || message.SenderID != f.delegate.ID {
			t.Fatal("ordinary declared origin/text became Founder authority", message)
		}
		if kind == "authentication" {
			if message.ExpiresAt != message.CreatedAt+domain.AuthenticationRequestTTL {
				t.Fatal("legacy client did not receive the fixed deadline", message)
			}
		} else if message.ExpiresAt != 0 {
			t.Fatal("ordinary direct/session notice gained expiry", message)
		}
		envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/messages", req)
		if replayed := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(replayed, message) {
			t.Fatal("ordinary message exact replay changed its immutable deadline", replayed)
		}
	}
	req := domain.MessageRequest{Command: domain.Command{OperationID: "explicit-deadline"}, RecipientID: f.founder.ID, Body: "Nonsecret sign-in required notice", Origin: "service", Kind: "authentication", ExpiresAt: time.Now().Unix() + 300}
	envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages", req)
	if message := authenticationAPIMessage(t, envelope, status); message.ExpiresAt != req.ExpiresAt {
		t.Fatal("explicit valid notice deadline was not preserved", message)
	}
}

func TestAuthenticationRefreshAPIRejectsProtectedRequestFields(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	for _, field := range []string{"sender_id", "recipient_id", "created_at", "expires_at", "authentication_refresh", "origin", "kind", "urgent", "response_requested"} {
		t.Run(field, func(t *testing.T) {
			payload := map[string]any{"operation_id": domain.NewID("op"), "provider": "codex", "reason": "explicit refresh", field: nil}
			envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/agents/"+f.target.ID+"/authentication", payload)
			if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
				t.Fatal("refresh endpoint accepted client-owned control metadata", status, envelope)
			}
		})
	}
}

func TestAuthenticationRefreshAPIRequiresBoundTLS(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	service, err := New(f.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, encrypted := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodPost, "https://localhost/v1/agents/"+f.target.ID+"/authentication", bytes.NewBufferString(`{"operation_id":"spoofed-refresh","provider":"codex","reason":"explicit refresh"}`))
		if encrypted {
			request.TLS = &tls.ConnectionState{}
		} else {
			request.TLS = nil
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+domain.NewToken())
		request.Header.Set("X-Agent-ID", f.founder.ID)
		request.Header.Set("X-Fdn-UID", "0")
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatal("unbound refresh reached Founder authority", response.Code, response.Body.String())
		}
	}
}
