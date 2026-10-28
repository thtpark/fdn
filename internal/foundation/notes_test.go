package foundation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func setFixtureNote(t *testing.T, s *Store, actor, scope, target, content string) domain.Note {
	t.Helper()
	ctx := context.Background()
	current, err := s.Note(ctx, actor, scope, target)
	if err != nil {
		t.Fatal(err)
	}
	note, err := s.SetNote(ctx, actor, domain.NoteRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: current.Revision},
		Scope:   scope, TargetID: target, Content: &content, Reason: "Intentional shared guidance",
	})
	if err != nil {
		t.Fatal(err)
	}
	return note
}

func noteDelegate(t *testing.T, f *fixture, projectID string) domain.Project {
	t.Helper()
	ctx := context.Background()
	project, err := f.s.Project(ctx, f.founder.ID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestNotesScopeAuthorityAndTeamReadableSnapshots(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project := noteDelegate(t, f, task.ProjectID)
	for _, item := range []struct {
		name, actor, scope, target string
		allowed                    bool
	}{
		{"Founder company", f.founder.ID, domain.CompanyNotes, "", true},
		{"delegate not company", f.other.ID, domain.CompanyNotes, "", false},
		{"worker not company", f.worker.ID, domain.CompanyNotes, "", false},
		{"Founder project", f.founder.ID, domain.ProjectNotes, project.ID, true},
		{"delegate project", f.other.ID, domain.ProjectNotes, project.ID, true},
		{"worker not project", f.worker.ID, domain.ProjectNotes, project.ID, false},
		{"self implicit", f.worker.ID, domain.AgentNotes, "", true},
		{"delegate assigned agent", f.other.ID, domain.AgentNotes, f.worker.ID, true},
		{"worker not another agent", f.worker.ID, domain.AgentNotes, f.other.ID, false},
		{"delegate not Founder", f.other.ID, domain.AgentNotes, f.founder.ID, false},
		{"Founder another agent", f.founder.ID, domain.AgentNotes, f.other.ID, true},
		{"delegate self", f.other.ID, domain.AgentNotes, f.other.ID, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			before, err := f.s.Note(ctx, item.actor, item.scope, item.target)
			if err != nil {
				t.Fatal(err)
			}
			content := "# Mission\nDeliver reliable software.\n\n[Skill](skills/review.md) [Website](https://example.com) [Repository](https://github.com/example/repo)"
			note, err := f.s.SetNote(ctx, item.actor, domain.NoteRequest{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: before.Revision},
				Scope:   item.scope, TargetID: item.target, Content: &content, Reason: "Set current mission and references",
			})
			if !item.allowed {
				assertCode(t, err, domain.Denied)
				after, err := f.s.Note(ctx, f.founder.ID, before.Scope, before.TargetID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("denied edit changed the document", after, err)
				}
				return
			}
			if err != nil || note.Content != content || note.Revision != before.Revision+1 || note.UpdatedBy != item.actor || note.CreatedBy == "" {
				t.Fatal("allowed scoped edit failed or lost provenance", note, err)
			}
			for _, viewer := range []string{f.founder.ID, f.worker.ID, f.other.ID} {
				shown, err := f.s.Note(ctx, viewer, note.Scope, note.TargetID)
				if err != nil || !reflect.DeepEqual(shown, note) {
					t.Fatal("shared notes were not team readable", shown, err)
				}
			}
		})
	}
	otherProject, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Other project"})
	if err != nil {
		t.Fatal(err)
	}
	setFixtureNote(t, f.s, f.founder.ID, domain.ProjectNotes, otherProject.ID, "UNRELATED_PROJECT_CONTEXT")
	setFixtureNote(t, f.s, f.other.ID, domain.AgentNotes, "", "OTHER_AGENT_CONTEXT")
	notes, err := f.s.NoteContext(ctx, f.worker.ID, project.ID)
	if err != nil || len(notes) != 3 || notes[0].Scope != domain.CompanyNotes || notes[1].TargetID != project.ID || notes[2].TargetID != f.worker.ID {
		t.Fatal("context omitted or reordered applicable scopes", notes, err)
	}
	data, _ := json.Marshal(notes)
	if strings.Contains(string(data), "UNRELATED_PROJECT_CONTEXT") || strings.Contains(string(data), "OTHER_AGENT_CONTEXT") {
		t.Fatal("context included another project or agent's notes")
	}
	notes, err = f.s.NoteContext(ctx, f.worker.ID, "")
	if err != nil || len(notes) != 2 || notes[0].Scope != domain.CompanyNotes || notes[1].Scope != domain.AgentNotes {
		t.Fatal("unscoped context inferred a project", notes, err)
	}
	if _, err := f.s.NoteContext(ctx, f.worker.ID, "project_missing"); err == nil {
		t.Fatal("unknown project became an empty successful context")
	}
}

