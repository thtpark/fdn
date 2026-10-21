package platform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/ingress"
)

func TestAccountHelperKernelUIDFixedOperationsAndClient(t *testing.T) {
	manager, system := accountFixture(t)
	root := filepath.Join(filepath.Dir(manager.options.StateDir), "h")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "s")
	listener, err := ingress.Listen(socket, -1)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewAccountHandler(manager, uint32(os.Geteuid())), ConnContext: ingress.ConnContext, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		select {
		case err := <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("account helper HTTP worker leaked")
		}
	}()
	client, err := newAccountClient(socket, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	account, err := client.Provision(context.Background(), AccountProvisionRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: 1})
	if err != nil || account.State != AccountActive || account.UID != manager.options.UIDMin {
		t.Fatal(account, err)
	}
	disabled, err := client.Disable(context.Background(), account.AgentID, account.Generation+1)
	if err != nil || disabled.UID != account.UID || disabled.State != AccountDisabled || disabled.Generation != account.Generation+1 || !system.disabled[account.Username] {
		t.Fatal(disabled, err)
	}
	mutations, lookups := len(system.mutations), system.lookups
	if _, err := client.Provision(context.Background(), AccountProvisionRequest{AgentID: account.AgentID, PublicKey: account.PublicKey, Generation: account.Generation}); err == nil {
		t.Fatal("typed RPC replay reinstalled a pre-disable SSH key")
	}
	if mutations != len(system.mutations) || lookups != system.lookups {
		t.Fatal("stale typed RPC invoked OS commands")
	}

	handler := NewAccountHandler(manager, uint32(os.Geteuid()))
	request := httptest.NewRequest(http.MethodPost, "http://accounts.local/v1/accounts/provision", strings.NewReader(`{"agent_id":"root","command":"id"}`))
	request.Header.Set("X-Foundation-UID", "0")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatal("HTTP header supplied a peer UID", recorder.Code)
	}

	transport := client.http.Transport.(*http.Transport)
	for _, body := range []string{`{"agent_id":"root","command":"id"}`, `{"agent_id":"` + accountTestAgent + `","public_key":"x","uid":0}`, `{} {}`} {
		request, err := http.NewRequest(http.MethodPost, "http://accounts.local/v1/accounts/provision", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.http.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("unknown field/type accepted", response.StatusCode)
		}
	}
	if conn, err := transport.DialContext(context.Background(), "tcp", "untrusted.example:80"); err == nil {
		conn.Close()
		t.Fatal("account helper dialed another network/origin")
	}
}

func TestAccountHelperRejectsWrongKernelUID(t *testing.T) {
	manager, _ := accountFixture(t)
	root := filepath.Join(filepath.Dir(manager.options.StateDir), "h")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := ingress.Listen(filepath.Join(root, "s"), -1)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewAccountHandler(manager, uint32(os.Geteuid())+1), ConnContext: ingress.ConnContext, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("helper leaked")
		}
	}()
	client, err := newAccountClient(listener.Addr().String(), uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Provision(context.Background(), AccountProvisionRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: 1}); err == nil {
		t.Fatal("another OS UID reached root operations")
	}
}
