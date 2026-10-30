package work

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (e *Engine) refreshNotes(ctx context.Context, launch *Launch) error {
	project := ""
	switch {
	case launch.Task != nil:
		project = launch.Task.ProjectID
	case launch.Project != nil:
		project = launch.Project.ID
	case launch.Message != nil && launch.Message.Request != nil:
		project = launch.Message.Request.Action.ProjectID
	}
	query := url.Values{"project": {project}}
	notes, err := api.Call[[]domain.Note](ctx, e.Client, http.MethodGet, "/v1/note-context?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	expected := []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes}}
	if project != "" {
		expected = append(expected, domain.Note{ID: "note_" + project, Scope: domain.ProjectNotes, TargetID: project})
	}
	actor := e.Client.Credential.AgentID
	expected = append(expected, domain.Note{ID: "note_" + actor, Scope: domain.AgentNotes, TargetID: actor})
	if len(notes) != len(expected) {
		return domain.Fail(domain.Prerequisite, "shared notes context is incomplete; verify the current server before starting work")
	}
	for i, note := range notes {
		if note.ID != expected[i].ID || note.Scope != expected[i].Scope || note.TargetID != expected[i].TargetID ||
			note.Revision < 0 || (note.Revision == 0 && note.Content != "") {
			return domain.Fail(domain.Prerequisite, "shared notes context does not match the current company, project and agent")
		}
		if err := domain.CheckText(note.Content, "shared notes context", domain.MaxNoteContent, false); err != nil {
			return err
		}
	}
	launch.SharedNotes = notes
	for _, referenced := range launch.ReferencedProjects {
		if referenced.ID == project || slices.ContainsFunc(launch.SharedNotes, func(note domain.Note) bool {
			return note.Scope == domain.ProjectNotes && note.TargetID == referenced.ID
		}) {
			continue
		}
		context, err := api.Call[[]domain.Note](ctx, e.Client, http.MethodGet, "/v1/note-context?"+url.Values{"project": {referenced.ID}}.Encode(), nil)
		if err != nil {
			return err
		}
		if len(context) != 3 || context[1].Scope != domain.ProjectNotes || context[1].TargetID != referenced.ID ||
			context[1].ID != "note_"+referenced.ID || context[1].Revision < 0 ||
			(context[1].Revision == 0 && context[1].Content != "") {
			return domain.Fail(domain.Prerequisite, "referenced project notes context is incomplete")
		}
		if err := domain.CheckText(context[1].Content, "referenced project notes", domain.MaxNoteContent, false); err != nil {
			return err
		}
		launch.SharedNotes = append(launch.SharedNotes, context[1])
	}
	return nil
}