func TestAgentNotesDelegateAuthorityTracksCurrentAssignments(t *testing.T) {
	for _, action := range []string{"revoke-delegate", "archive-project", "archive-task", "reassign-task", "terminal-task"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			task := f.ready(t)
			project := noteDelegate(t, f, task.ProjectID)
			before := setFixtureNote(t, f.s, f.other.ID, domain.AgentNotes, f.worker.ID, "Assigned-agent guidance")
			switch action {
			case "revoke-delegate":
				_, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
					Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true,
				})
				if err != nil {
					t.Fatal(err)
				}
			case "archive-project":
				archived := true
				_, err := f.s.ChangeProject(ctx, f.founder.ID, project.ID, domain.ProjectChange{
					Command:  domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision},
					Archived: &archived, Reason: "Archive this project's authority",
				})
				if err != nil {
					t.Fatal(err)
				}
			case "archive-task":
				f.change(t, f.founder.ID, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.CancelledStatus })
				f.change(t, f.founder.ID, task.ID, "archive", nil)
			case "reassign-task":
				f.change(t, f.founder.ID, task.ID, "assign", func(r *domain.TaskChange) { r.AgentID = f.founder.ID })
			case "terminal-task":
				f.change(t, f.founder.ID, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.CancelledStatus })
			}
			content := "No longer authorized"
			_, err := f.s.SetNote(ctx, f.other.ID, domain.NoteRequest{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: before.Revision},
				Scope:   domain.AgentNotes, TargetID: f.worker.ID, Content: &content, Reason: "Attempt a stale delegate edit",
			})
			assertCode(t, err, domain.Denied)
			after, err := f.s.Note(ctx, f.worker.ID, domain.AgentNotes, "")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("lost authority still changed agent-wide guidance", after, err)
			}
			setFixtureNote(t, f.s, f.worker.ID, domain.AgentNotes, "", "Self-owned guidance remains editable")
		})
	}
}

func TestNotesRevisionsReplayHistoryValidationAndUnchangedRunAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, err := f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeProject, err := f.s.Project(ctx, f.worker.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	beforeAgent, err := f.s.Me(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := f.s.Note(ctx, f.worker.ID, domain.AgentNotes, "")
	if err != nil || empty.Revision != 0 || empty.Content != "" || empty.ID != "note_"+f.worker.ID {
		t.Fatal("unset notes did not expose explicit revision zero", empty, err)
	}
	content := strings.Repeat("x", domain.MaxNoteContent)
	req := domain.NoteRequest{Command: op(), Scope: domain.AgentNotes, Content: &content, Reason: "Set the exact maximum supported content"}
	first, err := f.s.SetNote(ctx, f.worker.ID, req)
	if err != nil || len(first.Content) != domain.MaxNoteContent {
		t.Fatal("exact content limit rejected", first.Revision, err)
	}
	for range 4 {
		setFixtureNote(t, f.s, f.worker.ID, domain.AgentNotes, "", "Current agent instructions")
	}
	current, err := f.s.Note(ctx, f.worker.ID, domain.AgentNotes, "")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.SetNote(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, current) {
		t.Fatal("source-bound replay lost the current document", replayed, err)
	}
	histories, err := f.s.Histories(ctx, f.other.ID, current.ID)
	if err != nil || len(histories) != domain.HistoryLimit || histories[0].ActorID != f.worker.ID || histories[0].Reason != "Intentional shared guidance" {
		t.Fatal("notes history was unbounded, private or missing attribution", histories, err)
	}
	var snapshot domain.Note
	if err := json.Unmarshal(histories[0].Snapshot, &snapshot); err != nil || !reflect.DeepEqual(snapshot, current) {
		t.Fatal("history did not retain the actual note version", snapshot, err)
	}
	different := "Different immutable proposal"
	changed := req
	changed.Content = &different
	_, err = f.s.SetNote(ctx, f.worker.ID, changed)
	assertCode(t, err, domain.Conflict)
	stale := req
	stale.Command = op()
	_, err = f.s.SetNote(ctx, f.worker.ID, stale)
	assertCode(t, err, domain.Conflict)
	for _, item := range []struct {
		name string
		edit func(*domain.NoteRequest)
		code domain.Code
	}{
		{"missing-content", func(r *domain.NoteRequest) { r.Content = nil }, domain.Invalid},
		{"negative-revision", func(r *domain.NoteRequest) { r.ExpectedRevision = -1 }, domain.Invalid},
		{"missing-reason", func(r *domain.NoteRequest) { r.Reason = "" }, domain.Invalid},
		{"unknown-scope", func(r *domain.NoteRequest) { r.Scope = "organization" }, domain.Invalid},
		{"unknown-agent", func(r *domain.NoteRequest) { r.TargetID = "agent_missing" }, domain.NotFound},
		{"nul-content", func(r *domain.NoteRequest) { text := "bad\x00content"; r.Content = &text }, domain.Invalid},
		{"oversized-content", func(r *domain.NoteRequest) { text := strings.Repeat("x", domain.MaxNoteContent+1); r.Content = &text }, domain.Invalid},
	} {
		t.Run(item.name, func(t *testing.T) {
			input := domain.NoteRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: current.Revision},
				Scope: domain.AgentNotes, Content: &different, Reason: "Validate a proposed edit"}
			item.edit(&input)
			_, err := f.s.SetNote(ctx, f.worker.ID, input)
			assertCode(t, err, item.code)
		})
	}
	cleared := setFixtureNote(t, f.s, f.worker.ID, domain.AgentNotes, "", "")
	if cleared.Revision != current.Revision+1 || cleared.Content != "" || cleared.CreatedBy != first.CreatedBy || cleared.CreatedAt != first.CreatedAt {
		t.Fatal("explicit clear lost revision or creation provenance", cleared)
	}
	setFixtureNote(t, f.s, f.founder.ID, domain.CompanyNotes, "", "Company instructions changed during a call")
	setFixtureNote(t, f.s, f.founder.ID, domain.ProjectNotes, task.ProjectID, "Project instructions changed during a call")
	afterTask, err := f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil || !reflect.DeepEqual(beforeTask, afterTask) || afterTask.ActiveRun.ID != run.ID || afterTask.ActiveRun.StopRequested {
		t.Fatal("notes edit stopped work or changed task/retry/approval authority", afterTask, err)
	}
	afterProject, err := f.s.Project(ctx, f.worker.ID, task.ProjectID)
	if err != nil || !reflect.DeepEqual(beforeProject, afterProject) {
		t.Fatal("notes edit changed project revision or authority epoch", afterProject, err)
	}
	afterAgent, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || !reflect.DeepEqual(beforeAgent, afterAgent) {
		t.Fatal("notes edit changed identity or global mode", afterAgent, err)
	}
	assertUpdateChangesDrained(t, f.s)
}

