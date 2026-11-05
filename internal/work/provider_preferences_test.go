package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func TestAdhocPublicMetadataAndReportAreExplicitInProviderPrompts(t *testing.T) {
	for _, phase := range []string{"message", domain.ExecutionRun} {
		launch := Launch{Phase: phase}
		if phase == domain.ExecutionRun {
			launch.ConversationID = "conversation_fixture"
			launch.Task = &domain.Task{ID: "task_fixture", ProjectID: domain.AdhocProjectID}
		}
		context, err := json.Marshal(launch)
		if err != nil {
			t.Fatal(err)
		}
		prompt, err := invocationPrompt(context)
		if err != nil {
			t.Fatal(err)
		}
		fragments := []string{"title", "description", "acceptance_criteria", "private instructions"}
		if phase == domain.ExecutionRun {
			fragments = []string{"Include public_report={summary,evidence}", "private reply", "worklog add",
				"completed outcome still awaits independent verification"}
		}
		for _, fragment := range fragments {
			if !strings.Contains(prompt, fragment) {
				t.Fatal("provider prompt omitted safe task reporting requirements", phase, fragment)
			}
		}
	}
}

type selectionExecutor struct {
	models []string
	err    error
	names  []string
}

func (x *selectionExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	x.names = append(x.names, name)
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/synthetic/provider",
		Authentication: "authenticated", Models: x.models}, x.err
}

func (*selectionExecutor) Invoke(context.Context, provider.Invocation) (provider.Reply, error) {
	return provider.Reply{}, errors.New("selection must never start inference")
}

func TestProviderPreferencesTaskOverrideAndUnavailableModelFallback(t *testing.T) {
	for _, test := range []struct {
		name        string
		preferences *domain.ProviderPreferences
		launch      Launch
		models      []string
		provider    string
		model       string
		fallback    bool
	}{
		{"local", nil, Launch{}, []string{"local-model"}, "codex", "local-model", false},
		{"Founder-default", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{}, []string{"agent-model"}, "copilot", "agent-model", false},
		{"task-overrides-agent", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Task: &domain.Task{Provider: "copilot", Model: "task-model"}}, []string{"task-model", "agent-model"}, "copilot", "task-model", false},
		{"task-changes-provider", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Task: &domain.Task{Provider: "codex"}}, []string{"agent-model"}, "codex", "", false},
		{"missing-task-model-does-not-use-agent-model", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Task: &domain.Task{Provider: "copilot", Model: "missing-model"}}, []string{"agent-model"}, "copilot", "", true},
		{"missing-agent-model", &domain.ProviderPreferences{Provider: "copilot", Model: "missing-model"},
			Launch{}, []string{"another-model"}, "copilot", "", true},
		{"catalog-unavailable", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{}, nil, "copilot", "", true},
		{"verifier-uses-own-default", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Phase: domain.VerificationRun, Task: &domain.Task{Provider: "codex", Model: "task-model"}}, []string{"agent-model"}, "copilot", "agent-model", false},
		{"attention-uses-own-default", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Task: &domain.Task{Provider: "codex", Model: "task-model"}, Attention: &domain.Attention{}}, []string{"agent-model"}, "copilot", "agent-model", false},
		{"recovery-retains-source-selection", &domain.ProviderPreferences{Provider: "copilot", Model: "agent-model"},
			Launch{Recovery: &TaskRecoveryContext{SourceRun: domain.Run{Provider: "codex", Model: "source-model"}}}, []string{"source-model"}, "codex", "source-model", false},
		{"recovery-unavailable-model", nil,
			Launch{Recovery: &TaskRecoveryContext{SourceRun: domain.Run{Provider: "codex", Model: "source-model"}}}, []string{"other-model"}, "codex", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPushFixture(t)
			f.me.Preferences = test.preferences
			c := f.engine.Config
			c.Provider, c.Model = "codex", "local-model"
			x := &selectionExecutor{models: test.models}
			f.engine.Executor = x
			var logs bytes.Buffer
			f.engine.Log = slog.New(slog.NewTextHandler(&logs, nil))
			health, model, err := f.engine.selected(context.Background(), c, test.launch)
			if err != nil || health.Provider != test.provider || model != test.model || !slices.Equal(x.names, []string{test.provider}) {
				t.Fatal("incorrect session selection", health, model, x.names, err)
			}
			if (logs.Len() != 0) != test.fallback {
				t.Fatal("model fallback was silent or a normal selection logged a failure", logs.String())
			}
			if observed := readHealth(t, f.engine); observed.Model != model {
				t.Fatal("health did not retain the effective session model", observed)
			}
		})
	}
}

func TestProviderPreferencesRefreshForEachSessionWithoutChangingLocalConfig(t *testing.T) {
	f := newPushFixture(t)
	c := f.engine.Config
	x := &selectionExecutor{models: []string{"first-model", "next-model"}}
	f.engine.Executor = x
	for _, preferred := range []string{"first-model", "next-model"} {
		f.me.Preferences = &domain.ProviderPreferences{Provider: "copilot", Model: preferred}
		health, model, err := f.engine.selected(context.Background(), c, Launch{})
		if err != nil || health.Provider != "copilot" || model != preferred {
			t.Fatal("new session used stale defaults", health, model, err)
		}
	}
	if f.engine.Config.Provider != c.Provider || f.engine.Config.Model != c.Model {
		t.Fatal("Founder defaults rewrote the worker's local configuration")
	}
}
