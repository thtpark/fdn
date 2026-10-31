package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type environmentVersionRunner struct {
	calls int
	fail  bool
}

func (r *environmentVersionRunner) Run(_ context.Context, spec process.Spec) error {
	r.calls++
	if len(spec.Args) != 1 || spec.Args[0] != "--version" {
		return domain.Fail(domain.Invalid, "fixture rejected a non-version command")
	}
	if r.fail {
		return domain.Fail(domain.Unavailable, "fixture unavailable")
	}
	return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]any{
		"version": "0.3.16", "commit": "fixture", "api_version": domain.APIVersion, "schema_version": domain.SchemaVersion}})
}

func TestEnvironmentEmbeddedFactsNeverRunDesktopPrograms(t *testing.T) {
	observation, err := CollectEmbeddedEnvironment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if observation.CPU.LogicalCores <= 0 || observation.Memory.TotalBytes == nil || *observation.Memory.TotalBytes == 0 ||
		len(observation.Executables) != 1 || observation.Executables[0].State != "embedded" {
		t.Fatal("embedded environment did not report measurable resources and its actual runtime")
	}
}

func TestEnvironmentExecutableProbeIsBoundedVersionOnlyAndUnavailableIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-cli")
	if err := os.WriteFile(path, []byte("non-executable fixture content"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &environmentVersionRunner{}
	observed := observeFoundationExecutable(context.Background(), runner, "agent-cli", path)
	if observed.State != "observed" || observed.Version != "0.3.16" || !domain.ValidHash(observed.SHA256) || runner.calls != 1 {
		t.Fatal("version probe or digest was not observed", observed)
	}
	runner.fail = true
	observed = observeFoundationExecutable(context.Background(), runner, "agent-cli", path)
	if observed.State != "unavailable" || observed.Version != "" {
		t.Fatal("failed binary probe fabricated a version", observed)
	}
}
