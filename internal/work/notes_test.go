package work

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

type notesTransport struct {
	fixture *pushFixture
	notes   []domain.Note
	paths   []string
	fault   *domain.Fault
}

func (r *notesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/v1/note-context" {
		return r.fixture.RoundTrip(req)
	}
	r.paths = append(r.paths, req.URL.Query().Get("project"))
	return r.fixture.response(r.notes, r.fault)
}

func TestNotesRefreshUsesCurrentTaskProjectAndOwnAgentInEveryPhase(t *testing.T) {
	f := newPushFixture(t)
	for _, item := range []struct {
		phase, project string
		launch         Launch
	}{
		{"onboarding", "", Launch{}},
		{"scope", f.project.ID, Launch{Task: &f.task}},
		{"acceptance", f.project.ID, Launch{Task: &f.task}},
		{"preflight", f.project.ID, Launch{Task: &f.task}},
		{domain.ExecutionRun, f.project.ID, Launch{Task: &f.task}},
		{domain.VerificationRun, f.project.ID, Launch{Task: &f.task}},
		{"attention", f.project.ID, Launch{Task: &f.task}},
		{"research", f.project.ID, Launch{Task: &f.task}},
		{"request", f.project.ID, Launch{Message: &domain.Message{Request: &domain.MessageActionState{Action: domain.MessageAction{ProjectID: f.project.ID}}}}},
		{"message", "", Launch{Message: &domain.Message{Kind: "direct"}}},
	} {
		t.Run(item.phase, func(t *testing.T) {
			notes := []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes, Revision: 1, Content: "Company mission and instructions"}}
			if item.project != "" {
				notes = append(notes, domain.Note{ID: "note_" + item.project, Scope: domain.ProjectNotes, TargetID: item.project, Revision: 2, Content: "Project goals and references"})
			}
			notes = append(notes, domain.Note{ID: "note_" + f.me.ID, Scope: domain.AgentNotes, TargetID: f.me.ID, Revision: 3, Content: "Own agent skills"})
			r := &notesTransport{fixture: f, notes: notes}
			f.engine.Client.HTTP.Transport = r
			launch := item.launch
			launch.Phase, launch.AgentID = item.phase, f.me.ID
			if err := f.engine.refreshNotes(context.Background(), &launch); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.paths, []string{item.project}) || !reflect.DeepEqual(launch.SharedNotes, notes) {
				t.Fatal("invocation omitted or borrowed a notes scope", r.paths, launch.SharedNotes)
			}
		})
	}
}

func TestNotesMissingMalformedOrUnavailableContextBlocksBeforeInference(t *testing.T) {
	for _, item := range []struct {
		name  string
		notes []domain.Note
		fault *domain.Fault
	}{
		{"missing", nil, nil},
		{"omitted-scope", []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes}}, nil},
		{"wrong-agent", []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes}, {ID: "note_agent_other", Scope: domain.AgentNotes, TargetID: "agent_other"}}, nil},
		{"unversioned-content", []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes, Content: "Unversioned guidance"}, {ID: "note_agent_push", Scope: domain.AgentNotes, TargetID: "agent_push"}}, nil},
		{"unavailable", nil, domain.Fail(domain.Unavailable, "Notes are temporarily unavailable")},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := newPushFixture(t)
			r := &notesTransport{fixture: f, notes: item.notes, fault: item.fault}
			f.engine.Client.HTTP.Transport = r
			_, err := f.engine.text(context.Background(), f.engine.Config, Launch{Phase: "onboarding", AgentID: f.me.ID}, true)
			if err == nil || f.executor.invocations.Load() != 0 {
				t.Fatal("incomplete notes became a successful provider call", err, f.executor.invocations.Load())
			}
			journals, err := f.engine.State.Journals(context.Background(), false)
			if err != nil || len(journals) != 0 {
				t.Fatal("unread notes created a durable inference proposal", journals, err)
			}
		})
	}
}

func TestNotesEditsNeitherInvalidateActiveAuthorityNorResetTextResponseFences(t *testing.T) {
	for _, modeOnly := range []bool{false, true} {
		subscription := authoritySubscription{taskID: "task_fixture", projectID: "project_fixture", modeOnly: modeOnly}
		if subscription.relevant(domain.Update{Topic: "note", SourceID: "note_company", TaskID: subscription.taskID, ProjectID: subscription.projectID}) {
			t.Fatal("next-invocation notes edit invalidated a running call", modeOnly)
		}
	}
	launch := Launch{Phase: "scope", Task: &domain.Task{ID: "task_fixture", ProjectID: "project_fixture", SpecGeneration: 1},
		SharedNotes: []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes, Revision: 1, Content: "Original instructions"}}}
	before := textActionHash(launch)
	launch.SharedNotes[0].Revision++
	launch.SharedNotes[0].Content = "Changed instructions do not grant an inference retry"
	if after := textActionHash(launch); before != after {
		t.Fatal("notes edit bypassed a retained phase-response fence")
	}
}
