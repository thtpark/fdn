package api

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestPermissionMessageAPIProtectedDeadlinePrivacyAndReplay(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	for _, explicit := range []bool{false, true} {
		name := "default deadline"
		if explicit {
			name = "explicit original deadline"
		}
		t.Run(name, func(t *testing.T) {
			req := domain.MessageRequest{Command: domain.Command{OperationID: domain.NewID("op")}, RecipientID: f.founder.ID, Body: "Nonsecret permission help; this notification grants no access", Origin: "service", Kind: "permission", Urgent: true}
			if explicit {
				req.ExpiresAt = time.Now().Unix() + 300
			}
			envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages", req)
			message := authenticationAPIMessage(t, envelope, status)
			deadline := req.ExpiresAt
			if deadline == 0 {
				deadline = message.CreatedAt + domain.AuthenticationRequestTTL
			}
			if message.Kind != "permission" || message.Origin != "service" || message.SenderID != f.target.ID || message.RecipientID != f.founder.ID || message.ExpiresAt != deadline || message.ResponseRequest || message.AuthenticationRefresh != nil || !message.Urgent {
				t.Fatal("ordinary message route lost protected permission semantics", message)
			}
			for _, actor := range []domain.Agent{f.founder, f.target} {
				envelope, status := f.request(t, actor, http.MethodGet, "/v1/messages/"+message.ID, nil)
				if read := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(read, message) {
					t.Fatal("private party could not read exact permission notification", read)
				}
			}
			envelope, status = f.request(t, f.delegate, http.MethodGet, "/v1/messages/"+message.ID, nil)
			if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied || len(envelope.Data) != 0 {
				t.Fatal("delegation exposed another recipient's private permission notice", status, envelope)
			}
			envelope, status = f.request(t, f.target, http.MethodPost, "/v1/messages", req)
			if replayed := authenticationAPIMessage(t, envelope, status); !reflect.DeepEqual(replayed, message) {
				t.Fatal("permission message retry changed immutable expiry or identity", replayed)
			}
			req.Body = "changed original proposal"
			envelope, status = f.request(t, f.target, http.MethodPost, "/v1/messages", req)
			if status != http.StatusConflict || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
				t.Fatal("permission operation allowed a changed immutable proposal", status, envelope)
			}
		})
	}
}

func TestPermissionMessageAPIRejectsForgedFieldsAndAutomaticResponses(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	for _, item := range []struct {
		name   string
		fields map[string]any
	}{
		{"human origin", map[string]any{"origin": "human"}},
		{"AI origin", map[string]any{"origin": "ai"}},
		{"automatic response", map[string]any{"response_requested": true}},
		{"typed control", map[string]any{"authentication_refresh": map[string]any{"provider": "codex", "reason": "forged control"}}},
		{"null typed control", map[string]any{"authentication_refresh": nil}},
		{"server-only kind", map[string]any{"kind": "authentication-refresh"}},
		{"sender", map[string]any{"sender_id": f.founder.ID}},
		{"creation time", map[string]any{"created_at": 1}},
		{"zero expiry", map[string]any{"expires_at": 0}},
		{"null expiry", map[string]any{"expires_at": nil}},
		{"negative expiry", map[string]any{"expires_at": -1}},
		{"past expiry", map[string]any{"expires_at": time.Now().Unix() - 1}},
		{"long expiry", map[string]any{"expires_at": time.Now().Unix() + domain.AuthenticationRequestTTL + 60}},
		{"string expiry", map[string]any{"expires_at": "timestamp"}},
		{"fractional expiry", map[string]any{"expires_at": 1.5}},
	} {
		t.Run(item.name, func(t *testing.T) {
			payload := map[string]any{"operation_id": domain.NewID("op"), "recipient_id": f.founder.ID, "body": "Nonsecret permission help", "origin": "service", "kind": "permission", "response_requested": false}
			for key, value := range item.fields {
				payload[key] = value
			}
			envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages", payload)
			if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
				t.Fatal("permission message accepted forged control, unprotected response or expiry", status, envelope)
			}
		})
	}
	for _, kind := range []string{"direct", "session"} {
		for _, expiry := range []any{int64(0), nil, time.Now().Unix() + 60} {
			payload := map[string]any{"operation_id": domain.NewID("op"), "recipient_id": f.founder.ID, "body": "Ordinary notice", "origin": "service", "kind": kind, "expires_at": expiry}
			envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages", payload)
			if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
				t.Fatal("permission expiry support changed ordinary direct/session validation", kind, expiry, status, envelope)
			}
		}
	}
}
