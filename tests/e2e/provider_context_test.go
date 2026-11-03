//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

func TestNativeCodingToolsUseExplicitWorkerConfiguration(t *testing.T) {
	l := newLab(t, "codex")
	home := filepath.Join(filepath.Dir(l.worker.configFile), "home")
	t.Setenv("HOME", home)
	t.Setenv("FDN_WORKER_CONFIG", "/unrelated/inherited/configuration.json")
	path := filepath.Join(filepath.Dir(l.worker.configFile), "explicit worker configuration.json")
	if err := config.Write(path, l.worker.config); err != nil {
		t.Fatal(err)
	}
	worker, err := config.LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(filepath.Dir(l.worker.configFile), "native-coding-provider")
	command := fmt.Sprintf(`#!/bin/sh
set -eu
umask 077
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output-last-message ]; then output=$2; break; fi
  shift
done
test -n "$output"
cat >/dev/null
%q agent identity > identity-observation.json
printf '%%s\n' '{"outcome":"completed","summary":"Native CLI used the exact configured worker identity","evidence":[],"questions":[],"answer_ids":[],"safe_to_retry":false}' > "$output"
`, filepath.Join(binaries, "agent-cli"))
	if err := secure.Create(script, []byte(command)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(filepath.Dir(l.worker.configFile), "native-tool-workspace")
	if err := secure.Dir(workspace); err != nil {
		t.Fatal(err)
	}
	manager := provider.New(worker)
	reply, err := manager.Invoke(context.Background(), provider.Invocation{
		ID: "native-context-coding", Provider: "codex", Path: script, Coding: true,
		Workspace: workspace, Prompt: "Synthetic native context check; no paid inference.", Timeout: 5 * time.Second})
	if err != nil || reply.Outcome != "completed" {
		t.Fatal("native tool command fell back to another/missing configuration", err)
	}
	var result api.Envelope
	if err := config.Read(filepath.Join(workspace, "identity-observation.json"), &result); err != nil ||
		!result.OK {
		t.Fatal("native child omitted the actual CLI check", err)
	}
	var identity domain.AgentIdentity
	if err := json.Unmarshal(result.Data, &identity); err != nil || identity.AgentID != l.worker.agent.ID {
		t.Fatal("native child used a different identity", identity.AgentID, err)
	}
}