func TestNotesConcurrentEditsAndBodylessScopedUpdates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	cursors := map[string]string{}
	for _, agent := range []string{f.founder.ID, f.worker.ID, f.other.ID} {
		cursors[agent] = updateWatermark(t, f.s, agent)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, content := range []string{"First proposed company mission", "Second proposed company mission"} {
		writers.Go(func() {
			<-start
			_, err := f.s.SetNote(ctx, f.founder.ID, domain.NoteRequest{
				Command: op(), Scope: domain.CompanyNotes, Content: &content, Reason: "Concurrent revision-zero proposal",
			})
			results <- err
		})
	}
	close(start)
	writers.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if domain.AsFault(err).Code == domain.Conflict {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatal("concurrent edits lost an optimistic revision", accepted, conflicts)
	}
	for _, agent := range []string{f.founder.ID, f.worker.ID, f.other.ID} {
		page := updateReplay(t, f.s, agent, cursors[agent])
		if len(page.Items) != 1 || !updateContains(page, "note", "note_company") {
			t.Fatal("company invalidation was missing or duplicated", page)
		}
		data, _ := json.Marshal(page.Items)
		if strings.Contains(string(data), "mission") || strings.Contains(string(data), "content") {
			t.Fatal("bodyless note update disclosed content", string(data))
		}
		cursors[agent] = page.NextCursor
	}
	project := setFixtureNote(t, f.s, f.founder.ID, domain.ProjectNotes, task.ProjectID, "Project-only update")
	if page := updateReplay(t, f.s, f.worker.ID, cursors[f.worker.ID]); !updateContains(page, "note", project.ID) || page.Items[0].ProjectID != task.ProjectID {
		t.Fatal("assigned project worker missed a note update", page)
	}
	if page := updateReplay(t, f.s, f.other.ID, cursors[f.other.ID]); len(page.Items) != 0 {
		t.Fatal("unrelated worker received project-note invalidation", page)
	}
	own := setFixtureNote(t, f.s, f.worker.ID, domain.AgentNotes, "", "Own agent update")
	if page := updateReplay(t, f.s, f.worker.ID, cursors[f.worker.ID]); !updateContains(page, "note", own.ID) {
		t.Fatal("agent missed its own note invalidation", page)
	}
	if page := updateReplay(t, f.s, f.other.ID, cursors[f.other.ID]); len(page.Items) != 0 {
		t.Fatal("unrelated worker received agent-note invalidation", page)
	}
	assertUpdateChangesDrained(t, f.s)
}
