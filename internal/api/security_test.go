package api

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/testidentity"
)

func TestDefaultAPIRejectsBearerAndSpoofedIdentityWithoutKernelPeer(t *testing.T) {
	ctx := context.Background()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "server", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := domain.NewToken()
	actor, err := testidentity.Register(ctx, store, "", "fixture-Founder", 4100)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, path := range []string{"/healthz", "/v1/me", "/v1/agents", "/v1/enroll", "/v1/invitations"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://localhost"+path, nil)
			request.TLS = &tls.ConnectionState{}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Agent-ID", actor.ID)
			request.Header.Set("X-Fdn-UID", "0")
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("unbound request accepted: %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("X-Fdn-API-Version") != strconv.Itoa(domain.APIVersion) {
				t.Fatal("missing protocol version")
			}
		})
	}
}

func TestBootstrapAndMaintenanceCannotUseSpoofedUIDOrAccessBusinessRoutes(t *testing.T) {
	store, err := foundation.Open(context.Background(), filepath.Join(t.TempDir(), "server", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, item := range []struct {
		name, path string
		handler    http.Handler
	}{
		{"challenge", "/v1/identity/challenge", service.BootstrapHandler()},
		{"bootstrap-business", "/v1/tasks", service.BootstrapHandler()},
		{"root-recovery", "/v1/identity/recover-founder", service.MaintenanceHandler()},
	} {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://localhost"+item.path, nil)
			request.TLS = &tls.ConnectionState{}
			request.Header.Set("X-Fdn-UID", "0")
			response := httptest.NewRecorder()
			item.handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("claimed UID accepted: %d %s", response.Code, response.Body.String())
			}
		})
	}
	if _, err := NewAuthenticated(store, nil, nil); err == nil {
		t.Fatal("nil trusted authenticator became a permissive fallback")
	}
}
