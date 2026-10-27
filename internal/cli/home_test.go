package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestConfigInitConsolidatesDefaultUserPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var output, diagnostic bytes.Buffer
	if code := RunAgent(context.Background(), []string{"config", "init"}, nil, &output, &diagnostic); code != 0 {
		t.Fatal(code, output.String(), diagnostic.String())
	}
	root := filepath.Join(home, ".foundation")
	worker, err := config.LoadWorker(filepath.Join(root, "config", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if worker.StateDir != filepath.Join(root, "state") ||
		worker.WorkspaceRoot != filepath.Join(root, "workspaces") ||
		worker.IdentitySocket != filepath.Join(root, "run", "identity.sock") ||
		worker.CredentialFile != filepath.Join(root, "state", "credential.json") {
		t.Fatal("worker defaults escaped Foundation directory", worker)
	}
}

func TestExplicitLegacyConfigRemainsUsable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".config", "fdn", "agent.json")
	if err := config.Write(legacy, config.DefaultWorker(filepath.Join(home, "retained-state"))); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	code := RunAgent(context.Background(), []string{"--config", legacy, "config", "init"}, nil, &output, &diagnostic)
	var envelope struct {
		Error *domain.Fault `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err, output.String())
	}
	if code == 0 || envelope.Error == nil || envelope.Error.Code != domain.Conflict {
		t.Fatal("explicit existing configuration was rejected as an unavailable default", code, output.String())
	}
}
