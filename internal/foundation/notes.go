package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

var notesSchema = `
CREATE TABLE notes (id TEXT PRIMARY KEY, data BLOB NOT NULL);
` + dataUpdateTriggers("notes", "OLD.data IS NOT NEW.data")

func noteTarget(scope, target, actor string) string {
	if scope == domain.AgentNotes && target == "" {
		return actor
	}
	return target
}

func readNote(ctx context.Context, q queryer, scope, target string) (domain.Note, error) {
	id, err := domain.NoteID(scope, target)
	note := domain.Note{ID: id, Scope: scope, TargetID: target}
	if err != nil {
		return note, err
	}
	switch scope {
	case domain.ProjectNotes:
		if _, err := get[domain.Project](ctx, q, "projects", target); err != nil {
			return note, err
		}
	case domain.AgentNotes:
		if _, err := get[domain.Agent](ctx, q, "agents", target); err != nil {
			return note, err
		}
	}
	var data []byte
	err = q.QueryRowContext(ctx, "SELECT data FROM notes WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return note, nil
	}
	if err != nil {
		return note, err
	}
	err = json.Unmarshal(data, &note)
	return note, err
}

func requireNoteAuthority(ctx context.Context, tx *sql.Tx, actor domain.Agent, note domain.Note) error {
	switch note.Scope {
	case domain.CompanyNotes:
		if actor.Founder {
			return nil
		}
	case domain.ProjectNotes:
		project, err := get[domain.Project](ctx, tx, "projects", note.TargetID)
		if err != nil {
			return err
		}
		if actor.Founder || (!project.Archived && authority(actor, project)) {
			return nil
		}
	case domain.AgentNotes:
		target, err := activeIdentitySubject(ctx, tx, note.TargetID)
		if err != nil {
			return err
		}
		if actor.Founder || actor.ID == target.ID {
			return nil
		}
		if !target.Founder {
			var delegated bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM tasks t JOIN projects p ON p.id=t.project_id
 WHERE t.assignee_id=? AND t.archived=0 AND t.status NOT IN (?,?,?)
 AND json_extract(p.data,'$.archived')=0
 AND EXISTS(SELECT 1 FROM json_each(p.data,'$.delegates') WHERE value=?)
)`, target.ID, domain.Done, domain.WontDo, domain.CancelledStatus, actor.ID).Scan(&delegated)
			if err != nil {
				return err
			}
			if delegated {
				return nil
			}
		}
	}
	return domain.Fail(domain.Denied, "notes require Founder, current authority for this project, or self/current project-assignment authority for this non-Founder agent")
}

func (s *Store) SetNote(ctx context.Context, actor string, req domain.NoteRequest) (domain.Note, error) {
	return mutate(ctx, s, actor, "note.set", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Note, error) {
		var zero domain.Note
		if req.ExpectedRevision < 0 || req.Content == nil {
			return zero, domain.Fail(domain.Invalid, "notes require content (empty clears it) and a nonnegative expected_revision")
		}
		if err := domain.CheckText(*req.Content, "shared notes", domain.MaxNoteContent, false); err != nil {
			return zero, err
		}
		if err := domain.CheckText(req.Reason, "notes change reason", 8192, true); err != nil {
			return zero, err
		}
		note, err := readNote(ctx, tx, req.Scope, noteTarget(req.Scope, req.TargetID, a.ID))
		if err != nil {
			return zero, err
		}
		if err := requireNoteAuthority(ctx, tx, a, note); err != nil {
			return zero, err
		}
		if note.Revision != req.ExpectedRevision {
			return zero, domain.Fail(domain.Conflict, "notes revision changed; read the current document before editing")
		}
		if note.Revision == 0 {
			note.CreatedBy, note.CreatedAt = a.ID, s.timestamp()
		}
		note.Content, note.Revision = *req.Content, note.Revision+1
		note.UpdatedBy, note.UpdatedAt = a.ID, s.timestamp()
		data, err := encode(note)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notes(id,data) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, note.ID, data); err != nil {
			return zero, err
		}
		if err := s.history(ctx, tx, note.ID, a.ID, "set", note, req.Reason); err != nil {
			return zero, err
		}
		project := ""
		if note.Scope == domain.ProjectNotes {
			project = note.TargetID
		}
		return note, s.event(ctx, tx, "note", note.ID, "", project)
	})
}

func (s *Store) Note(ctx context.Context, actor, scope, target string) (domain.Note, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Note{}, err
	}
	return readNote(ctx, s.db, scope, noteTarget(scope, target, actor))
}

func (s *Store) NoteContext(ctx context.Context, actor, project string) (notes []domain.Note, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return nil, err
	}
	company, err := readNote(ctx, snapshot, domain.CompanyNotes, "")
	if err != nil {
		return nil, err
	}
	notes = []domain.Note{company}
	if project != "" {
		note, err := readNote(ctx, snapshot, domain.ProjectNotes, project)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	agent, err := readNote(ctx, snapshot, domain.AgentNotes, actor)
	if err != nil {
		return nil, err
	}
	return append(notes, agent), nil
}
