package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestIdentityVersionIsOfflineAndStructured(t *testing.T) {
	var output, diagnostic bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &output, &diagnostic); code != 0 {
		t.Fatal("offline version failed", code)
	}
	var envelope api.Envelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatal("version did not return a valid envelope", err)
	}
	var version struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal(envelope.Data, &version); err != nil || version.Version != domain.Version || version.Commit != domain.Commit {
		t.Fatal("version did not expose the exact compiled binary", version, err)
	}
}

func TestIdentityCommandOfflineHelpAndStrictPrivateConfiguration(t *testing.T) {
	var output, errors bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &output, &errors); code != 0 {
		t.Fatal(code)
	}
	for _, word := range []string{"prepare", "connect", "serve", "installing user", "public", "No bearer"} {
		if !strings.Contains(output.String(), word) {
			t.Fatal("identity command help omitted its security contract", word)
		}
	}
	root, err := os.MkdirTemp(filepath.Join("..", ".."), ".id-command-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	file := filepath.Join(root, "identity.json")
	c := config.IdentityService{StateDir: filepath.Join(root, "private"), Socket: filepath.Join(root, "ipc", "identity.sock"), WorkerUID: os.Geteuid() + 1, WorkerGID: os.Getegid()}
	if err := config.Write(file, c); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"prepare", "connect", "serve"} {
		output.Reset()
		if code := run(context.Background(), []string{"--config", file, command}, &output, &errors); code != 4 {
			t.Fatal("identity native entrypoint accepted a foreign/legacy runner UID", command, code)
		}
		var result api.Envelope
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.OK || result.Error == nil || strings.Contains(output.String(), "PRIVATE KEY") {
			t.Fatal("invalid or sensitive native error envelope", err)
		}
	}
	if os.Geteuid() != 0 {
		c.WorkerUID, c.WorkerGID = 0, 0
		if err := config.Write(file, c); err != nil {
			t.Fatal(err)
		}
		publicDir := filepath.Join(root, "public")
		if err := os.Mkdir(publicDir, 0700); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		if code := run(context.Background(), []string{"--config", file, "prepare", "--name", "Agent1", "--output", filepath.Join(publicDir, "request.json")}, &output, &errors); code != 0 {
			t.Fatal("native preparation required another OS account or explicit UID/GID", code)
		}
	}
	if err := os.WriteFile(file, []byte(`{"state_dir":"/private","socket":"/ipc/s","worker_uid":5,"worker_gid":5,"test_uid":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := run(context.Background(), []string{"--config", file, "serve"}, &output, &errors); code != 2 {
		t.Fatal("native configuration accepted a runtime test-UID override", code)
	}
}
