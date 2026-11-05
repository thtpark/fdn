//go:build e2e && liveprovider

package e2e_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

func TestLiveCodexRecoveryInspectsZeroRetryZIP(t *testing.T) {
	providerPath, keyPath := os.Getenv("FDN_LIVE_CODEX_PATH"), os.Getenv("FDN_LIVE_API_KEY_FILE")
	if providerPath == "" || keyPath == "" || os.Getenv("FDN_LIVE_CODEX_AUTHORIZED") != "yes" {
		t.Skip("requires explicit paid-test authorization, an isolated Codex executable and an owner-private API key file")
	}
	if !filepath.IsAbs(providerPath) || !filepath.IsAbs(keyPath) {
		t.Fatal("live-test executable/key references must be absolute")
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("live-test key file must already be owner-only")
	}
	keyData, err := secure.Read(keyPath, 8192)
	if err != nil {
		t.Fatal("protected live-test key could not be read")
	}
	key := strings.TrimSpace(string(keyData))
	if _, value, found := strings.Cut(key, "="); found {
		key = strings.Trim(strings.TrimSpace(value), "\"'")
	}
	if key == "" || strings.ContainsAny(key, "\r\n") {
		t.Fatal("live-test key file must contain one key or one environment assignment")
	}
	authHome := filepath.Join(t.TempDir(), "codex")
	if err := secure.Dir(authHome); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", authHome)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	auth, err := json.Marshal(map[string]string{"OPENAI_API_KEY": key})
	if err != nil {
		t.Fatal("could not prepare isolated API authentication")
	}
	authPath := filepath.Join(authHome, "auth.json")
	if err := secure.Create(authPath, auth); err != nil {
		t.Fatal("could not prepare isolated API authentication")
	}
	t.Cleanup(func() {
		if err := os.Remove(authPath); err != nil && !os.IsNotExist(err) {
			t.Error("isolated API authentication cleanup failed")
		}
	})
	if err := secure.Create(filepath.Join(authHome, "config.toml"), []byte("model_reasoning_effort = \"low\"\nmodel_max_output_tokens = 4096\n")); err != nil {
		t.Fatal(err)
	}
	l := newLab(t, "codex", foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
		func(context.Context, string, []uint32) error { return nil })})
	if evidence := os.Getenv("FDN_LIVE_EVIDENCE_DIR"); evidence != "" {
		if !filepath.IsAbs(evidence) {
			t.Fatal("live-test evidence directory must be absolute")
		}
		if err := secure.Dir(evidence); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			entries, err := os.ReadDir(filepath.Join(l.worker.config.StateDir, "invocations"))
			if err != nil && !os.IsNotExist(err) {
				t.Error("could not enumerate private synthetic invocation evidence")
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				target := filepath.Join(evidence, entry.Name())
				if err := secure.Dir(target); err != nil {
					t.Error("could not create private synthetic evidence destination")
					continue
				}
				for _, name := range []string{"prompt.txt", "reply.json", "stdout.log", "stderr.log"} {
					data, err := secure.Read(filepath.Join(l.worker.config.StateDir, "invocations", entry.Name(), name), 3<<20)
					if os.IsNotExist(err) {
						continue
					}
					if err != nil || secure.Create(filepath.Join(target, name), data) != nil {
						t.Error("could not retain private synthetic invocation evidence")
					}
				}
			}
		})
	}
	l.workspace = filepath.Join(l.root, "workspaces", "live-recovery")
	if err := os.Mkdir(l.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	l.binding = must[local.Binding](t, "agent-cli", []string{"--config", l.worker.configFile,
		"workspace", "bind", "--path", l.workspace, "--allow-tools", "--timeout", "180"}, nil)
	l.cli(t, l.worker, []string{"config", "set", "--input", "-"}, map[string]any{"invocation_timeout_seconds": 180})
	task := l.approved(t, "recoverable-stopped")
	data := liveRecoveryArchive(t, 20_210_825)
	upload := filepath.Join(l.root, "synthetic.zip")
	if err := os.WriteFile(upload, data, 0600); err != nil {
		t.Fatal(err)
	}
	file := must[domain.SharedFile](t, "agent-cli", []string{"--config", l.founder.configFile,
		"file", "upload", "--path", upload}, nil)
	l.cli(t, l.founder, []string{"file", "grant", file.ID, "--input", "-"},
		domain.FileGrantRequest{AgentID: l.worker.agent.ID, Reason: "Explicit synthetic test input grant"})
	must[domain.FileDownloadReceipt](t, "agent-cli", []string{"--config", l.worker.configFile,
		"file", "download", file.ID, "--output", filepath.Join(l.workspace, "source.zip")}, nil)
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "provider", Reason: "Pin the same actual Codex model before the interrupted execution", Provider: "codex", Model: "gpt-5.4"})
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "retry-policy", Reason: "Exact zero-retry interrupted-work case",
		RetryPolicy: &domain.RetryPolicy{MaxRetries: 0, BackoffSeconds: 1}})
	description := fmt.Sprintf("Inspect the already downloaded synthetic source.zip (%d bytes, SHA-256 %s). It contains nested.zip, containing values.xlsx with xl/worksheets/sheet1.xml. Read the actual nested workbook and write analysis.txt with the exact UTF-8 content %q (without the surrounding quotes and without a trailing newline). Retain the verified input and existing partial nested.zip. This is local file reading/analysis only; no external mutations or document code execution are authorized.", file.Size, file.SHA256, "Nested workbook A1: 42")
	criteria := "Actual analysis.txt bytes equal the quoted UTF-8 string \"Nested workbook A1: 42\" without the quotes or any trailing newline. Verify the input and read the actual nested workbook, not metadata."
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "edit", Reason: "Authorize exact synthetic file-analysis scope",
		Description: &description, AcceptanceCriteria: &criteria})
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "approve", Reason: "Exact synthetic scope approved", SpecGeneration: task.SpecGeneration})
	for range 2 {
		must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(binaries, "agent-cli"), "--config", l.worker.configFile,
		"--timeout", "240", "work", "run")
	command.Env = isolatedEnvironment(command.Args[1:])
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waitFor(t, 15*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(l.workspace, "nested.zip"))
		return err == nil
	}, "actual fixture partial extraction before controlled cancellation")
	l.cli(t, l.founder, []string{"agent", "mode", l.worker.agent.ID, "--input", "-"},
		domain.ModeRequest{Mode: "manual", Reason: "Controlled test interruption; join the original process before recovery"})
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("original native process did not join after controlled cancellation")
	}
	current := l.task(t, task.ID)
	if current.ActiveRun != nil || current.Failures != 0 || current.Attempts != 1 || !current.UnsafeRetry || current.RetryPolicy.MaxRetries != 0 {
		t.Fatal("controlled interruption did not reproduce stopped zero-failure/zero-retry work")
	}
	l.cli(t, l.worker, []string{"config", "set", "--input", "-"},
		map[string]any{"provider_paths": map[string]string{"codex": providerPath, "copilot": filepath.Join(binaries, "fixtureprovider")}})
	l.cli(t, l.founder, []string{"agent", "mode", l.worker.agent.ID, "--input", "-"},
		domain.ModeRequest{Mode: "ai", Reason: "Restore test AI intent for the explicitly authorized bounded synthetic recovery"})
	for range 6 {
		stepCtx, stop := context.WithTimeout(context.Background(), 240*time.Second)
		step := exec.CommandContext(stepCtx, filepath.Join(binaries, "agent-cli"), "--config", l.worker.configFile,
			"--timeout", "240", "work", "run")
		step.Env = isolatedEnvironment(step.Args[1:])
		output, err := step.Output()
		stop()
		var result api.Envelope
		if err != nil || json.Unmarshal(output, &result) != nil || !result.OK {
			t.Fatal("actual Codex controller step failed; provider diagnostics remain private", err)
		}
		current = l.task(t, task.ID)
		if current.Status == domain.Completed || (current.RecoveryAttempts != 0 && current.ActiveRun == nil && current.UnsafeRetry) {
			break
		}
	}
	state, err := local.Open(context.Background(), l.worker.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	journals, err := state.Journals(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var recoveries, realCalls, commandCalls, receipts int
	var inputTokens, outputTokens int64
	for _, journal := range journals {
		if journal.TaskID != task.ID || journal.ProviderVersion == "1.2.3" {
			continue
		}
		realCalls++
		if journal.Kind == domain.RecoveryRun {
			recoveries++
			var reply struct {
				Recovery struct {
					InspectionID string `json:"inspection_id"`
				} `json:"recovery"`
			}
			if err := json.Unmarshal(journal.Reply, &reply); err != nil {
				t.Fatal(err)
			}
			var receipt work.RecoveryInspection
			found, err := state.Get(context.Background(), "recovery-inspection:"+reply.Recovery.InspectionID, &receipt)
			if err != nil || !found || receipt.RunID != journal.RunID || len(receipt.Checks) == 0 {
				t.Fatal("actual Codex recovery did not produce native inspection evidence")
			}
			receipts++
		}
		file, err := os.Open(filepath.Join(l.worker.config.StateDir, "invocations", journal.ID, "stdout.log"))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var event struct {
				Type string `json:"type"`
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
				Usage struct {
					Input  int64 `json:"input_tokens"`
					Output int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				if event.Type == "item.completed" && event.Item.Type == "command_execution" {
					commandCalls++
				}
				if event.Type == "turn.completed" {
					inputTokens += event.Usage.Input
					outputTokens += event.Usage.Output
				}
			}
		}
		if err := scanner.Err(); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(filepath.Join(l.workspace, "analysis.txt"))
	if err != nil || string(actual) != "Nested workbook A1: 42" || current.Status != domain.Completed ||
		current.ID != task.ID || current.Attempts != 2 || current.Failures != 0 || current.RecoveryAttempts != 1 ||
		current.UnsafeRetry || recoveries != 1 || receipts != 1 || commandCalls < 1 || realCalls > 6 {
		t.Fatal("actual Codex did not inspect and continue the original exact zero-retry task", current.Status, current.Attempts,
			current.Failures, current.RecoveryAttempts, recoveries, receipts, commandCalls, realCalls)
	}
	t.Logf("Real Codex: calls=%d recovery=%d native_receipts=%d commands=%d input_tokens=%d output_tokens=%d; original task completed with attempts=2 failures=0 max_retries=0",
		realCalls, recoveries, receipts, commandCalls, inputTokens, outputTokens)
}

func liveRecoveryArchive(t *testing.T, size int) []byte {
	t.Helper()
	member := func(name string, content []byte) []byte {
		var data bytes.Buffer
		archive := zip.NewWriter(&data)
		part, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	sheet := []byte(`<worksheet><sheetData><row r="1"><c r="A1"><v>42</v></c></row></sheetData></worksheet>`)
	nested := member("values.xlsx", member("xl/worksheets/sheet1.xml", sheet))
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"nested.zip", nested}, {"padding.bin", make([]byte, size-len(member("nested.zip", nested))-len(member("padding.bin", nil))+22)}} {
		part, err := archive.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if data.Len() != size {
		t.Fatal("synthetic archive size does not match the live scenario", data.Len(), size)
	}
	return data.Bytes()
}
