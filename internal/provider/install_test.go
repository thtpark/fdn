package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
)

type npmFixture struct {
	auditFailure bool
	calls        []process.Spec
}

func (n *npmFixture) Run(_ context.Context, s process.Spec) error {
	n.calls = append(n.calls, s)
	if s.Path == "npm" {
		switch s.Args[0] {
		case "install":
			if !slices.Contains(s.Args, "--ignore-scripts") || !slices.Contains(s.Args, "--save-exact") {
				return fmt.Errorf("unsafe install flags")
			}
			packageSpec := s.Args[len(s.Args)-1]
			index := strings.LastIndex(packageSpec, "@")
			name, version := packageSpec[:index], packageSpec[index+1:]
			data, _ := json.Marshal(map[string]any{"packages": map[string]any{"node_modules/" + name: map[string]string{"version": version, "integrity": "sha512-fixture-integrity"}}})
			if err := os.WriteFile(filepath.Join(s.Dir, "package-lock.json"), data, 0600); err != nil {
				return err
			}
			path := filepath.Join(s.Dir, "node_modules", ".bin", "codex")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(version), 0700)
		case "ci":
			if !slices.Contains(s.Args, "--ignore-scripts") {
				return fmt.Errorf("scripts enabled")
			}
			return nil
		case "audit":
			if len(s.Args) < 2 || s.Args[1] != "signatures" {
				return fmt.Errorf("signature audit missing")
			}
			if n.auditFailure {
				return fmt.Errorf("synthetic signature verification failure")
			}
			return nil
		}
		return fmt.Errorf("unexpected npm command")
	}
	if slices.Contains(s.Args, "--help") {
		_, err := io.WriteString(s.Stdout, "--dangerously-bypass-approvals-and-sandbox --ask-for-approval --output-last-message --ephemeral --sandbox --skip-git-repo-check")
		return err
	}
	if slices.Contains(s.Args, "--version") {
		version, err := os.ReadFile(s.Path)
		if err != nil {
			return err
		}
		_, err = s.Stdout.Write(version)
		return err
	}
	return fmt.Errorf("unexpected provider invocation")
}

func TestProviderStagingVerifiesSignaturesAndRetainsActiveOnFailure(t *testing.T) {
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	fixture := &npmFixture{}
	m := &Manager{Config: c, Runner: fixture}
	installed, err := m.Install(context.Background(), "codex", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !installed.SignaturesVerified || installed.Version != "1.2.3" {
		t.Fatal(installed)
	}
	fixture.auditFailure = true
	if _, err := m.Install(context.Background(), "codex", "1.2.4"); err == nil {
		t.Fatal("failed signature audit activated provider")
	}
	var active Installed
	if err := config.Read(filepath.Join(c.StateDir, "providers", "codex", "active.json"), &active); err != nil {
		t.Fatal(err)
	}
	if active.Version != "1.2.3" || active.Path != installed.Path {
		t.Fatal("active provider changed after staging failure")
	}
	entries, err := os.ReadDir(filepath.Join(c.StateDir, "providers", "codex", "versions"))
	if err != nil || len(entries) != 1 {
		t.Fatal("failed stage leaked", err)
	}
	if _, err := m.Install(context.Background(), "codex", "latest"); err == nil {
		t.Fatal("unbounded version accepted")
	}
}

func TestRPCBoundsAndResponseIdentity(t *testing.T) {
	for _, data := range []string{
		strings.Repeat("x", 5000),
		"Content-Length: 1\r\nContent-Length: 1\r\n\r\n{}",
		"Content-Length: 9999999\r\n\r\n",
		"Content-Length: 2\r\n\r\n{}",
		"Content-Length: 48\r\n\r\n" + `{"jsonrpc":"2.0","id":8,"result":{"ok":true}}`,
	} {
		if _, err := rpc(bufio.NewReaderSize(strings.NewReader(data), 4096), io.Discard, 1, "connect", map[string]any{}); err == nil {
			t.Fatal("invalid RPC frame accepted")
		}
	}
	result := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(result), result)
	var request bytes.Buffer
	if _, err := rpc(bufio.NewReader(strings.NewReader(frame)), &request, 1, "connect", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.String(), `"method":"connect"`) {
		t.Fatal("wrong RPC request")
	}
}
