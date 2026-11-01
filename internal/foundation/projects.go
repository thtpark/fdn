package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) ChangeProject(ctx context.Context, actor, id string, req domain.ProjectChange) (domain.Project, error) {
	return mutate(ctx, s, actor, "project.change:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Project, error) {
		p, err := get[domain.Project](ctx, tx, "projects", id)
		if err != nil {
			return p, err
		}
		if err := requireAuthority(a, p); err != nil {
			return p, err
		}
		if err := expect(p.Revision, req.ExpectedRevision); err != nil {
			return p, err
		}
		if err := domain.CheckText(req.Reason, "project change reason", 8192, true); err != nil {
			return p, err
		}
		if req.Name == nil && req.Description == nil && req.Archived == nil {
			return p, domain.Fail(domain.Invalid, "project change must include an editable field")
		}
		if req.Name != nil {
			if p.ID == domain.AdhocProjectID && *req.Name != "Adhoc" {
				return p, domain.Fail(domain.Invalid, "the reserved Adhoc project's name is fixed")
			}
			p.Name = *req.Name
		}
		if req.Description != nil {
			p.Description = *req.Description
		}
		if req.Archived != nil {
			p.Archived = *req.Archived
		}
		if err := domain.CheckText(p.Name, "project name", 256, true); err != nil {
			return p, err
		}
		if err := domain.CheckText(p.Description, "project description", 32768, false); err != nil {
			return p, err
		}
		p.Revision++
		p.Epoch++
		data, err := encode(p)
		if err != nil {
			return p, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET data = ? WHERE id = ?", data, p.ID); err != nil {
			return p, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT data FROM tasks WHERE project_id = ? AND json_extract(data,'$.active_run') IS NOT NULL", p.ID)
		if err != nil {
			return p, err
		}
		tasks, err := scanJSON[domain.Task](rows)
		if err != nil {
			return p, err
		}
		for _, task := range tasks {
			task.ActiveRun.StopRequested = true
			if err := s.saveTask(ctx, tx, &task, a.ID, "project-changed", req.Reason); err != nil {
				return p, err
			}
		}
		if err := s.history(ctx, tx, p.ID, a.ID, "change", p, req.Reason); err != nil {
			return p, err
		}
		return p, s.event(ctx, tx, "project", p.ID, "", p.ID)
	})
}
