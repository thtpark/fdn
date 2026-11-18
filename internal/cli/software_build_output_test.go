package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeBuildStepPrivateFailureAndOverflow(t *testing.T) {
	for _, command := range []string{"printf private-out; printf private-err >&2; exit 7", "head -c 262145 /dev/zero"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			step := domain.BuildStep{Args: []string{"sh", "-c", command}}
			err := runNativeBuildStep(context.Background(), root, "build_fixture", "run_fixture", 1, root, "", step)
			if err == nil || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private-out") {
				t.Fatal("failure lost or private data exposed", err)
			}
			directory := filepath.Join(root, "build-evidence", "build_fixture", "run_fixture", "step-001")
			for _, name := range []string{"step.json", "stdout.log", "stderr.log", "result.json"} {
				info, err := os.Stat(filepath.Join(directory, name))
				if err != nil || info.Mode().Perm() != 0600 || info.Size() > 256<<10 {
					t.Fatal("missing, public or unbounded private evidence", name, err)
				}
			}
			if strings.Contains(command, "private-out") {
				out, err := os.ReadFile(filepath.Join(directory, "stdout.log"))
				if err != nil || string(out) != "private-out" {
					t.Fatal("failed step stdout was lost", err)
				}
				diagnostic, err := os.ReadFile(filepath.Join(directory, "stderr.log"))
				if err != nil || string(diagnostic) != "private-err" {
					t.Fatal("failed step stderr was lost", err)
				}
			}
			replay := domain.BuildStep{Args: []string{"sh", "-c", "touch replayed"}}
			if err := runNativeBuildStep(context.Background(), root, "build_fixture", "run_fixture", 1, root, "", replay); err == nil {
				t.Fatal("existing step evidence overwritten")
			}
			if _, err := os.Stat(filepath.Join(root, "replayed")); !os.IsNotExist(err) {
				t.Fatal("step replay repeated native effects", err)
			}
		})
	}
}

func TestNativeBuildStepPersistenceFailureDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "build-evidence"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	step := domain.BuildStep{Args: []string{"sh", "-c", "touch executed"}}
	if err := runNativeBuildStep(context.Background(), root, "build_fixture", "run_fixture", 1, root, "", step); err == nil {
		t.Fatal("evidence persistence failure became success")
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("compiler ran without durable evidence", err)
	}
}
