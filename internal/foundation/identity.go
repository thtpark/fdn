package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) Agents(ctx context.Context, actor string) ([]domain.Agent, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	agents, err := scanJSON[domain.Agent](rows)
	if err != nil {
		return nil, err
	}
	now := s.timestamp()
	for i := range agents {
		agents[i], err = s.projectAgentStatus(ctx, s.db, agents[i], now)
		if err != nil {
			return nil, err
		}
	}
	return agents, nil
}

func (s *Store) Me(ctx context.Context, actor string) (domain.Agent, error) {
	a, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return a, err
	}
	return s.projectAgentStatus(ctx, s.db, a, s.timestamp())
}

func (s *Store) Heartbeat(ctx context.Context, actor string) (domain.Agent, error) {
	return s.HeartbeatWithHealth(ctx, actor, nil)
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
		if err := revokeIdentity(ctx, tx, target); err != nil {
			return agent, err
		}
		if err := s.stopIdentityWork(ctx, tx, target, a.ID, "agent-revoked"); err != nil {
			return agent, err
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
		if _, err := activeIdentitySubject(ctx, tx, req.AgentID); err != nil {
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
		if _, err := activeIdentitySubject(ctx, tx, req.AgentID); err != nil {
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
