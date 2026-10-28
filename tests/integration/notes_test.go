//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type notesInvocation struct {
	launch work.Launch
	prompt string
	coding bool
}

type notesExecutor struct {
	base        provider.Executor
	invocations []notesInvocation
	during      func(context.Context, work.Launch)
}

func (x *notesExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	return x.base.Check(ctx, name)
}

func (x *notesExecutor) Invoke(ctx context.Context, inv provider.Invocation) (provider.Reply, error) {
	_, raw, _ := strings.Cut(inv.Prompt, "\nFDN_CONTEXT_JSON\n")
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	x.invocations = append(x.invocations, notesInvocation{launch: launch, prompt: inv.Prompt, coding: inv.Coding})
	if x.during != nil {
		x.during(ctx, launch)
	}
	return x.base.Invoke(ctx, inv)
}

func publishIntegrationNote(t *testing.T, client *api.Client, scope, target, content string) domain.Note {
	t.Helper()
	ctx := context.Background()
	query := url.Values{"scope": {scope}, "target": {target}}
	current, err := api.Call[domain.Note](ctx, client, http.MethodGet, "/v1/notes?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	note, err := api.Call[domain.Note](ctx, client, http.MethodPost, "/v1/notes", domain.NoteRequest{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: current.Revision},
		Scope:   scope, TargetID: target, Content: &content, Reason: "Publish intentional scoped task guidance",
	})
	if err != nil {
		t.Fatal(err)
	}
	return note
}

