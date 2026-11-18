package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
)

func TestNativeBackupEvidenceRetainsFailureAndExactBounds(t *testing.T) {
	for _, size := range []int{backupDiagnosticLimit, backupDiagnosticLimit + 1} {
		t.Run(string(rune('a'+size-backupDiagnosticLimit)), func(t *testing.T) {
			root := t.TempDir()
			runner, err := newBackupEvidenceRunner(root, process.Native{}, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), process.Spec{Path: "/bin/sh",
				Args: []string{"-c", "printf '%s' '" + strings.Repeat("x", size) + "' >&2; exit 23"}})
			if err == nil {
				t.Fatal("failed native backup command became success")
			}
			var result backupCommandEvidence
			if err := config.Read(filepath.Join(root, "backup-command/result.json"), &result); err != nil {
				t.Fatal(err)
			}
			if result.PID == 0 || !result.ProcessJoined || result.ExitCode == nil || *result.ExitCode != 23 || result.Error == "" {
				t.Fatal("native error or joined ownership evidence missing", result)
			}
			if strings.Contains(result.Error, "private size limit") != (size > backupDiagnosticLimit) {
				t.Fatal("diagnostic threshold did not distinguish exactly 8192 from 8193 bytes", result.Error)
			}
			path := filepath.Join(root, "backup-command/stderr.log")
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) != backupDiagnosticLimit {
				t.Fatal("private exact diagnostic bound lost", len(raw), err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("private diagnostic permissions", err)
			}
			if _, err := newBackupEvidenceRunner(root, process.Native{}, time.Now); !errors.Is(err, os.ErrExist) {
				t.Fatal("old diagnostic directory could be replaced", err)
			}
		})
	}
}

func TestNativeBackupEvidenceSuccessfulProcessCannotHideOverflow(t *testing.T) {
	for _, size := range []int{backupDiagnosticLimit, backupDiagnosticLimit + 1} {
		runner, err := newBackupEvidenceRunner(t.TempDir(), process.Native{}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		err = runner.Run(context.Background(), process.Spec{Path: "/bin/sh",
			Args: []string{"-c", "printf '%s' '" + strings.Repeat("x", size) + "' >&2"}})
		if (err != nil) != (size > backupDiagnosticLimit) {
			t.Fatal("successful exit hid output overflow or rejected exact limit", size, err)
		}
	}
}

func TestNativeBackupEvidenceRetainsCancellation(t *testing.T) {
	runner, err := newBackupEvidenceRunner(t.TempDir(), process.Native{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = runner.Run(ctx, process.Spec{Path: "/bin/sh", Args: []string{"-c", "printf private-cancel >&2; sleep 30"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("native cancellation lost", err)
	}
	var result backupCommandEvidence
	if err := config.Read(filepath.Join(runner.directory, "result.json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.PID == 0 || !result.ProcessJoined || result.Error == "" {
		t.Fatal("cancelled native process proof was not retained", result)
	}
}
