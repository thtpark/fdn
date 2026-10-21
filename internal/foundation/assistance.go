package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) grantAuthority(ctx context.Context, q queryer, grant domain.AssistanceGrant) error {
	if !grant.Active {
		return domain.Fail(domain.Denied, "assistance grant was revoked")
	}
	issuer, err := s.actor(ctx, q, grant.CreatedBy)
	if err != nil {
		return err
	}
	if !issuer.Founder {
		return domain.Fail(domain.Denied, "assistance grant has no current Founder authorization")
	}
	requester, err := s.actor(ctx, q, grant.RequesterID)
	if err != nil {
		return err
	}
	if _, err := s.actor(ctx, q, grant.TargetID); err != nil {
		return err
	}
	if grant.ProjectID != "" {
		project, err := get[domain.Project](ctx, q, "projects", grant.ProjectID)
		if err != nil {
			return err
		}
		if project.Archived || !authority(requester, project) {
			return domain.Fail(domain.Denied, "project-scoped assistance requires current project authority")
		}
	}
	return nil
}

func (s *Store) CreateAssistance(ctx context.Context, actor string, req domain.AssistanceGrantRequest) (domain.AssistanceGrant, error) {
	return mutate(ctx, s, actor, "assistance.grant", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AssistanceGrant, error) {
		var grant domain.AssistanceGrant
		if !a.Founder {
			return grant, domain.Fail(domain.Denied, "only Founder may issue persistent assistance authorization")
		}
		if req.Mode != "tmux" && req.Mode != "vnc" {
			return grant, domain.Fail(domain.Invalid, "assistance mode must be tmux or vnc")
		}
		if req.Purpose == "" {
			req.Purpose = "authentication"
		}
		if req.Purpose != "authentication" && req.Purpose != "terminal" {
			return grant, domain.Fail(domain.Invalid, "assistance purpose must be authentication or terminal")
		}
		if (req.Provider != "" && !domain.ValidProvider(req.Provider)) || (req.Purpose != "authentication" && req.Provider != "") {
			return grant, domain.Fail(domain.Invalid, "provider restriction requires a supported authentication provider")
		}
		if err := domain.CheckText(req.Reason, "assistance authorization reason", 8192, true); err != nil {
			return grant, err
		}
		grant = domain.AssistanceGrant{ID: domain.NewID("assistance"), RequesterID: req.RequesterID, TargetID: req.TargetID, ProjectID: req.ProjectID,
			Mode: req.Mode, Purpose: req.Purpose, Provider: req.Provider, Active: true, Revision: 1, CreatedBy: a.ID, CreatedAt: s.timestamp(), UpdatedAt: s.timestamp(), Reason: req.Reason}
		if err := s.grantAuthority(ctx, tx, grant); err != nil {
			return grant, err
		}
		data, err := encode(grant)
		if err != nil {
			return grant, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO assistance_grants(id,data,requester_id,target_id,active) VALUES(?,?,?,?,1)", grant.ID, data, grant.RequesterID, grant.TargetID); err != nil {
			return grant, err
		}
		return grant, s.history(ctx, tx, grant.ID, a.ID, "grant", grant, req.Reason)
	})
}

func (s *Store) Assistance(ctx context.Context, actor, id string) (domain.AssistanceGrant, error) {
	a, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return domain.AssistanceGrant{}, err
	}
	grant, err := get[domain.AssistanceGrant](ctx, s.db, "assistance_grants", id)
	if err != nil {
		return grant, err
	}
	if !a.Founder && a.ID != grant.RequesterID && a.ID != grant.TargetID {
		return domain.AssistanceGrant{}, domain.Fail(domain.Denied, "assistance authorization is private to its parties and Founder")
	}
	return grant, nil
}

func (s *Store) AssistanceList(ctx context.Context, actor string) ([]domain.AssistanceGrant, error) {
	a, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	query := "SELECT data FROM assistance_grants WHERE active = 1"
	args := []any{}
	if !a.Founder {
		query += " AND (requester_id = ? OR target_id = ?)"
		args = append(args, a.ID, a.ID)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id LIMIT 200", args...)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.AssistanceGrant](rows)
}

func (s *Store) RevokeAssistance(ctx context.Context, actor, id string, req domain.AssistanceRevoke) (domain.AssistanceGrant, error) {
	return mutate(ctx, s, actor, "assistance.revoke:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AssistanceGrant, error) {
		if !a.Founder {
			return domain.AssistanceGrant{}, domain.Fail(domain.Denied, "only Founder may revoke persistent assistance authorization")
		}
		grant, err := get[domain.AssistanceGrant](ctx, tx, "assistance_grants", id)
		if err != nil {
			return grant, err
		}
		if err := expect(grant.Revision, req.ExpectedRevision); err != nil {
			return grant, err
		}
		if err := domain.CheckText(req.Reason, "revocation reason", 8192, true); err != nil {
			return grant, err
		}
		grant.Active, grant.Reason, grant.UpdatedAt = false, req.Reason, s.timestamp()
		grant.Revision++
		data, err := encode(grant)
		if err != nil {
			return grant, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE assistance_grants SET data = ?, active = 0 WHERE id = ?", data, grant.ID); err != nil {
			return grant, err
		}
		return grant, s.history(ctx, tx, grant.ID, a.ID, "revoke", grant, req.Reason)
	})
}
