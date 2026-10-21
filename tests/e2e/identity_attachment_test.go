//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestNativeWorkerUsesOnlyLocalIdentityAndPreservesMigrationState(t *testing.T) {
	l := newLab(t, "codex")
	w := l.worker
	ctx := context.Background()
	for range 2 {
		actor := must[domain.Agent](t, "agent-cli", []string{"--config", w.configFile, "connect"}, nil)
		if actor.ID != w.agent.ID {
			t.Fatal("reconnect changed the local public identity")
		}
	}
	credentialBytes, err := os.ReadFile(w.config.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var credentialFields map[string]json.RawMessage
	if err := json.Unmarshal(credentialBytes, &credentialFields); err != nil || len(credentialFields) != 1 || credentialFields["agent_id"] == nil {
		t.Fatal("native worker persisted a secret instead of just its actor ID", err)
	}
	if info, err := os.Stat(w.config.CredentialFile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("local public identity binding is not owner-restricted", err)
	}
	if err := filepath.WalkDir(filepath.Dir(w.config.StateDir), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Type()&os.ModeSocket != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil && (bytes.Contains(data, []byte("PRIVATE KEY-----")) || bytes.Contains(data, []byte(w.fixtureToken))) {
			t.Fatal("worker-owned files contain a private identity key or fixture bearer credential")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// These deliberately unusable remote settings must never become a TCP fallback.
	workerConfig := w.config
	workerConfig.URL = "https://127.0.0.1:1"
	workerConfig.CAFile = filepath.Join(l.root, "absent-remote-trust.pem")
	if err := config.Write(w.configFile, workerConfig); err != nil {
		t.Fatal(err)
	}
	must[domain.Agent](t, "agent-cli", []string{"--config", w.configFile, "connect"}, nil)
	must[domain.Page[domain.Task]](t, "agent-cli", []string{"--config", w.configFile, "task", "list"}, nil)
	state, err := local.Open(ctx, w.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, "identity-migration-checkpoint", map[string]string{"checkpoint": "retain existing uncertainty"}); err != nil {
		t.Fatal(err)
	}
	state.Close()
	legacy := config.Credential{AgentID: w.agent.ID, Token: domain.NewToken()}
	if err := config.Write(w.config.CredentialFile, legacy); err != nil {
		t.Fatal(err)
	}
	reply, code, diagnostic := run(t, "agent-cli", []string{"--config", w.configFile, "connect"}, nil)
	if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Prerequisite {
		t.Fatal("legacy bearer silently migrated without the explicit switch", reply.Error)
	}
	if strings.Contains(diagnostic, legacy.Token) || strings.Contains(string(reply.Data), legacy.Token) {
		t.Fatal("migration handoff exposed the retained legacy credential")
	}
	var unchanged config.Credential
	if err := config.Read(w.config.CredentialFile, &unchanged); err != nil || unchanged != legacy {
		t.Fatal("failed migration destroyed retained evidence", err)
	}
	must[domain.Agent](t, "agent-cli", []string{"--config", w.configFile, "connect", "--migrate"}, nil)
	unchanged = config.Credential{}
	if err := config.Read(w.config.CredentialFile, &unchanged); err != nil || unchanged.AgentID != w.agent.ID || unchanged.Token != "" {
		t.Fatal("native explicit migration retained a bearer secret", err)
	}
	state, err = local.Open(ctx, w.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var checkpoint map[string]string
	if found, err := state.Get(ctx, "identity-migration-checkpoint", &checkpoint); err != nil || !found || checkpoint["checkpoint"] != "retain existing uncertainty" {
		t.Fatal("attachment/migration reset local state", checkpoint, err)
	}
	wrong := config.Credential{AgentID: l.founder.agent.ID}
	if err := config.Write(w.config.CredentialFile, wrong); err != nil {
		t.Fatal(err)
	}
	reply, code, _ = run(t, "agent-cli", []string{"--config", w.configFile, "connect", "--migrate"}, nil)
	if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Denied {
		t.Fatal("attachment reused state bound to a different actor", reply.Error)
	}
	unchanged = config.Credential{}
	if err := config.Read(w.config.CredentialFile, &unchanged); err != nil || unchanged != wrong {
		t.Fatal("identity mismatch rewrote the existing binding", err)
	}
	if err := config.Write(w.config.CredentialFile, config.Credential{AgentID: w.agent.ID}); err != nil {
		t.Fatal(err)
	}
	workerConfig.IdentitySocket = filepath.Join(l.root, "absent-identity.sock")
	if err := config.Write(w.configFile, workerConfig); err != nil {
		t.Fatal(err)
	}
	reply, code, _ = run(t, "agent-cli", []string{"--config", w.configFile, "connect"}, nil)
	if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Unavailable {
		t.Fatal("missing local identity did not fail closed", reply.Error)
	}
}

func TestNativeRetiredBearerEnrollmentFlagsDoNotReturn(t *testing.T) {
	l := newLab(t, "codex")
	for _, flags := range [][]string{
		{"connect", "--invitation-file", filepath.Join(l.root, "retired-invitation.json")},
		{"connect", "--name", "different identity"},
	} {
		args := append([]string{"--config", l.worker.configFile}, flags...)
		reply, code, _ := run(t, "agent-cli", args, nil)
		if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Invalid {
			t.Fatal("native worker still accepted retired enrollment flags", flags, reply.Error)
		}
	}
	output := filepath.Join(l.root, "retired-invitation.json")
	reply, code, _ := run(t, "agent-cli", []string{"--config", l.founder.configFile, "invitation", "create", "--output", output}, nil)
	if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Prerequisite {
		t.Fatal("native CLI exposed bearer invitations", reply.Error)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("retired invitation command created a transfer file")
	}
	client, err := api.NewLocalClient(l.worker.config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	actor, err := api.Call[domain.Agent](context.Background(), client, "GET", "/v1/me", nil)
	if err != nil || actor.ID != l.worker.agent.ID {
		t.Fatal("rejected flags changed the existing local identity", err)
	}
}
