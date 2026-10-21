//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
)

// workflowAPI is a business-workflow fixture, not a standalone Foundation server.
// Its same-UID proxies do not establish production account/key isolation.
// Only this test process holds the fixture bearer values; workers receive no keys.
type workflowAPI struct {
	store   *foundation.Store
	service *api.Server
	server  *httptest.Server
	root    string
}

func newWorkflowAPI(t *testing.T, root string) *workflowAPI {
	t.Helper()
	store, err := foundation.Open(context.Background(), filepath.Join(root, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(root); err != nil {
		t.Fatal(err)
	}
	service, err := api.NewAuthenticated(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func(r *http.Request) (domain.Agent, error) {
		return store.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(service.Handler())
	server.TLS, err = pki.Server(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	fixture := &workflowAPI{store: store, service: service, server: server, root: root}
	t.Cleanup(func() {
		service.Close()
		server.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (f *workflowAPI) seed(t *testing.T, founderID, name string) (domain.Agent, string) {
	t.Helper()
	ctx := context.Background()
	invitation, token := domain.NewToken(), domain.NewToken()
	if founderID == "" {
		if _, err := f.store.Bootstrap(ctx, domain.Hash(invitation), time.Now().Unix()+600); err != nil {
			t.Fatal(err)
		}
	} else if _, err := f.store.Invite(ctx, founderID, domain.InvitationRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, TokenHash: domain.Hash(invitation), ExpiresAt: time.Now().Unix() + 600,
	}); err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.Enroll(ctx, domain.EnrollRequest{Command: domain.Command{OperationID: domain.NewID("op")},
		Name: name, InvitationToken: invitation, CredentialHash: domain.Hash(token)})
	if err != nil {
		t.Fatal(err)
	}
	return agent, token
}

func (f *workflowAPI) localProxy(t *testing.T, socket string, agent domain.Agent, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := api.NewClient(f.server.URL, filepath.Join(f.root, "ca.pem"), "", config.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = client.HTTP.Transport
	proxy.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = upstream.Host
		r.Header.Set("Authorization", "Bearer "+token)
	}
	identity := domain.AgentIdentity{AgentID: agent.ID, State: "active", Assurance: "test-fixture-only"}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local/status" && r.URL.Path != "/local/profile" {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		w.Header().Set("Content-Type", "application/json")
		var data any = api.LocalStatus{Agent: agent, Identity: identity, Connected: true, Ready: true}
		if r.URL.Path == "/local/profile" {
			data = config.ConnectionProfile{Identity: identity, URL: f.server.URL}
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(api.Envelope{OK: true, Data: encoded, Warnings: []string{}})
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		client.Close()
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	})
}
