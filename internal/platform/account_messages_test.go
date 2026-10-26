package platform

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/transport"
)

func TestBinaryAccountHelperKernelUIDAndGenerationFences(t *testing.T) {
	manager, system := accountFixture(t)
	directory := filepath.Join(filepath.Dir(manager.options.StateDir), "binary")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "s")
	listener, err := ingress.Listen(socket, -1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- transport.ServeConnections(ctx, listener, NewAccountMessageHandler(manager, uint32(os.Geteuid())),
			func(err error) {
				if domain.AsFault(err).Code != domain.Unavailable && domain.AsFault(err).Code != domain.Cancelled {
					t.Errorf("binary account connection failed: %v", err)
				}
			})
	}()
	defer func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("binary account workers leaked")
		}
	}()
	client, err := newMessageAccountClient(socket, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.http != nil || client.messageDial == nil {
		t.Fatal("native account client retained HTTP")
	}
	account, err := client.Provision(ctx, AccountProvisionRequest{AgentID: accountTestAgent,
		PublicKey: accountTestKey(t), Generation: 1})
	if err != nil || account.State != AccountActive {
		t.Fatal("binary provision failed", account, err)
	}
	disabled, err := client.Disable(ctx, account.AgentID, 2)
	if err != nil || disabled.State != AccountDisabled || disabled.UID != account.UID || disabled.Generation != 2 {
		t.Fatal("binary disable lost account/generation", disabled, err)
	}
	mutations, lookups := len(system.mutations), system.lookups
	if _, err := client.Provision(ctx, AccountProvisionRequest{AgentID: account.AgentID, PublicKey: account.PublicKey, Generation: 1}); err == nil {
		t.Fatal("stale binary provision revived an old key")
	}
	if len(system.mutations) != mutations || system.lookups != lookups {
		t.Fatal("stale binary generation reached OS operations")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	session, err := message.NewSession(ctx, conn, nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Call(ctx, message.Request{Operation: "accounts.exec"}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("binary helper exposed a generic root capability", err)
	}
	if _, err := session.Call(ctx, message.Request{Operation: "accounts.disable", Query: map[string]string{"uid": "0"}}); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("binary account metadata overrode kernel identity", err)
	}
}
