package upgrade

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

const backupDiagnosticLimit = 8192
const coldBackupArchive = "before-upgrade.tar.age"

type backupStreamResult struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type backupCommandEvidence struct {
	Path          string   `json:"path"`
	Args          []string `json:"args"`
	StartedAt     int64    `json:"started_at"`
	FinishedAt    int64    `json:"finished_at,omitempty"`
	PID           int      `json:"pid,omitempty"`
	ProcessJoined bool     `json:"process_joined"`
	ExitCode      *int     `json:"exit_code,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type backupEvidenceRunner struct {
	runner    process.Runner
	directory string
	now       func() time.Time
}

func newBackupEvidenceRunner(directory string, runner process.Runner, now func() time.Time) (*backupEvidenceRunner, error) {
	path := filepath.Join(directory, "backup-command")
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, err
	}
	if err := secure.SyncDir(directory); err != nil {
		return nil, err
	}
	return &backupEvidenceRunner{runner: runner, directory: path, now: now}, nil
}

func (r *backupEvidenceRunner) Run(ctx context.Context, spec process.Spec) error {
	evidence := backupCommandEvidence{Path: spec.Path, Args: spec.Args, StartedAt: r.now().Unix()}
	if err := config.Write(filepath.Join(r.directory, "started.json"), evidence); err != nil {
		return err
	}
	output, err := process.NewPrivateOutput(filepath.Join(r.directory, "stderr.log"), backupDiagnosticLimit)
	if err != nil {
		return err
	}
	spec.Stderr = output
	started := spec.Started
	spec.Started = func(pid int) error {
		evidence.PID = pid
		if err := config.Write(filepath.Join(r.directory, "started.json"), evidence); err != nil {
			return err
		}
		if started != nil {
			return started(pid)
		}
		return nil
	}
	runErr := r.runner.Run(ctx, spec)
	_, outputErr := output.Finish()
	evidence.FinishedAt = r.now().Unix()
	var cleanup *process.CleanupError
	evidence.ProcessJoined = evidence.PID != 0 && !errors.As(runErr, &cleanup)
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		code := exit.ExitCode()
		evidence.ExitCode = &code
	} else if runErr == nil {
		code := 0
		evidence.ExitCode = &code
	}
	combined := errors.Join(runErr, outputErr)
	if combined != nil {
		evidence.Error = combined.Error()
	}
	return errors.Join(combined, config.Write(filepath.Join(r.directory, "result.json"), evidence))
}

func backupEvidenceError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(domain.Fail(domain.Unavailable, "native cold backup failed; private backup-command evidence was retained, not a verified backup"), err)
}