func TestScopedNotesReachEveryTaskPhaseRefreshBetweenCallsAndRemainDistinctFromPrivateKnowledge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	workerBase := &executor{}
	worker := newEngine(t, h, workerBase)
	task := readyForEngine(t, h, worker)
	delegateProject(t, h, task)
	task = h.change(t, h.founder, task.ID, "edit", func(r *domain.TaskChange) {
		description := "Review the exact fixture output before execution."
		r.Description = &description
	})
	company := publishIntegrationNote(t, h.founder, domain.CompanyNotes, "", "# Company mission\nDeliver dependable results.")
	project := publishIntegrationNote(t, h.other, domain.ProjectNotes, task.ProjectID, "# Project goals\n[Context](https://example.com/context) [Repo](https://github.com/example/repo)")
	agent := publishIntegrationNote(t, h.worker, domain.AgentNotes, "", "# Worker skills\nUse exact output checks.")
	reviewerNote := publishIntegrationNote(t, h.other, domain.AgentNotes, "", "REVIEWER_ONLY_AGENT_GUIDANCE")
	unrelated, err := api.Call[domain.Project](ctx, h.founder, http.MethodPost, "/v1/projects", domain.ProjectRequest{Command: command(), Name: "Unrelated project"})
	if err != nil {
		t.Fatal(err)
	}
	publishIntegrationNote(t, h.founder, domain.ProjectNotes, unrelated.ID, "UNRELATED_PROJECT_GUIDANCE")
	if _, err := worker.State.PutKnowledge(ctx, local.KnowledgeChange{Kind: "skill", Name: "private-checks", Content: "PRIVATE_LOCAL_SKILL_NOT_FOR_PUBLICATION", Selected: true}); err != nil {
		t.Fatal(err)
	}
	workerCalls := &notesExecutor{base: workerBase}
	var updatedCompany domain.Note
	workerCalls.during = func(call context.Context, launch work.Launch) {
		if launch.Phase == "preflight" {
			if !reflect.DeepEqual(launch.SharedNotes, []domain.Note{company, project, agent}) {
				t.Fatal("preflight did not pin the original snapshot", launch.SharedNotes)
			}
			updatedCompany = publishIntegrationNote(t, h.founder, domain.CompanyNotes, "", "# Updated company mission\nKeep exact verification evidence.")
			if call.Err() != nil {
				t.Fatal("note edit cancelled the in-flight provider call", call.Err())
			}
		}
	}
	worker.Executor = workerCalls
	for range 4 {
		if _, err := worker.Step(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if workerBase.count("onboarding") != 1 || workerBase.count("scope") != 1 || workerBase.count("acceptance") != 1 || workerBase.count("execution") != 0 {
		t.Fatal("notes bypassed separate review or signoff", workerBase.phases)
	}
	current, err := api.Call[domain.Task](ctx, h.worker, http.MethodGet, api.Path("tasks", task.ID), nil)
	if err != nil || current.Readiness != nil {
		t.Fatal("notes granted execution approval", current, err)
	}
	h.change(t, h.founder, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = current.SpecGeneration })
	for range 2 {
		if _, err := worker.Step(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	current, err = api.Call[domain.Task](ctx, h.worker, http.MethodGet, api.Path("tasks", task.ID), nil)
	if err != nil || current.Status != domain.Completed || updatedCompany.Revision != company.Revision+1 {
		t.Fatal("approved task did not finish with a refreshed next-call snapshot", current, updatedCompany, err)
	}
	for _, inv := range workerCalls.invocations {
		want := []domain.Note{company, project, agent}
		if inv.launch.Phase == "onboarding" {
			want = []domain.Note{company, agent}
		} else if inv.launch.Phase == domain.ExecutionRun {
			want = []domain.Note{updatedCompany, project, agent}
			if !inv.coding {
				t.Fatal("execution notes were only tested on a text call")
			}
		}
		if !reflect.DeepEqual(inv.launch.SharedNotes, want) || len(inv.launch.Knowledge) != 1 || inv.launch.Knowledge[0].Content != "PRIVATE_LOCAL_SKILL_NOT_FOR_PUBLICATION" {
			t.Fatal("provider omitted scoped notes or mixed private/shared knowledge", inv.launch.Phase, inv.launch.SharedNotes, inv.launch.Knowledge)
		}
		if strings.Contains(inv.prompt, "REVIEWER_ONLY_AGENT_GUIDANCE") || strings.Contains(inv.prompt, "UNRELATED_PROJECT_GUIDANCE") || !strings.Contains(inv.prompt, "Read and apply every supplied shared_notes document") {
			t.Fatal("worker prompt borrowed another scope or lacked explicit note instructions", inv.launch.Phase)
		}
	}
	journals, err := worker.State.Journals(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range workerCalls.invocations {
		found := false
		for _, journal := range journals {
			if journal.Kind != inv.launch.Phase {
				continue
			}
			var retained work.Launch
			if err := json.Unmarshal(journal.Context, &retained); err != nil || !reflect.DeepEqual(retained.SharedNotes, inv.launch.SharedNotes) || journal.PromptHash != domain.Hash(inv.prompt) {
				t.Fatal("journal rewrote the snapshot or omitted it from the prompt hash", journal.Kind, err)
			}
			found = true
		}
		if !found {
			t.Fatal("provider snapshot was not durably retained", inv.launch.Phase)
		}
	}
	reviewer := newEngine(t, h, &executor{})
	reviewer.Client = h.other
	reviewer.Config.Provider, reviewer.Config.Model = "copilot", "review-model"
	reviewerCalls := &notesExecutor{base: &verificationExecutor{t: t, decisions: []string{"accept"}}}
	reviewer.Executor = reviewerCalls
	for range 2 {
		if _, err := reviewer.Step(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	verified := false
	for _, inv := range reviewerCalls.invocations {
		if inv.launch.Phase == domain.VerificationRun {
			verified = true
			if !inv.coding || !reflect.DeepEqual(inv.launch.SharedNotes, []domain.Note{updatedCompany, project, reviewerNote}) {
				t.Fatal("independent reviewer used the assignee's agent notes or a stale company snapshot", inv.launch.SharedNotes)
			}
			if len(inv.launch.Knowledge) != 0 || strings.Contains(inv.prompt, "PRIVATE_LOCAL_SKILL_NOT_FOR_PUBLICATION") {
				t.Fatal("reviewer borrowed another agent's private local skill")
			}
		}
	}
	if !verified {
		t.Fatal("separate tool-enabled verification did not reference scoped notes")
	}
	current, err = api.Call[domain.Task](ctx, h.worker, http.MethodGet, api.Path("tasks", task.ID), nil)
	if err != nil || current.Status != domain.Done || current.Verification == nil || current.Verification.ActorID != h.otherAgent.ID {
		t.Fatal("notes replaced independent current-authority verification", current, err)
	}
	for _, note := range []domain.Note{updatedCompany, project, agent, reviewerNote} {
		query := url.Values{"scope": {note.Scope}, "target": {note.TargetID}}
		retained, err := api.Call[domain.Note](ctx, h.worker, http.MethodGet, "/v1/notes?"+query.Encode(), nil)
		if err != nil || !reflect.DeepEqual(retained, note) || strings.Contains(retained.Content, "PRIVATE_LOCAL_SKILL_NOT_FOR_PUBLICATION") {
			t.Fatal("task work changed shared notes or automatically published private knowledge", retained, err)
		}
	}
}
