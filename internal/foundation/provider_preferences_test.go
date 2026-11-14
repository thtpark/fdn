package foundation

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentPreferencesFounderOrSelfVersionedAndReplaySafe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	provider, model := "copilot", "preferred-fixture"
	req := domain.AgentPreferencesRequest{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1},
		Provider: preferenceText(provider), Model: preferenceText(model), Reason: "Founder chooses the default model."}
	for _, actor := range []domain.Agent{f.other} {
		_, err := f.s.SetAgentPreferences(ctx, actor.ID, f.worker.ID, req)
		assertCode(t, err, domain.Denied)
	}
	before, err := f.s.Me(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, req)
	if err != nil || saved.Provider != provider || saved.Model != model || saved.Revision != 2 || saved.UpdatedBy != f.founder.ID {
		t.Fatal("Founder defaults were not saved", saved, err)
	}
	replayed, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, saved) {
		t.Fatal("exact preference replay changed its receipt", replayed, err)
	}
	after, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || after.Preferences == nil || *after.Preferences != saved ||
		after.Revision != before.Revision || after.Mode != before.Mode {
		t.Fatal("defaults changed mode/authority generation or were absent from the agent", after, err)
	}
	preference, err := f.s.AgentPreferences(ctx, f.other.ID, f.worker.ID)
	if err != nil || preference != saved {
		t.Fatal("agent defaults could not be inspected", preference, err)
	}
	provider = "codex"
	changed, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, domain.AgentPreferencesRequest{
		Command:  domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: saved.Revision},
		Provider: &provider, Reason: "Switch provider without inheriting its predecessor's model.",
	})
	if err != nil || changed.Provider != "codex" || changed.Model != "" || changed.Revision != 3 {
		t.Fatal("provider change retained the old model", changed, err)
	}
	_, err = f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, domain.AgentPreferencesRequest{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: saved.Revision},
		Model:   &model, Reason: "Stale edit must not overwrite newer defaults.",
	})
	assertCode(t, err, domain.Conflict)
	provider = ""
	cleared, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, domain.AgentPreferencesRequest{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: changed.Revision}, Provider: &provider, Reason: "Restore the worker's local defaults.",
	})
	if err != nil || cleared.Provider != "" || cleared.Model != "" || cleared.Revision != 4 {
		t.Fatal("defaults were not explicitly cleared", cleared, err)
	}
	old, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, req)
	current, readErr := f.s.AgentPreferences(ctx, f.founder.ID, f.worker.ID)
	if err != nil || readErr != nil || old != saved || current != cleared {
		t.Fatal("old exact replay reapplied defaults or lost its original receipt", old, current, err, readErr)
	}
}

func TestAgentPreferencesSelfMayChangePriorFounderDefaultsWithoutChangingAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := "codex"
	saved, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, domain.AgentPreferencesRequest{
		Command:  domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1},
		Provider: &first, Reason: "Founder initial preference",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Me(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, model := "copilot", "self-selected"
	changed, err := f.s.SetAgentPreferences(ctx, f.worker.ID, f.worker.ID, domain.AgentPreferencesRequest{
		Command:  domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: saved.Revision},
		Provider: &next, Model: &model, Reason: "Agent deliberately chooses its next-call default",
	})
	if err != nil || changed.Provider != next || changed.Model != model || changed.UpdatedBy != f.worker.ID {
		t.Fatal("self-managed preferred model did not replace the earlier default", changed, err)
	}
	after, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || after.Revision != before.Revision || after.Mode != before.Mode {
		t.Fatal("self-managed defaults changed mode or authority", after, err)
	}
	_, err = f.s.SetAgentPreferences(ctx, f.worker.ID, f.founder.ID, domain.AgentPreferencesRequest{
		Command:  domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1},
		Provider: &next, Reason: "Cannot change another actor",
	})
	assertCode(t, err, domain.Denied)
}

func TestAgentPreferencesValidateBeforeChangingState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		provider, model *string
		reason          string
	}{
		{nil, nil, "No preference"},
		{preferenceText("unsupported"), nil, "Unsupported provider"},
		{nil, preferenceText("unbound-model"), "No provider for the model"},
		{preferenceText("codex"), preferenceText(strings.Repeat("x", 257)), "Oversized model"},
		{preferenceText("copilot"), preferenceText("invalid\x00model"), "Invalid model"},
		{preferenceText("codex"), nil, ""},
	} {
		_, err := f.s.SetAgentPreferences(ctx, f.founder.ID, f.worker.ID, domain.AgentPreferencesRequest{
			Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1}, Provider: test.provider, Model: test.model, Reason: test.reason,
		})
		assertCode(t, err, domain.Invalid)
	}
	got, err := f.s.AgentPreferences(ctx, f.worker.ID, f.worker.ID)
	if err != nil || got.Revision != 1 || got.Provider != "" || got.Model != "" {
		t.Fatal("invalid updates changed the default", got, err)
	}
}

func preferenceText(value string) *string { return &value }

func TestTaskModelPreferenceAllowsOnlyTheSelectedModelOrProviderDefault(t *testing.T) {
	for _, model := range []string{"preferred-fixture", "", "different-fixture"} {
		t.Run("model="+model, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			task := f.ready(t)
			task = f.change(t, f.founder.ID, task.ID, "provider", func(q *domain.TaskChange) {
				q.Provider, q.Model = "codex", "preferred-fixture"
			})
			task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
			task = f.change(t, f.founder.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
			run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
				Command:        domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
				SpecGeneration: task.SpecGeneration, Provider: "codex", Model: model, TokenHash: domain.Hash(domain.NewToken()),
			})
			if model == "different-fixture" {
				assertCode(t, err, domain.Prerequisite)
			} else if err != nil || run.Model != model {
				t.Fatal("preferred model or provider-selected fallback was rejected", run, err)
			}
		})
	}
}

func TestTaskCreateProviderPreferencesRequireProjectAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Model selection"})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.TaskRequest{Command: op(), ProjectID: project.ID, Title: "Use the selected task model", Provider: "copilot", Model: "task-fixture"}
	_, err = f.s.CreateTask(ctx, f.worker.ID, req)
	assertCode(t, err, domain.Denied)
	task, err := f.s.CreateTask(ctx, f.founder.ID, req)
	if err != nil || task.Provider != req.Provider || task.Model != req.Model {
		t.Fatal("authorized task model preference was lost", task, err)
	}
}
