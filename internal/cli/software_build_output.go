package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func runNativeBuildStep(ctx context.Context, state, build, run string, index int, workspace, extraPath string, step domain.BuildStep) error {
	reference := fmt.Sprintf("%s/%s/step-%03d", build, run, index)
	failure := func() error {
		return domain.Fail(domain.Prerequisite, "native build step failed; inspect private evidence "+reference+"; original running journal remains fenced")
	}
	parent := filepath.Join(state, "build-evidence", build, run)
	for _, directory := range []string{filepath.Join(state, "build-evidence"), filepath.Join(state, "build-evidence", build), parent} {
		if err := secure.Dir(directory); err != nil {
			return failure()
		}
	}
	directory := filepath.Join(parent, fmt.Sprintf("step-%03d", index))
	if err := os.Mkdir(directory, 0700); err != nil {
		return failure()
	}
	identity, err := json.Marshal(struct {
		BuildID string   `json:"build_id"`
		RunID   string   `json:"run_id"`
		Step    int      `json:"step"`
		Args    []string `json:"args"`
	}{build, run, index, step.Args})
	if err != nil {
		return failure()
	}
	if err := secure.Create(filepath.Join(directory, "step.json"), identity); err != nil {
		return failure()
	}
	stdout, err := process.NewPrivateOutput(filepath.Join(directory, "stdout.log"), 256<<10)
	if err != nil {
		return failure()
	}
	stderr, err := process.NewPrivateOutput(filepath.Join(directory, "stderr.log"), 256<<10)
	if err != nil {
		_, closeErr := stdout.Finish()
		return errors.Join(failure(), safeBuildOutputError(closeErr))
	}
	binary, lookupErr := exec.LookPath(step.Args[0])
	if extraPath != "" {
		for _, entry := range filepath.SplitList(extraPath) {
			candidate := filepath.Join(entry, step.Args[0])
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				binary, lookupErr = candidate, nil
				break
			}
		}
	}
	env := process.Environment()
	if extraPath != "" {
		for i, entry := range env {
			if strings.HasPrefix(entry, "PATH=") {
				env[i] = "PATH=" + extraPath + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
			}
		}
	}
	runErr := lookupErr
	if runErr == nil {
		runErr = (process.Native{}).Run(ctx, process.Spec{Path: binary, Args: step.Args[1:], Dir: workspace, Env: env, Stdout: stdout, Stderr: stderr})
	}
	_, stdoutErr := stdout.Finish()
	_, stderrErr := stderr.Finish()
	var cleanup *process.CleanupError
	result, marshalErr := json.Marshal(struct {
		ExecutionFailed bool `json:"execution_failed"`
		OutputFailed    bool `json:"output_failed"`
		CleanupFailed   bool `json:"cleanup_failed"`
	}{runErr != nil, stdoutErr != nil || stderrErr != nil, errors.As(runErr, &cleanup)})
	persistErr := marshalErr
	if persistErr == nil {
		persistErr = secure.Create(filepath.Join(directory, "result.json"), result)
	}
	if errors.Join(runErr, stdoutErr, stderrErr, persistErr) != nil {
		return failure()
	}
	return nil
}

func safeBuildOutputError(err error) error {
	if err != nil {
		return domain.Fail(domain.Prerequisite, "private native output persistence or close failed")
	}
	return nil
}
