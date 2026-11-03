package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
)

func TestWorkerCLIContextUsesScopedConfigAndExplicitFlagWins(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	t.Setenv("HOME", filepath.Join(root, "home"))
	first, second := filepath.Join(root, "explicit worker.json"), filepath.Join(root, "other worker.json")
	for _, path := range []string{first, second} {
		if err := config.Write(path, config.DefaultWorker(path+"-state")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.WorkerConfigEnvironment, first)
	for _, scenario := range []struct {
		args []string
		want string
	}{
		{[]string{"config", "show"}, first + "-state"},
		{[]string{"--config", second, "config", "show"}, second + "-state"},
	} {
		result, code := invoke(t, "agent", scenario.args, nil)
		var worker config.Worker
		if err := json.Unmarshal(result.Data, &worker); err != nil || code != 0 || !result.OK || worker.StateDir != scenario.want {
			t.Fatal("worker tool context fell back or overrode its explicit flag", code, result.Error, err)
		}
	}
	t.Setenv(config.WorkerConfigEnvironment, filepath.Join(root, "missing-worker.json"))
	if result, code := invoke(t, "agent", []string{"config", "show"}, nil); code == 0 || result.OK {
		t.Fatal("missing explicit tool context silently fell back to another identity")
	}
	if _, err := os.Stat(filepath.Join(root, "home", config.UserDirectory, "config", "agent.json")); !os.IsNotExist(err) {
		t.Fatal("scoped context adopted or created a default configuration", err)
	}
}
