package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
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
	var gotBearer bool
	var mu sync.Mutex
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotBearer = gotBearer || r.Header.Get("Authorization") != ""
		mu.Unlock()
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		status, _ := json.Marshal(LocalStatus{Agent: domain.Agent{ID: "agent_local"}, Connected: true})
		_ = json.NewEncoder(w).Encode(Envelope{OK: true, Data: status, Warnings: []string{}})
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { server.Close(); <-done }()
	client, err := NewLocalClient(config.Worker{IdentitySocket: socket, CredentialFile: filepath.Join(root, "does-not-exist"), URL: "https://127.0.0.1:1"})
	if err != nil || client.Credential.Token != "" || !client.Local {
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
	mu.Lock()
	defer mu.Unlock()
	if gotBearer {
		t.Fatal("local client sent a bearer credential")
	}
	if _, err := NewLocalClient(config.Worker{IdentitySocket: "relative.sock"}); err == nil {
		t.Fatal("relative socket was accepted")
	}
	if response, err := client.HTTP.Get("http://other-origin/local/status"); err == nil {
		response.Body.Close()
		t.Fatal("local client dialed an origin other than its fixed Unix service")
	}
}
