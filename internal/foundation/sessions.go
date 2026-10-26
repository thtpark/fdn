package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) sessionAuthority(ctx context.Context, q queryer, session domain.Session) error {
	requester, err := activeIdentitySubject(ctx, q, session.RequesterID)
	if err != nil {
		return err
	}
	target, err := activeIdentitySubject(ctx, q, session.TargetID)
	if err != nil {
		return err
	}
	if _, err := assistanceScreen(target, session.Mode, session.ScreenID, session.ApplicationID, session.DesktopFallback); err != nil {
		return err
	}
	if session.GrantID != "" {
		grant, err := get[domain.AssistanceGrant](ctx, q, "assistance_grants", session.GrantID)
		if err != nil {
			return err
		}
		if grant.RequesterID != session.RequesterID || grant.TargetID != session.TargetID || grant.ProjectID != session.ProjectID ||
			grant.Mode != session.Mode || grant.Purpose != session.Purpose || (grant.Provider != "" && grant.Provider != session.Provider) ||
			grant.ScreenID != session.ScreenID || grant.ApplicationID != session.ApplicationID || grant.DesktopFallback != session.DesktopFallback {
			return domain.Fail(domain.Denied, "session is outside its persistent assistance authorization")
		}
		return s.grantAuthority(ctx, q, grant)
	}
	project, err := get[domain.Project](ctx, q, "projects", session.ProjectID)
	if err != nil {
		return err
	}
	if project.Archived || !authority(requester, project) {
		return domain.Fail(domain.Denied, "session requester no longer has project authority")
	}
	return nil
}

