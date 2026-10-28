package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNoteCLIUsesDurableExactProposalAndExplicitRevisions(t *testing.T) {
	var bodies [][]byte
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/notes" || r.URL.RawQuery != "" {
			t.Error("unexpected note mutation route", r.Method, r.URL.String())
		}
		var req domain.NoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Scope != domain.CompanyNotes || req.Content == nil || *req.Content != "# Company mission\nShip reliable software." || req.ExpectedRevision != 7 || req.OperationID != "notes-exact-proposal" {
			t.Error("note CLI changed the exact proposal", req, err)
		}
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, data)
		diagnosticsEnvelope(t, w, domain.Note{ID: "note_company", Scope: domain.CompanyNotes, Content: *req.Content, Revision: 8}, nil)
	})
	input := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(input, []byte(`{"scope":"company","content":"# Company mission\nShip reliable software.","expected_revision":7,"reason":"Publish the current mission"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", file, "--operation-id", "notes-exact-proposal", "note", "set", "--input", input}
	for range 2 {
		envelope, code := invoke(t, "agent", args, nil)
		var note domain.Note
		if err := json.Unmarshal(envelope.Data, &note); code != 0 || !envelope.OK || err != nil || note.Revision != 8 {
			t.Fatal("note CLI failed its exact proposal", envelope, code, note, err)
		}
	}
	if len(bodies) != 2 || !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatal("uncertain retry changed the prepared note proposal", bodies)
	}
	if err := os.WriteFile(input, []byte(`{"scope":"company","content":"Changed immutable proposal","expected_revision":7,"reason":"Publish the current mission"}`), 0600); err != nil {
		t.Fatal(err)
	}
	envelope, code := invoke(t, "agent", args, nil)
	if code != 5 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Conflict || len(bodies) != 2 {
		t.Fatal("changed proposal reused a retained operation", envelope, code)
	}
	for _, request := range requests() {
		if request.path != "/v1/me" && (request.method != http.MethodPost || request.path != "/v1/notes") {
			t.Fatal("note edit silently fetched/substituted a newer revision", request)
		}
	}
}

func TestNoteCLIReadsScopesOwnContextAndBoundedHistory(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("note read made a mutation", r.Method, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/v1/notes":
			scope, target := r.URL.Query().Get("scope"), r.URL.Query().Get("target")
			if scope == domain.AgentNotes && !strings.HasPrefix(target, "agent_") {
				t.Error("agent notes did not default to the current identity", target)
			}
			diagnosticsEnvelope(t, w, domain.Note{Scope: scope, TargetID: target, Revision: 0}, nil)
		case r.URL.Path == "/v1/note-context":
			if r.URL.Query().Get("project") != "project_fixture" {
				t.Error("context lost the explicit project scope", r.URL.RawQuery)
			}
			diagnosticsEnvelope(t, w, []domain.Note{{Scope: domain.CompanyNotes}, {Scope: domain.ProjectNotes, TargetID: "project_fixture"}, {Scope: domain.AgentNotes}}, nil)
		case r.URL.Path == "/v1/history/note_project_fixture":
			diagnosticsEnvelope(t, w, []domain.History{}, nil)
		default:
			t.Error("unexpected note query", r.URL.String())
		}
	})
	for _, command := range [][]string{
		{"note", "show", "company"}, {"note", "show", "project", "project_fixture"}, {"note", "show", "agent"},
		{"note", "show", "agent", "agent_other"}, {"note", "context", "--project", "project_fixture"},
		{"note", "history", "project", "project_fixture"},
	} {
		args := append([]string{"--config", file}, command...)
		if envelope, code := invoke(t, "agent", args, nil); code != 0 || !envelope.OK {
			t.Fatal("note read command failed", command, envelope, code)
		}
	}
	for _, request := range requests() {
		if request.method != http.MethodGet || len(request.body) != 0 {
			t.Fatal("notes query was not bodyless and read-only", request)
		}
	}
	for _, command := range [][]string{
		{"note", "show"}, {"note", "show", "project"}, {"note", "show", "company", "agent_other"},
		{"note", "show", "agent", "agent_other", "extra"}, {"note", "context", "project_fixture"}, {"note", "context", "--agent", "agent_other"},
	} {
		args := append([]string{"--config", file}, command...)
		if envelope, code := invoke(t, "agent", args, nil); code != 2 || envelope.OK {
			t.Fatal("invalid notes CLI arity or impersonation accepted", command, envelope, code)
		}
	}
}
