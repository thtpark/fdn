//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeTaskRecoveryInspectsRetainedOutputAndContinuesWithoutHumanApproval(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t, name)
			task := l.approved(t, "recoverable")
			for range 2 {
				must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			}
			envelope, code, _ := run(t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			if code != 8 || envelope.OK {
				t.Fatal("fixture did not retain its first failed provider invocation")
			}
			time.Sleep(1100 * time.Millisecond)
			for range 5 {
				must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
				if l.task(t, task.ID).Status == domain.Completed {
					break
				}
			}
			current := l.task(t, task.ID)
			if current.ID != task.ID || current.Status != domain.Completed || current.UnsafeRetry ||
				current.Attempts != 2 || current.RecoveryAttempts != 1 || current.Failures != 1 || current.RetryApproval != nil {
				t.Fatal("native recovery replaced the task, reset accounting or required retry approval", current)
			}
			for _, file := range []struct {
				name, expected string
			}{{"result.txt", "verified fixture result\n"}, {"partial.txt", "verified fixture partial result\n"}} {
				content, err := os.ReadFile(filepath.Join(l.workspace, file.name))
				if err != nil || string(content) != file.expected {
					t.Fatal("native continuation did not preserve/check its actual output", file.name, err)
				}
			}
			executions, err := os.ReadFile(filepath.Join(l.workspace, "invocations.count"))
			if err != nil || strings.Count(string(executions), "\n") != 2 {
				t.Fatal("native execution count changed", err)
			}
			recovery, err := os.ReadFile(filepath.Join(l.workspace, "recovery.count"))
			if err != nil || strings.Count(string(recovery), "\n") != 1 {
				t.Fatal("native reconciliation repeated", err)
			}
		})
	}
}
