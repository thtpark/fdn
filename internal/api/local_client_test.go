package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/transport"
)

func TestLocalClientNeedsOnlyUnixSocketAndNeverLoadsBearer(t *testing.T) {
	root, err := os.MkdirTemp(filepath.Join("..", ".."), ".local-client-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- transport.ServeConnections(ctx, listener, func(ctx context.Context, conn net.Conn) error {
			session, err := message.NewSession(ctx, conn, func(_ context.Context, request message.Request) ([]byte, error) {
				if request.Operation != "query.local.status" || request.Target != "" || len(request.Query) != 0 || len(request.Payload) != 0 {
					return nil, domain.Fail(domain.Denied, "only the exact public status query is available")
				}
				return message.MarshalPayload(LocalStatus{Agent: domain.Agent{ID: "agent_local"}, Connected: true})
			})
			if err != nil {
				return err
			}
			defer session.Close()
			<-session.Done()
			return nil
		}, func(err error) { t.Errorf("binary fixture failed: %v", err) })
	}()
	defer func() { cancel(); listener.Close(); <-done }()
	client, err := NewLocalClient(config.Worker{IdentitySocket: socket, CredentialFile: filepath.Join(root, "does-not-exist"), URL: "https://127.0.0.1:1"})
	if err != nil || !client.Local || !client.IsMessageClient() || client.HTTP != nil {
		t.Fatal(client, err)
	}
	defer client.Close()
	status, err := client.LocalStatus(context.Background())
	if err != nil || !status.Connected || status.Agent.ID != "agent_local" {
		t.Fatal(status, err)
	}
	if err := client.Request(context.Background(), http.MethodGet, "/local/status", nil, &status); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalClient(config.Worker{IdentitySocket: "relative.sock"}); err == nil {
		t.Fatal("relative socket was accepted")
	}
	if err := client.Request(context.Background(), "GET", "http://other-origin/local/status", nil, &status); err == nil {
		t.Fatal("local client dialed an origin other than its fixed Unix service")
	}
}
