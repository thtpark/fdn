package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) Bootstrap(ctx context.Context, hash string, expiresAt int64) (domain.Invitation, error) {
	if !domain.ValidHash(hash) || expiresAt <= s.timestamp() || expiresAt > s.timestamp()+86400 {
		return domain.Invitation{}, domain.Fail(domain.Invalid, "bootstrap requires a token hash and expiry within 24 hours")
	}
	return transact(ctx, s, "bootstrap", "bootstrap", "bootstrap", hash, func(tx *sql.Tx) (domain.Invitation, error) {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents").Scan(&count); err != nil {
			return domain.Invitation{}, err
		}
		if count != 0 {
			return domain.Invitation{}, domain.Fail(domain.Conflict, "Foundation is already initialized")
		}
		inv := domain.Invitation{ID: domain.NewID("invitation"), Founder: true, ExpiresAt: expiresAt}
		data, err := encode(inv)
		if err != nil {
			return inv, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO invitations(id, data, token_hash) VALUES(?, ?, ?)", inv.ID, data, hash)
		return inv, err
	})
}

func (s *Store) Authenticate(ctx context.Context, token string) (domain.Agent, error) {
	if len(token) != 43 {
		return domain.Agent{}, domain.Fail(domain.Denied, "invalid authentication")
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM agents WHERE credential_hash = ? AND active = 1", domain.Hash(token)).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Agent{}, domain.Fail(domain.Denied, "invalid or revoked authentication")
		}
		return domain.Agent{}, err
	}
	var agent domain.Agent
	err := json.Unmarshal(data, &agent)
	return agent, err
}

func (s *Store) Enroll(ctx context.Context, req domain.EnrollRequest) (domain.Agent, error) {
	if err := domain.CheckText(req.Name, "agent name", 128, true); err != nil {
		return domain.Agent{}, err
	}
	if len(req.InvitationToken) != 43 || !domain.ValidHash(req.CredentialHash) {
		return domain.Agent{}, domain.Fail(domain.Invalid, "a valid invitation and credential hash are required")
	}
	hash := domain.Hash(req.InvitationToken)
	return transact(ctx, s, "enroll:"+hash, "enroll", req.OperationID, req, func(tx *sql.Tx) (domain.Agent, error) {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM invitations WHERE token_hash = ?", hash).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Agent{}, domain.Fail(domain.Denied, "invalid invitation")
			}
			return domain.Agent{}, err
		}
		inv, err := get[domain.Invitation](ctx, tx, "invitations", id)
		if err != nil {
			return domain.Agent{}, err
		}
		if inv.UsedBy != "" || inv.ExpiresAt <= s.timestamp() {
			return domain.Agent{}, domain.Fail(domain.Denied, "invitation has expired or was already used")
		}
		if inv.SponsorID != "" {
			sponsor, err := s.actor(ctx, tx, inv.SponsorID)
			if err != nil {
				return domain.Agent{}, domain.Fail(domain.Denied, "invitation sponsor is no longer authorized")
			}
			if ok, err := s.canInvite(ctx, tx, sponsor); err != nil || !ok {
				if err != nil {
					return domain.Agent{}, err
				}
				return domain.Agent{}, domain.Fail(domain.Denied, "invitation sponsor is no longer authorized")
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE credential_hash = ?", req.CredentialHash).Scan(&count); err != nil {
			return domain.Agent{}, err
		}
		if count != 0 {
			return domain.Agent{}, domain.Fail(domain.Conflict, "credential is already bound to an agent")
		}
		agent := domain.Agent{ID: domain.NewID("agent"), Name: req.Name, Founder: inv.Founder, Active: true, SponsorID: inv.SponsorID, CreatedAt: s.timestamp(), LastSeen: s.timestamp()}
		agent.Revision, agent.Mode = 1, "ai"
		if agent.Founder {
			agent.Mode = "manual"
		}
		data, err := encode(agent)
		if err != nil {
			return agent, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO agents(id, data, credential_hash, active) VALUES(?, ?, ?, 1)", agent.ID, data, req.CredentialHash); err != nil {
			return agent, err
		}
		inv.UsedBy = agent.ID
		data, err = encode(inv)
		if err != nil {
			return agent, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE invitations SET data = ? WHERE id = ?", data, inv.ID); err != nil {
			return agent, err
		}
		return agent, s.event(ctx, tx, "agent", agent.ID, "", "")
	})
}

