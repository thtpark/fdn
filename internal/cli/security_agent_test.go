package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func identityAttachmentFixture(t *testing.T, actualID, statusID string, connected bool) (config.Worker, string) {
	t.Helper()
	root, err := os.MkdirTemp(".", ".identity-attachment-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "i.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("runner forwarded a legacy bearer credential")
		}
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		var value any
		switch r.URL.Path {
		case "/local/status":
			value = map[string]any{"agent": domain.Agent{ID: statusID, Mode: "ai", Active: true}, "identity": domain.AgentIdentity{AgentID: statusID, State: "active"}, "connected": connected}
		case "/v1/me":
			value = domain.Agent{ID: actualID, Mode: "ai", Active: true}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, _ := json.Marshal(value)
		json.NewEncoder(w).Encode(api.Envelope{OK: true, Data: data, Warnings: []string{}})
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
	worker := config.DefaultWorker(filepath.Join(root, "state"))
	worker.IdentitySocket = socket
	path := filepath.Join(root, "config", "agent.json")
	if err := config.Write(path, worker); err != nil {
		t.Fatal(err)
	}
	return worker, path
}

func attach(t *testing.T, path string, flags ...string) (api.Envelope, int) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	args := append([]string{"--config", path, "connect"}, flags...)
	code := RunAgent(context.Background(), args, bytes.NewReader(nil), &out, &diagnostic)
	var reply api.Envelope
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatalf("invalid output %s %s: %v", out.String(), diagnostic.String(), err)
	}
	return reply, code
}

func TestConnectAttachesPublicIdentityWithoutCopyingCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := domain.NewID("agent")
	c, path := identityAttachmentFixture(t, id, id, true)
	for range 2 {
		response, code := attach(t, path)
		if code != 0 || !response.OK {
			t.Fatalf("attachment failed: %+v", response)
		}
	}
	var credential config.Credential
	if err := config.Read(c.CredentialFile, &credential); err != nil {
		t.Fatal(err)
	}
	if credential.AgentID != id || credential.Token != "" {
		t.Fatalf("runner received a secret or wrong identity: %+v", credential)
	}
	state, err := local.Open(context.Background(), c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.BindIdentity(context.Background(), domain.NewID("agent")); err == nil {
		t.Fatal("attached state accepted another identity")
	}
}

func TestConnectRequiresMatchingLiveIdentityAndExplicitLegacyMigration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := domain.NewID("agent")
	for _, item := range []struct {
		name, actual, status string
		connected            bool
	}{
		{"not-admitted", id, id, false},
		{"inconsistent-service", id, domain.NewID("agent"), true},
	} {
		t.Run(item.name, func(t *testing.T) {
			c, path := identityAttachmentFixture(t, item.actual, item.status, item.connected)
			reply, code := attach(t, path)
			if code == 0 || reply.OK {
				t.Fatal("unverified local service accepted")
			}
			if _, err := os.Stat(c.CredentialFile); !os.IsNotExist(err) {
				t.Fatal("failed attachment persisted credentials")
			}
		})
	}
	c, path := identityAttachmentFixture(t, id, id, true)
	token := domain.NewToken()
	if err := config.Write(c.CredentialFile, config.Credential{AgentID: id, Token: token}); err != nil {
		t.Fatal(err)
	}
	if reply, code := attach(t, path); code == 0 || reply.OK {
		t.Fatal("legacy secret silently migrated")
	}
	var unchanged config.Credential
	if err := config.Read(c.CredentialFile, &unchanged); err != nil || unchanged.Token != token {
		t.Fatal("failed migration changed retained evidence", err)
	}
	if reply, code := attach(t, path, "--migrate"); code != 0 || !reply.OK {
		t.Fatalf("same-identity approved migration failed: %+v", reply)
	}
	unchanged = config.Credential{}
	if err := config.Read(c.CredentialFile, &unchanged); err != nil || unchanged.Token != "" {
		t.Fatal("migration retained a bearer secret", err)
	}
}
