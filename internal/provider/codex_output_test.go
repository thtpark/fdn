package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type codexOutputRunner struct {
	overflow bool
}

func (runner codexOutputRunner) Run(_ context.Context, spec process.Spec) error {
	reply, err := json.Marshal(Reply{Outcome: "completed", Summary: "Synthetic result from actual structured reply bytes"})
	if err != nil {
		return err
	}
	output := slices.Index(spec.Args, "--output-last-message")
	if output < 0 || output+1 >= len(spec.Args) {
		return errors.New("fixture omitted exact final-response file")
	}
	if err := secure.Write(spec.Args[output+1], reply); err != nil {
		return err
	}
	if !slices.Contains(spec.Args, "--json") || runner.overflow {
		_, err = spec.Stderr.Write(bytes.Repeat([]byte("d"), (256<<10)+1))
		return err
	}
	_, err = spec.Stdout.Write([]byte("{\"type\":\"thread.started\",\"thread_id\":\"synthetic_thread\"}\n"))
	return err
}

func TestCodexMachineOutputKeepsFinalReplySeparateFromHumanDiagnostics(t *testing.T) {
	for _, coding := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "private")
		worker := config.DefaultWorker(filepath.Join(root, "state"))
		if err := secure.Dir(root); err != nil {
			t.Fatal(err)
		}
		manager := &Manager{Config: worker, Runner: codexOutputRunner{}}
		reply, err := manager.Invoke(context.Background(), Invocation{ID: "machine_output", Provider: "codex",
			Path: "/synthetic/codex", Prompt: "Synthetic bounded prompt", Coding: coding, Workspace: root, Timeout: time.Second})
		if err != nil || reply.Outcome != "completed" {
			t.Fatal("human-format diagnostics rejected a valid structured result", coding, err)
		}
	}
}

func TestCodexMachineModeDoesNotTurnRealOverflowIntoSuccess(t *testing.T) {
	worker := config.DefaultWorker(filepath.Join(t.TempDir(), "private", "state"))
	manager := &Manager{Config: worker, Runner: codexOutputRunner{overflow: true}}
	if _, err := manager.Invoke(context.Background(), Invocation{ID: "actual_overflow", Provider: "codex",
		Path: "/synthetic/codex", Prompt: "Synthetic bounded prompt", Timeout: time.Second}); err == nil {
		t.Fatal("real diagnostic overflow was silently truncated into success")
	}
}
