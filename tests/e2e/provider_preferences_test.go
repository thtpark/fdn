//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/tests/scenario"
)

func TestNativePreferredModelsUseFounderDefaultsTaskOverridesAndProviderFallback(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		for _, test := range []struct {
			name, agentModel, taskModel, expected string
		}{
			{"agent-default", "agent-model", "", "agent-model"},
			{"task-override", "agent-model", "task-model", "task-model"},
			{"unavailable-task-model", "agent-model", "missing-task-model", ""},
			{"unavailable-agent-model", "missing-agent-model", "", ""},
		} {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				localProvider := "codex"
				if name == localProvider {
					localProvider = "copilot"
				}
				l := newLab(t, localProvider)
				l.cli(t, l.worker, []string{"work", "run"}, nil)
				preferences := must[domain.ProviderPreferences](t, "agent-cli",
					[]string{"--config", l.founder.configFile, "agent", "preferences", l.worker.agent.ID, "--input", "-"},
					map[string]any{"provider": name, "model": test.agentModel, "reason": "Set this worker's new-session defaults."})
				if preferences.Provider != name || preferences.Model != test.agentModel {
					t.Fatal("compiled Founder CLI lost its agent defaults", preferences)
				}
				task := l.approved(t, "Write and verify the model-selection result")
				taskProvider := name
				if test.taskModel == "" {
					taskProvider = ""
				}
				task = l.change(t, l.founder, task.ID, domain.TaskChange{
					Kind: "provider", Provider: taskProvider, Model: test.taskModel, Reason: "Choose the task's model preference."})
				task = l.change(t, l.founder, task.ID, domain.TaskChange{
					Kind: "approve", SpecGeneration: task.SpecGeneration, Reason: "Approve this exact preferred-model specification."})
				plan := scenario.Plan{ActorID: l.worker.agent.ID, Provider: name, Model: test.expected,
					Models: []string{"agent-model", "task-model"},
					Rules: []scenario.Rule{
						{Name: "preflight", Phases: []string{"preflight"}, MaxCalls: 1,
							Reply: `{"outcome":"clear","summary":"Reviewed the exact approved output and checks."}`},
						{Name: "execution", Phases: []string{domain.ExecutionRun}, MaxCalls: 1,
							Actions: []scenario.Action{{Kind: "write", Path: "result.txt", Text: "verified fixture result\n"}},
							Reply:   `{"outcome":"completed","summary":"Wrote and verified the requested result.","evidence":["Checked the exact saved bytes."]}`},
					}}
				if err := scenario.WritePlan(workerHome(l.worker), plan); err != nil {
					t.Fatal(err)
				}
				for step := 0; step < 3 && l.task(t, task.ID).Status != domain.Completed; step++ {
					l.cli(t, l.worker, []string{"work", "run"}, nil)
				}
				current := l.task(t, task.ID)
				if current.Status != domain.Completed || current.SpecGeneration != task.SpecGeneration || current.Model != test.taskModel {
					t.Fatal("model fallback changed the specification or failed to complete", current)
				}
				runs, err := api.Call[[]domain.Run](context.Background(), l.worker.client, "GET", api.Path("tasks", task.ID, "runs"), nil)
				if err != nil || len(runs) != 1 || runs[0].Provider != name || runs[0].Model != test.expected {
					t.Fatal("native invocation or server claim used the wrong selection", runs, err)
				}
				trace, err := scenario.ReadTrace(workerHome(l.worker))
				if err != nil || len(trace.Calls) != 2 || !trace.Calls[1].Coding || !trace.Calls[1].Completed {
					t.Fatal("real provider argv/tool execution was not verified", trace, err)
				}
				for _, call := range trace.Calls {
					if call.Provider != name || call.Model != test.expected {
						t.Fatal("actual provider argv ignored the preference hierarchy", call)
					}
				}
				bytes, err := os.ReadFile(filepath.Join(l.workspace, "result.txt"))
				if err != nil || string(bytes) != "verified fixture result\n" {
					t.Fatal("preferred-model execution did not produce the actual required output", string(bytes), err)
				}
				local, err := config.LoadWorker(l.worker.configFile)
				if err != nil || local.Provider != localProvider || local.Model != "fixture-model" {
					t.Fatal("remote defaults overwrote the installing user's local configuration", local.Provider, local.Model, err)
				}
			})
		}
	}
}
