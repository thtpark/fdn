//go:build e2e

package e2e_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/testidentity"
	"github.com/thtpark/fdn/internal/transport"
)

// workflowAPI is a business-workflow fixture, not a standalone Foundation server.
// Its same-UID proxies do not establish production account/key isolation.
// Only this test process injects business actors; workers receive no keys.
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
		return testidentity.Authenticate(store, r)
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

func (f *workflowAPI) seed(t *testing.T, founderID, name string) domain.Agent {
	t.Helper()
	ctx := context.Background()
	agents := 0
	if founderID != "" {
		current, err := f.store.Agents(ctx, founderID)
		if err != nil {
			t.Fatal(err)
		}
		agents = len(current)
	}
	agent, err := testidentity.Register(ctx, f.store, founderID, name, int64(4100+agents))
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func (f *workflowAPI) localProxy(t *testing.T, socket string, agent domain.Agent) {
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
	ctx, cancel := context.WithCancel(context.Background())
	var peers sync.WaitGroup
	client := api.NewMessageClient("fixture://foundation", func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		peers.Go(func() {
			defer left.Close()
			_ = f.service.ServeTrustedMessages(ctx, left, agent.ID)
		})
		return right, nil
	})
	identity := domain.AgentIdentity{AgentID: agent.ID, State: "active", Assurance: "test-fixture-only"}
	done := make(chan error, 1)
	go func() {
		done <- transport.ServeConnections(ctx, listener, func(ctx context.Context, conn net.Conn) error {
			session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
				switch request.Operation {
				case "query.local.status":
					return message.MarshalPayload(api.LocalStatus{Agent: agent, Identity: identity, Connected: true, Ready: true})
				case "query.local.profile":
					return message.MarshalPayload(config.ConnectionProfile{Identity: identity, URL: f.server.URL})
				}
				if err := api.ValidateOrdinaryMessage(request); err != nil {
					return nil, err
				}
				result, err := client.Message(call, request)
				return result, message.PublicError(err)
			})
			if err != nil {
				return err
			}
			defer session.Close()
			<-session.Done()
			return nil
		}, func(err error) {
			slog.Warn("binary workflow fixture connection ended", "code", domain.AsFault(err).Code)
		})
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		client.Close()
		peers.Wait()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}
