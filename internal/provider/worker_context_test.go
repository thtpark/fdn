package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
)

func TestCodingInvocationBindsOnlyItsLoadedWorkerConfigLocally(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(root, "explicit worker.json")
	if err := config.Write(path, config.DefaultWorker(filepath.Join(root, "state"))); err != nil {
		t.Fatal(err)
	}
	worker, err := config.LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.WorkerConfigEnvironment, "/unrelated/inherited/config.json")
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "copilot"} {
		for _, coding := range []bool{false, true} {
			runner := &fixtureRunner{}
			manager := &Manager{Config: worker, Runner: runner}
			_, err := manager.Invoke(context.Background(), Invocation{ID: name + map[bool]string{true: "-coding", false: "-text"}[coding],
				Provider: name, Path: "/fixture/" + name, Coding: coding, Workspace: workspace, Prompt: "private prompt", Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			spec := runner.specs[len(runner.specs)-1]
			found := ""
			for _, entry := range spec.Env {
				if value, ok := strings.CutPrefix(entry, config.WorkerConfigEnvironment+"="); ok {
					found = value
				}
			}
			if coding && found != path || !coding && found != "" {
				t.Fatal("tool context escaped coding or inherited another worker binding", name, coding, found)
			}
			prompt, err := os.ReadFile(filepath.Join(worker.StateDir, "invocations",
				name+map[bool]string{true: "-coding", false: "-text"}[coding], "prompt.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(spec.Args, " "), path) || strings.Contains(string(prompt), path) {
				t.Fatal("local tool context entered model prompt or provider argv")
			}
		}
	}
}
