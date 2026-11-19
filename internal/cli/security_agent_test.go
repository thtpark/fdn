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
	"github.com/thtpark/fdn/internal/apitest"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/message"
)

func identityAttachmentFixture(t *testing.T, actualID, statusID string, connected bool) (config.Worker, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fdn-identity-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "i.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &apitest.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case "/v1/message-requests":
			value = []domain.Message{}
		case "/v1/software-requests":
			value = []domain.SoftwareDeployment{}
		case "/v1/upgrades/lease":
			value = domain.UpgradeLease{}
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

func TestNativeTransportDispatchLeavesRunnerStateUntouched(t *testing.T) {
	for _, command := range []struct {
		name  string
		input any
	}{
		{"message-worker", domain.MessageWorkerCommand{}},
		{"software-mailbox", domain.SoftwareMailboxRequest{Action: "inbox"}},
		{"upgrade-lease", domain.UpgradeLeaseRequest{}},
	} {
		for _, failure := range []string{"", "foreign-subject", "missing-credential", "transport", "request"} {
			for _, existing := range []bool{false, true} {
				t.Run(command.name+"/"+failure+"/"+strconv.FormatBool(existing), func(t *testing.T) {
					id := domain.NewID("agent")
					actual := id
					if failure == "foreign-subject" {
						actual = domain.NewID("agent")
					}
					c, path := identityAttachmentFixture(t, actual, actual, true)
					c.CredentialFile = filepath.Join(filepath.Dir(path), "credential.json")
					if err := config.Write(path, c); err != nil {
						t.Fatal(err)
					}
					if err := config.Write(c.CredentialFile, config.Credential{AgentID: id}); err != nil {
						t.Fatal(err)
					}
					if failure == "missing-credential" {
						if err := os.Remove(c.CredentialFile); err != nil {
							t.Fatal(err)
						}
					}
					if failure == "transport" {
						c.IdentitySocket = filepath.Join(filepath.Dir(c.IdentitySocket), "missing.sock")
						if err := config.Write(path, c); err != nil {
							t.Fatal(err)
						}
					}
					var before []byte
					var beforeInfo os.FileInfo
					var databaseInfo os.FileInfo
					database := filepath.Join(c.StateDir, "local.sqlite")
					if existing {
						state, err := local.Open(context.Background(), c.StateDir)
						if err != nil {
							t.Fatal(err)
						}
						if err := state.Close(); err != nil {
							t.Fatal(err)
						}
						before, err = os.ReadFile(database)
						if err != nil {
							t.Fatal(err)
						}
						beforeInfo, err = os.Stat(c.StateDir)
						if err != nil {
							t.Fatal(err)
						}
						databaseInfo, err = os.Stat(database)
						if err != nil {
							t.Fatal(err)
						}
					}
					input, err := message.MarshalPayload(command.input)
					if err != nil {
						t.Fatal(err)
					}
					if failure == "request" {
						input = []byte{0xff}
					}
					var out, diagnostic bytes.Buffer
					code := RunAgent(context.Background(), []string{"--config", path, command.name},
						bytes.NewReader(input), &out, &diagnostic)
					if (code == 0) != (failure == "") {
						t.Fatalf("unexpected result %d: %s %s", code, &out, &diagnostic)
					}
					if !existing {
						if _, err := os.Stat(c.StateDir); !os.IsNotExist(err) {
							t.Fatal("native dispatcher created runner state", err)
						}
						return
					}
					after, err := os.ReadFile(database)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("native dispatcher modified the stopped database", err)
					}
					afterDatabaseInfo, err := os.Stat(database)
					if err != nil || !databaseInfo.ModTime().Equal(afterDatabaseInfo.ModTime()) ||
						!os.SameFile(databaseInfo, afterDatabaseInfo) {
						t.Fatal("native dispatcher touched or replaced the stopped database", err)
					}
					afterInfo, err := os.Stat(c.StateDir)
					if err != nil || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
						t.Fatal("native dispatcher created/deleted runner files such as WAL/SHM", err)
					}
				})
			}
		}
	}
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
	if credential.AgentID != id {
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

func TestConnectRequiresMatchingIdentityAndRejectsRemovedCredentials(t *testing.T) {
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
	old := struct {
		AgentID string `json:"agent_id"`
		Token   string `json:"token"`
	}{AgentID: id, Token: token}
	if err := config.Write(c.CredentialFile, old); err != nil {
		t.Fatal(err)
	}
	if reply, code := attach(t, path); code == 0 || reply.OK {
		t.Fatal("legacy secret silently migrated")
	}
	var unchanged struct {
		AgentID string `json:"agent_id"`
		Token   string `json:"token"`
	}
	if err := config.Read(c.CredentialFile, &unchanged); err != nil || unchanged.Token != token {
		t.Fatal("failed migration changed retained evidence", err)
	}
	if reply, code := attach(t, path, "--migrate"); code == 0 || reply.OK {
		t.Fatalf("removed migration flag was accepted: %+v", reply)
	}
	if err := config.Read(c.CredentialFile, &unchanged); err != nil || unchanged.Token != token {
		t.Fatal("rejected migration changed retained input", err)
	}
}
