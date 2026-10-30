package foundation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

func activeFounder(ctx context.Context, q queryer) (domain.Agent, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT id FROM agents WHERE active=1 AND json_extract(data,'$.founder')=1 AND coalesce(json_extract(data,'$.banned'),0)=0 ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return domain.Agent{}, err
	}
	return activeIdentitySubject(ctx, q, id)
}

func (s *Store) adhocProject(ctx context.Context, tx *sql.Tx, actor domain.Agent) (domain.Project, error) {
	project, err := get[domain.Project](ctx, tx, "projects", domain.AdhocProjectID)
	if err == nil {
		return project, nil
	}
	if domain.AsFault(err).Code != domain.NotFound {
		return project, err
	}
	founder, err := activeFounder(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return project, domain.Fail(domain.Prerequisite, "an active Founder is required to initialize the Adhoc project")
	}
	if err != nil {
		return project, err
	}
	project = domain.Project{
		ID: domain.AdhocProjectID, Name: "Adhoc", OwnerID: founder.ID, Delegates: []string{},
		Description: "Agent-created, self-assigned records of authorized standalone actions. This project grants no additional OS, project, spending or verification authority.",
		Revision:    1, Epoch: 1, CreatedAt: s.timestamp(),
	}
	data, err := encode(project)
	if err != nil {
		return project, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO projects(id,data) VALUES(?,?)", project.ID, data); err != nil {
		return project, err
	}
	if err := s.history(ctx, tx, project.ID, actor.ID, "adhoc-initialize", project); err != nil {
		return project, err
	}
	return project, s.event(ctx, tx, "project", project.ID, "", project.ID)
}