func (s *Store) saveSession(ctx context.Context, tx *sql.Tx, session domain.Session, actor, action string) error {
	data, err := encode(session)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(id, data, requester_id, target_id, state, expires_at) VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data, state=excluded.state`, session.ID, data, session.RequesterID, session.TargetID, session.State, session.ExpiresAt)
	if err != nil {
		return err
	}
	return s.history(ctx, tx, session.ID, actor, action, session)
}

func (s *Store) RequestSession(ctx context.Context, actor string, req domain.SessionRequest) (domain.Session, error) {
	return mutate(ctx, s, actor, "session.request", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Session, error) {
		session := domain.Session{ID: domain.NewID("session"), RequesterID: a.ID, TargetID: req.TargetID,
			ProjectID: req.ProjectID, TaskID: req.TaskID, Mode: req.Mode, Purpose: req.Purpose,
			Provider: req.Provider, Reason: req.Reason, State: "requested", ExpiresAt: req.ExpiresAt, Revision: 1, CreatedAt: s.timestamp(),
			ScreenID: req.ScreenID, ApplicationID: req.ApplicationID, DesktopFallback: req.DesktopFallback}
		session.GrantID = req.GrantID
		target, err := activeIdentitySubject(ctx, tx, req.TargetID)
		if err != nil {
			return session, err
		}
		if !target.Founder && req.GrantID == "" {
			return session, domain.Fail(domain.Prerequisite, "autonomous targets require a persistent Founder-issued assistance grant, not a local human consent step")
		}
		if err := s.sessionAuthority(ctx, tx, session); err != nil {
			return session, err
		}
		if req.Mode != "tmux" && req.Mode != "vnc" {
			return session, domain.Fail(domain.Invalid, "session mode must be tmux or vnc")
		}
		if req.Purpose != "terminal" && req.Purpose != "authentication" {
			return session, domain.Fail(domain.Invalid, "session purpose must be terminal or authentication")
		}
		if req.Purpose == "authentication" && !domain.ValidProvider(req.Provider) {
			return session, domain.Fail(domain.Invalid, "authentication handoff requires a supported provider")
		}
		if req.Purpose == "terminal" && req.Provider != "" {
			return session, domain.Fail(domain.Invalid, "terminal sessions do not select a provider")
		}
		if err := domain.CheckText(req.Reason, "session reason", 8192, true); err != nil {
			return session, err
		}
		if req.ExpiresAt < s.timestamp()+60 || req.ExpiresAt > s.timestamp()+900 {
			return session, domain.Fail(domain.Invalid, "session expiry must be 60-900 seconds from now")
		}
		if req.TaskID != "" {
			task, err := get[domain.Task](ctx, tx, "tasks", req.TaskID)
			if err != nil {
				return session, err
			}
			if task.ProjectID != req.ProjectID || task.AssigneeID != req.TargetID {
				return session, domain.Fail(domain.Denied, "task must relate this project and target worker")
			}
		}
		if session.GrantID != "" {
			session.State = "consented"
		}
		return session, s.saveSession(ctx, tx, session, a.ID, "request")
	})
}

func (s *Store) ChangeSession(ctx context.Context, actor, id string, req domain.SessionChange) (domain.Session, error) {
	return mutate(ctx, s, actor, "session."+req.Action+":"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Session, error) {
		session, err := get[domain.Session](ctx, tx, "sessions", id)
		if err != nil {
			return session, err
		}
		if session.TargetID != a.ID && session.RequesterID != a.ID {
			return session, domain.Fail(domain.Denied, "session is private to requester and target")
		}
		if err := expect(session.Revision, req.ExpectedRevision); err != nil {
			return session, err
		}
		terminal := slices.Contains([]string{"denied", "revoked", "closed", "expired", "failed"}, session.State)
		if terminal {
			return session, domain.Fail(domain.Conflict, "session is already retired")
		}
		if req.Action != "close" && req.Action != "revoke" {
			if session.ExpiresAt <= s.timestamp() {
				return session, domain.Fail(domain.Prerequisite, "session expired; request fresh consent")
			}
			if err := s.sessionAuthority(ctx, tx, session); err != nil {
				return session, err
			}
		}
		switch req.Action {
		case "open":
			if a.ID != session.RequesterID {
				return session, domain.Fail(domain.Denied, "only the requester explicitly opens access")
			}
			session.OpenRequested = true
		case "consent", "deny":
			if a.ID != session.TargetID {
				return session, domain.Fail(domain.Denied, "only the target records per-session consent")
			}
			if session.State != "requested" {
				return session, domain.Fail(domain.Conflict, "consent is already decided")
			}
			session.State = "consented"
			if req.Action == "deny" {
				session.State = "denied"
			}
		case "active":
			if a.ID != session.TargetID || !session.OpenRequested || session.State != "consented" {
				return session, domain.Fail(domain.Prerequisite, "target needs consent and an explicit requester open")
			}
			session.State = "active"
		case "revoke":
			session.State = "revoked"
		case "close":
			session.State = "closed"
		default:
			return session, domain.Fail(domain.Invalid, "unsupported session action")
		}
		session.Revision++
		return session, s.saveSession(ctx, tx, session, a.ID, req.Action)
	})
}

func (s *Store) Session(ctx context.Context, actor, id string) (domain.Session, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Session{}, err
	}
	session, err := get[domain.Session](ctx, s.db, "sessions", id)
	if err != nil {
		return session, err
	}
	if actor != session.RequesterID && actor != session.TargetID {
		return domain.Session{}, domain.Fail(domain.Denied, "session is private to requester and target")
	}
	if session.ExpiresAt <= s.timestamp() && slices.Contains([]string{"requested", "consented", "active"}, session.State) {
		session.State = "expired"
	}
	return session, nil
}

func (s *Store) SessionAccess(ctx context.Context, actor, id, side string) (domain.Session, error) {
	session, err := s.Session(ctx, actor, id)
	if err != nil {
		return session, err
	}
	if (side == "target" && actor != session.TargetID) || (side == "requester" && actor != session.RequesterID) || (side != "target" && side != "requester") {
		return session, domain.Fail(domain.Denied, "wrong relay side")
	}
	if !session.OpenRequested || (session.State != "consented" && session.State != "active") {
		return session, domain.Fail(domain.Prerequisite, "relay requires unexpired consent and explicit requester open")
	}
	return session, s.sessionAuthority(ctx, s.db, session)
}

func (s *Store) Sessions(ctx context.Context, actor string) ([]domain.Session, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM sessions WHERE (target_id = ? OR requester_id = ?) AND state IN ('requested','consented','active') AND expires_at > ? ORDER BY id LIMIT 200", actor, actor, s.timestamp())
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Session](rows)
}

func (s *Store) RetireSessions(ctx context.Context) error {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	rows, err := tx.QueryContext(ctx, "SELECT data FROM sessions WHERE state IN ('consented','active')")
	if err != nil {
		return err
	}
	sessions, err := scanJSON[domain.Session](rows)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		session.State = "closed"
		session.Revision++
		if err := s.saveSession(ctx, tx, session, "foundation", "restart-retirement"); err != nil {
			return err
		}
	}
	return s.commit(ctx, tx)
}
