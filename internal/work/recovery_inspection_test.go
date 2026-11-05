package work

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func TestRecoveryInspectionReadsActualBytesAndExplicitFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "retained.bin")
	content := []byte("actual retained input\n")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	check, err := inspectRecoveryPath(context.Background(), path)
	if err != nil || check.Problem != "" || check.Kind != "file" ||
		check.Size != int64(len(content)) || check.SHA256 != domain.Hash(string(content)) {
		t.Fatal("inspection did not read the actual input", check, err)
	}
	for _, test := range []struct {
		name string
		make func(string) error
		code string
	}{
		{"missing", func(string) error { return nil }, "not-found"},
		{"symlink", func(target string) error { return os.Symlink(path, target) }, "access-denied"},
		{"fifo", func(target string) error { return syscall.Mkfifo(target, 0600) }, "access-denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(root, test.name)
			if err := test.make(target); err != nil {
				t.Fatal(err)
			}
			check, err := inspectRecoveryPath(context.Background(), target)
			if err != nil || check.Problem != test.code || check.Detail == "" || check.SHA256 != "" {
				t.Fatal("failed check became a successful inspection", check, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectRecoveryPath(ctx, path); err == nil {
		t.Fatal("cancelled inspection was accepted")
	}
}

func TestRecoveryInspectionBoundsDirectoryOutput(t *testing.T) {
	root := t.TempDir()
	for index := range 130 {
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(index)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	check, err := inspectRecoveryPath(context.Background(), root)
	if err != nil || check.Kind != "directory" || check.Problem != "" || !check.Truncated || len(check.Entries) != 128 {
		t.Fatal("directory inspection was unbounded or lost its truncation", check, err)
	}
}

func TestRecoveryPromptAuthorizesInspectionRatherThanRetryApproval(t *testing.T) {
	prompt, err := phasePrompt(domain.RecoveryRun)
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{
		"TOOL-ENABLED inspection", "already admitted by the controller", "not a prohibition on inspection",
		"max_retries=0 with failures=0 is NOT exhausted", "work recovery-inspect",
		"Task flags and policy metadata are not observations", "Missing ordinary tools may be installed",
		"Never approve retries or clear a fence yourself", "not execution replay",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatal("missing recovery instruction", required)
		}
	}
	if err := validateTextPhase(Launch{Phase: "preflight"}, provider.Reply{
		Outcome: "paused", Recovery: &provider.RecoveryResult{InspectionID: "inspection_wrong_phase"},
	}); err == nil {
		t.Fatal("tool-disabled preparation accepted a recovery proposal")
	}
}

func TestRecoveryContinuationPreflightDoesNotMistakeItsOwnSandboxForAnExecutionBlocker(t *testing.T) {
	prompt, err := phasePrompt("preflight")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"read-only sandbox in THIS preparation invocation", "not evidence that the later execution invocation",
		"Do not block or ask to make this preparation call writable", "genuinely observed OS restriction or revoked binding",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatal("preflight confused preparation isolation with execution access", required)
		}
	}
}
