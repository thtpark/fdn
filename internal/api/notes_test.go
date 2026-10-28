package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNotesBinaryAPIEnforcesAuthorityAndExactContext(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	delegate := messageFixtureClient(t, f.server, f.delegate.ID)
	target := messageFixtureClient(t, f.server, f.target.ID)
	companyText, projectText, agentText := "# Company mission\nBe reliable.", "# Project goals\n[Repository](https://github.com/example/repo)", "# Agent skills\nUse the supplied context."
	companyReq := domain.NoteRequest{Command: domain.Command{OperationID: "notes-company"}, Scope: domain.CompanyNotes, Content: &companyText, Reason: "Publish the company mission"}
	company, err := Call[domain.Note](ctx, founder, http.MethodPost, "/v1/notes", companyReq)
	if err != nil || company.ID != "note_company" || company.Revision != 1 || company.UpdatedBy != f.founder.ID {
		t.Fatal("binary company notes lost their contract", company, err)
	}
	for _, client := range []*Client{delegate, target} {
		req := companyReq
		req.OperationID = domain.NewID("op")
		if _, err := Call[domain.Note](ctx, client, http.MethodPost, "/v1/notes", req); err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("non-Founder set company-wide notes", err)
		}
	}
	project, err := Call[domain.Note](ctx, delegate, http.MethodPost, "/v1/notes", domain.NoteRequest{
		Command: domain.Command{OperationID: "notes-project"}, Scope: domain.ProjectNotes, TargetID: f.project.ID,
		Content: &projectText, Reason: "Publish delegated project goals",
	})
	if err != nil || project.TargetID != f.project.ID || project.UpdatedBy != f.delegate.ID {
		t.Fatal("current delegate could not set project-wide notes", project, err)
	}
	agent, err := Call[domain.Note](ctx, target, http.MethodPost, "/v1/notes", domain.NoteRequest{
		Command: domain.Command{OperationID: "notes-self"}, Scope: domain.AgentNotes, Content: &agentText, Reason: "Publish own agent-wide guidance",
	})
	if err != nil || agent.TargetID != f.target.ID {
		t.Fatal("implicit self target used a different identity", agent, err)
	}
	query := url.Values{"project": {f.project.ID}}
	notes, err := Call[[]domain.Note](ctx, target, http.MethodGet, "/v1/note-context?"+query.Encode(), nil)
	if err != nil || !reflect.DeepEqual(notes, []domain.Note{company, project, agent}) {
		t.Fatal("binary task context omitted an applicable note", notes, err)
	}
	ownContext, err := Call[[]domain.Note](ctx, delegate, http.MethodGet, "/v1/note-context?"+query.Encode(), nil)
	if err != nil || len(ownContext) != 3 || ownContext[2].TargetID != f.delegate.ID || ownContext[2].Revision != 0 || ownContext[2].Content != "" {
		t.Fatal("context borrowed another agent's instructions", ownContext, err)
	}
	for _, path := range []string{"/v1/note-context?agent=" + f.target.ID, "/v1/notes?scope=company&actor=agent_other", "/v1/notes?scope=agent&scope=company"} {
		if err := target.Request(ctx, http.MethodGet, path, nil, nil); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("binary notes accepted unknown or repeated query metadata", path, err)
		}
	}
	companyReq.Content = &projectText
	if _, err := Call[domain.Note](ctx, founder, http.MethodPost, "/v1/notes", companyReq); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("binary note operation accepted changed immutable content", err)
	}
	history, err := Call[[]domain.History](ctx, delegate, http.MethodGet, Path("history", agent.ID), nil)
	if err != nil || len(history) != 1 || history[0].ActorID != f.target.ID {
		t.Fatal("binary notes history lost provenance", history, err)
	}
	var historical domain.Note
	if err := json.Unmarshal(history[0].Snapshot, &historical); err != nil || !reflect.DeepEqual(historical, agent) {
		t.Fatal("protobuf history changed the actual notes", historical, err)
	}
	empty := ""
	cleared, err := Call[domain.Note](ctx, target, http.MethodPost, "/v1/notes", domain.NoteRequest{
		Command: domain.Command{OperationID: "notes-clear", ExpectedRevision: agent.Revision}, Scope: domain.AgentNotes,
		Content: &empty, Reason: "Explicitly clear own notes",
	})
	if err != nil || cleared.Content != "" || cleared.Revision != 2 {
		t.Fatal("protobuf confused present empty content with omitted content", cleared, err)
	}
}

func TestNotesRawAPIRejectsProtectedFieldsAndPreservesCurrentDocuments(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	for _, field := range []string{"updated_by", "created_by", "updated_at", "created_at", "revision", "id", "founder", "delegates", "project_epoch", "selected", "archived"} {
		payload := map[string]any{"operation_id": domain.NewID("op"), "scope": "agent", "content": "Current agent guidance", "reason": "Explicit edit", field: "forged"}
		envelope, code := f.request(t, f.target, http.MethodPost, "/v1/notes", payload)
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("raw notes API accepted a protected field", field, code, envelope)
		}
	}
	for _, payload := range []map[string]any{
		{"operation_id": "notes-no-content", "scope": "agent", "reason": "Missing content is not a clear"},
		{"operation_id": "notes-null-content", "scope": "agent", "content": nil, "reason": "Null is not a clear"},
		{"operation_id": "notes-negative-revision", "scope": "agent", "content": "", "reason": "Invalid revision", "expected_revision": -1},
		{"operation_id": "notes-no-reason", "scope": "agent", "content": "No intentional publication reason"},
		{"operation_id": "notes-company-target", "scope": "company", "target_id": f.target.ID, "content": "Invalid target", "reason": "Invalid company scope"},
	} {
		envelope, code := f.request(t, f.target, http.MethodPost, "/v1/notes", payload)
		if code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("invalid note input was silently accepted", code, envelope)
		}
	}
	envelope, code := f.request(t, f.target, http.MethodGet, "/v1/notes?scope=agent", nil)
	var note domain.Note
	if err := json.Unmarshal(envelope.Data, &note); err != nil || code != http.StatusOK || note.Revision != 0 || note.Content != "" || note.TargetID != f.target.ID {
		t.Fatal("rejected raw input mutated the unset document", code, note, err)
	}
}