func (s *Store) canInvite(ctx context.Context, q queryer, actor domain.Agent) (bool, error) {
	if actor.Founder {
		return true, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT data FROM projects")
	if err != nil {
		return false, err
	}
	projects, err := scanJSON[domain.Project](rows)
	if err != nil {
		return false, err
	}
	for _, project := range projects {
		if !project.Archived && slices.Contains(project.Delegates, actor.ID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) Invite(ctx context.Context, actor string, req domain.InvitationRequest) (domain.Invitation, error) {
	return mutate(ctx, s, actor, "invitation.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Invitation, error) {
		ok, err := s.canInvite(ctx, tx, a)
		if err != nil {
			return domain.Invitation{}, err
		}
		if !ok {
			return domain.Invitation{}, domain.Fail(domain.Denied, "only Founder or a current project delegate may invite")
		}
		if !domain.ValidHash(req.TokenHash) || req.ExpiresAt <= s.timestamp() || req.ExpiresAt > s.timestamp()+86400 {
			return domain.Invitation{}, domain.Fail(domain.Invalid, "invitation needs a token hash and expiry within 24 hours")
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM invitations WHERE token_hash = ?", req.TokenHash).Scan(&count); err != nil {
			return domain.Invitation{}, err
		}
		if count != 0 {
			return domain.Invitation{}, domain.Fail(domain.Conflict, "invitation token already exists")
		}
		inv := domain.Invitation{ID: domain.NewID("invitation"), SponsorID: a.ID, ExpiresAt: req.ExpiresAt}
		data, err := encode(inv)
		if err != nil {
			return inv, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO invitations(id, data, token_hash) VALUES(?, ?, ?)", inv.ID, data, req.TokenHash)
		return inv, err
	})
}

func (s *Store) Agents(ctx context.Context, actor string) ([]domain.Agent, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Agent](rows)
}

func (s *Store) Me(ctx context.Context, actor string) (domain.Agent, error) {
	return s.actor(ctx, s.db, actor)
}

func (s *Store) Heartbeat(ctx context.Context, actor string) (domain.Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer tx.Rollback()
	a, err := s.actor(ctx, tx, actor)
	if err != nil {
		return a, err
	}
	a.LastSeen = s.timestamp()
	data, err := encode(a)
	if err != nil {
		return a, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET data = ? WHERE id = ?", data, a.ID); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

func (s *Store) Revoke(ctx context.Context, actor, target string, req domain.AgentRequest) (domain.Agent, error) {
	return mutate(ctx, s, actor, "agent.revoke:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Agent, error) {
		if !a.Founder {
			return domain.Agent{}, domain.Fail(domain.Denied, "only Founder may revoke an agent")
		}
		agent, err := get[domain.Agent](ctx, tx, "agents", target)
		if err != nil {
			return agent, err
		}
		if agent.Founder {
			return agent, domain.Fail(domain.Denied, "Founder cannot be revoked")
		}
		agent.Active = false
		agent.Revision++
		data, err := encode(agent)
		if err != nil {
			return agent, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET data = ?, active = 0 WHERE id = ?", data, target); err != nil {
			return agent, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT data FROM tasks WHERE assignee_id = ? OR json_extract(data, '$.readiness.actor_id') = ? OR json_extract(data, '$.retry_approval.actor_id') = ?", target, target, target)
		if err != nil {
			return agent, err
		}
		tasks, err := scanJSON[domain.Task](rows)
		if err != nil {
			return agent, err
		}
		for _, task := range tasks {
			if task.ActiveRun != nil {
				task.ActiveRun.StopRequested = true
			}
			task.Readiness, task.RetryApproval = nil, nil
			if err := s.saveTask(ctx, tx, &task, a.ID, "agent-revoked"); err != nil {
				return agent, err
			}
		}
		return agent, s.event(ctx, tx, "agent", target, "", "")
	})
}

func (s *Store) CreateProject(ctx context.Context, actor string, req domain.ProjectRequest) (domain.Project, error) {
	return mutate(ctx, s, actor, "project.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Project, error) {
		if err := domain.CheckText(req.Name, "project name", 256, true); err != nil {
			return domain.Project{}, err
		}
		if err := domain.CheckText(req.Description, "project description", 32768, false); err != nil {
			return domain.Project{}, err
		}
		if !a.Founder {
			result, err := tx.ExecContext(ctx, "DELETE FROM project_creators WHERE agent_id = ?", a.ID)
			if err != nil {
				return domain.Project{}, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return domain.Project{}, err
			}
			if n == 0 {
				return domain.Project{}, domain.Fail(domain.Denied, "Founder must authorize project creation")
			}
		}
		p := domain.Project{ID: domain.NewID("project"), Name: req.Name, Description: req.Description, OwnerID: a.ID, Delegates: []string{a.ID}, Revision: 1, Epoch: 1, CreatedAt: s.timestamp()}
		data, err := encode(p)
		if err != nil {
			return p, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO projects(id, data) VALUES(?, ?)", p.ID, data); err != nil {
			return p, err
		}
		if err := s.history(ctx, tx, p.ID, a.ID, "create", p); err != nil {
			return p, err
		}
		return p, s.event(ctx, tx, "project", p.ID, "", p.ID)
	})
}

func (s *Store) GrantCreation(ctx context.Context, actor string, req domain.GrantRequest) (map[string]bool, error) {
	return mutate(ctx, s, actor, "project.creation-grant", req.Command, req, func(tx *sql.Tx, a domain.Agent) (map[string]bool, error) {
		if !a.Founder {
			return nil, domain.Fail(domain.Denied, "only Founder grants project creation")
		}
		if _, err := s.actor(ctx, tx, req.AgentID); err != nil {
			return nil, err
		}
		var err error
		if req.Revoke {
			_, err = tx.ExecContext(ctx, "DELETE FROM project_creators WHERE agent_id = ?", req.AgentID)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO project_creators(agent_id) VALUES(?) ON CONFLICT DO NOTHING", req.AgentID)
		}
		return map[string]bool{"authorized": !req.Revoke}, err
	})
}

func authority(a domain.Agent, p domain.Project) bool {
	return a.Active && (a.Founder || slices.Contains(p.Delegates, a.ID))
}

func (s *Store) Delegate(ctx context.Context, actor, projectID string, req domain.GrantRequest) (domain.Project, error) {
	return mutate(ctx, s, actor, "project.delegate:"+projectID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Project, error) {
		p, err := get[domain.Project](ctx, tx, "projects", projectID)
		if err != nil {
			return p, err
		}
		if (!req.Revoke && !a.Founder) || (req.Revoke && !authority(a, p)) {
			return p, domain.Fail(domain.Denied, "grant requires Founder; revoke requires Founder or this project's delegate")
		}
		if err := expect(p.Revision, req.ExpectedRevision); err != nil {
			return p, err
		}
		if _, err := s.actor(ctx, tx, req.AgentID); err != nil {
			return p, err
		}
		if req.Revoke {
			p.Delegates = slices.DeleteFunc(p.Delegates, func(id string) bool { return id == req.AgentID })
		} else if !slices.Contains(p.Delegates, req.AgentID) {
			p.Delegates = append(p.Delegates, req.AgentID)
			slices.Sort(p.Delegates)
		}
		p.Epoch++
		p.Revision++
		data, err := encode(p)
		if err != nil {
			return p, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET data = ? WHERE id = ?", data, p.ID); err != nil {
			return p, err
		}
		if err := s.history(ctx, tx, p.ID, a.ID, "delegate", p); err != nil {
			return p, err
		}
		return p, s.event(ctx, tx, "project", p.ID, "", p.ID)
	})
}

func (s *Store) Projects(ctx context.Context, actor string) ([]domain.Project, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Project](rows)
}

func (s *Store) Project(ctx context.Context, actor, id string) (domain.Project, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Project{}, err
	}
	return get[domain.Project](ctx, s.db, "projects", id)
}
