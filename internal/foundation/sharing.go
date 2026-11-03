package foundation

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func sharingRecord(ctx context.Context, q queryer, target string) (domain.Sharing, error) {
	record, err := get[domain.Sharing](ctx, q, "sharing", target)
	if err != nil && domain.AsFault(err).Code == domain.NotFound {
		return domain.Sharing{AgentID: target, Terminals: []domain.Terminal{}, FollowExecution: true}, nil
	}
	return record, err
}

func (s *Store) saveSharing(ctx context.Context, tx *sql.Tx, a domain.Agent, record domain.Sharing, action, reason string) (domain.Sharing, error) {
	record.Revision++
	record.UpdatedAt = s.timestamp()
	record.ServerObservedAt = 0
	data, err := encode(record)
	if err != nil {
		return record, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sharing(id,data) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, record.AgentID, data); err != nil {
		return record, err
	}
	if err := s.switchSharingSessions(ctx, tx, a.ID, record); err != nil {
		return record, err
	}
	return record, s.history(ctx, tx, record.AgentID+".sharing", a.ID, action, record, reason)
}

func (s *Store) switchSharingSessions(ctx context.Context, tx *sql.Tx, actor string, record domain.Sharing) error {
	rows, err := tx.QueryContext(ctx, `SELECT data FROM sessions WHERE target_id=?
AND state IN ('consented','active') AND expires_at>? AND json_extract(data,'$.purpose')='sharing'`, record.AgentID, s.timestamp())
	if err != nil {
		return err
	}
	sessions, err := scanJSON[domain.Session](rows)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.SelectionRevision == record.SelectionRevision {
			continue
		}
		if session.InputGranted {
			return domain.Fail(domain.Prerequisite, "an active input handoff cannot change its selected resource")
		}
		if session.InputLeaseID != "" {
			lease, err := get[domain.Session](ctx, tx, "sessions", session.InputLeaseID)
			if err != nil {
				return err
			}
			if lease.State == "consented" {
				lease.State, lease.InputRequestedBy = "closed", ""
				lease.Revision++
				if err := s.saveSession(ctx, tx, lease, actor, "sharing.selection-cancel-input"); err != nil {
					return err
				}
			}
		}
		session.SelectionRevision = record.SelectionRevision
		session.InputRequestedBy = ""
		session.InputLeaseID = ""
		if session.Mode == "tmux" {
			session.TerminalID = record.TerminalID
		} else {
			session.ScreenID = record.ScreenID
			target, err := activeIdentitySubject(ctx, tx, record.AgentID)
			if err != nil {
				return err
			}
			for _, screen := range target.Screens {
				if screen.ID == record.ScreenID {
					session.DesktopFallback = screen.Scope == "desktop"
				}
			}
		}
		session.Revision++
		if err := s.saveSession(ctx, tx, session, actor, "sharing.selection"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) sharingViewer(ctx context.Context, q queryer, a domain.Agent, target string) error {
	if a.Founder || a.ID == target {
		return nil
	}
	rows, err := q.QueryContext(ctx, "SELECT data FROM assistance_grants WHERE requester_id=? AND target_id=? AND active=1", a.ID, target)
	if err != nil {
		return err
	}
	grants, err := scanJSON[domain.AssistanceGrant](rows)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.Purpose == "sharing" {
			err := s.grantAuthority(ctx, q, grant)
			if err == nil {
				return nil
			}
			if code := domain.AsFault(err).Code; code != domain.Denied && code != domain.Prerequisite && code != domain.NotFound {
				return err
			}
		}
	}
	return domain.Fail(domain.Denied, "sharing requires Founder, the target, or a current time-limited Founder approval")
}

func (s *Store) Sharing(ctx context.Context, actor, target string) (domain.Sharing, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return domain.Sharing{}, err
	}
	defer tx.Close()
	a, err := s.actor(ctx, tx, actor)
	if err != nil {
		return domain.Sharing{}, err
	}
	if _, err := activeIdentitySubject(ctx, tx, target); err != nil {
		return domain.Sharing{}, err
	}
	if err := s.sharingViewer(ctx, tx, a, target); err != nil {
		return domain.Sharing{}, err
	}
	record, err := sharingRecord(ctx, tx, target)
	if err != nil {
		return record, err
	}
	if record.ScreenID != "" {
		var protected int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE target_id=?
AND expires_at>? AND state IN ('consented','active') AND json_extract(data,'$.purpose')='authentication'
AND json_extract(data,'$.screen_id')=?`, target, s.timestamp(), record.ScreenID).Scan(&protected); err != nil {
			return record, err
		}
		record.Protected = record.Protected || protected != 0
	}
	if !a.Founder && a.ID != target {
		selected := []domain.Terminal{}
		for _, terminal := range record.Terminals {
			if terminal.ID == record.TerminalID && !terminal.Protected && !record.Protected {
				selected = append(selected, terminal)
			}
		}
		record.Terminals = selected
	}
	record.ServerObservedAt = s.timestamp()
	return record, nil
}

func (s *Store) SharingSessions(ctx context.Context, actor, target string) ([]domain.Session, error) {
	a, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	if !a.Founder && a.ID != target {
		return nil, domain.Fail(domain.Denied, "only Founder or the target may inspect all input handoffs")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM sessions WHERE target_id=?
AND state IN ('consented','active') AND expires_at>? AND json_extract(data,'$.purpose')='sharing'
ORDER BY id LIMIT 200`, target, s.timestamp())
	if err != nil {
		return nil, err
	}
	sessions, err := scanJSON[domain.Session](rows)
	for i := range sessions {
		sessions[i].ServerObservedAt = s.timestamp()
	}
	return sessions, err
}

func (s *Store) PublishSharing(ctx context.Context, actor string, req domain.SharingPublishRequest) (domain.Sharing, error) {
	return mutate(ctx, s, actor, "sharing.publish", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Sharing, error) {
		if err := domain.ValidateTerminals(req.Terminals); err != nil {
			return domain.Sharing{}, err
		}
		record, err := sharingRecord(ctx, tx, a.ID)
		if err != nil {
			return record, err
		}
		before := record
		record.Terminals = slices.Clone(req.Terminals)
		if record.FollowExecution && slices.ContainsFunc(record.Terminals, func(t domain.Terminal) bool { return t.ID == "execution" }) {
			record.TerminalID = "execution"
		}
		oldSelected, newSelected := domain.Terminal{}, domain.Terminal{}
		for _, terminal := range before.Terminals {
			if terminal.ID == before.TerminalID {
				oldSelected = terminal
			}
		}
		for _, terminal := range record.Terminals {
			if terminal.ID == record.TerminalID {
				newSelected = terminal
			}
		}
		if newSelected.ID == "" && len(record.Terminals) > 0 {
			record.TerminalID, newSelected = record.Terminals[0].ID, record.Terminals[0]
		} else if len(record.Terminals) == 0 {
			record.TerminalID = ""
		}
		if record.ScreenID == "" && len(a.Screens) > 0 {
			record.ScreenID = a.Screens[0].ID
		}
		if record.TerminalID != before.TerminalID || record.ScreenID != before.ScreenID || oldSelected != newSelected {
			record.SelectionRevision++
		}
		if slices.Equal(before.Terminals, record.Terminals) && record.TerminalID == before.TerminalID && record.ScreenID == before.ScreenID {
			return record, nil
		}
		return s.saveSharing(ctx, tx, a, record, "sharing.publish", "publish owned terminal capabilities; no work or mode authority changed")
	})
}

func (s *Store) SelectSharing(ctx context.Context, actor, target string, req domain.SharingSelectRequest) (domain.Sharing, error) {
	return mutate(ctx, s, actor, "sharing.select:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Sharing, error) {
		if !a.Founder && a.ID != target {
			return domain.Sharing{}, domain.Fail(domain.Denied, "only Founder or the target may select shared terminals/screens")
		}
		targetAgent, err := activeIdentitySubject(ctx, tx, target)
		if err != nil {
			return domain.Sharing{}, err
		}
		if err := domain.CheckText(req.Reason, "sharing selection reason", 8192, true); err != nil {
			return domain.Sharing{}, err
		}
		record, err := sharingRecord(ctx, tx, target)
		if err != nil {
			return record, err
		}
		if err := expect(record.Revision, req.ExpectedRevision); err != nil {
			return record, err
		}
		if req.Mode == "tmux" {
			if !slices.ContainsFunc(record.Terminals, func(t domain.Terminal) bool { return t.ID == req.ResourceID }) {
				return record, domain.Fail(domain.Prerequisite, "selected terminal is not currently published by the target")
			}
			record.TerminalID = req.ResourceID
			record.FollowExecution = req.ResourceID == "execution"
		} else if req.Mode == "vnc" {
			if !slices.ContainsFunc(targetAgent.Screens, func(screen domain.Screen) bool { return screen.ID == req.ResourceID }) {
				return record, domain.Fail(domain.Prerequisite, "selected screen is not currently published by the target")
			}
			record.ScreenID = req.ResourceID
		} else {
			return record, domain.Fail(domain.Invalid, "sharing selection mode must be tmux or vnc")
		}
		var input int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE target_id=? AND expires_at>?
AND state IN ('consented','active') AND json_extract(data,'$.purpose')='sharing'
AND json_extract(data,'$.input_granted')=1`, target, s.timestamp()).Scan(&input); err != nil {
			return record, err
		}
		if input != 0 {
			return record, domain.Fail(domain.Prerequisite, "revoke the current input handoff before switching the selected resource")
		}
		record.Protected = req.Protected
		record.SelectionRevision++
		return s.saveSharing(ctx, tx, a, record, "sharing.select", req.Reason)
	})
}

func (s *Store) sharingSessionAuthority(ctx context.Context, q queryer, session domain.Session) error {
	requester, err := activeIdentitySubject(ctx, q, session.RequesterID)
	if err != nil {
		return err
	}
	target, err := activeIdentitySubject(ctx, q, session.TargetID)
	if err != nil {
		return err
	}
	record, err := sharingRecord(ctx, q, target.ID)
	if err != nil {
		return err
	}
	if record.SelectionRevision != session.SelectionRevision {
		return domain.Fail(domain.Conflict, "target sharing selection changed; reconnect to the current selected resource")
	}
	if !requester.Founder {
		if session.GrantID == "" || record.Protected {
			return domain.Fail(domain.Denied, "delegate viewing needs a current approval and cannot capture protected credential UI")
		}
		grant, err := get[domain.AssistanceGrant](ctx, q, "assistance_grants", session.GrantID)
		if err != nil {
			return err
		}
		if grant.Purpose != "sharing" || grant.RequesterID != requester.ID || grant.TargetID != target.ID ||
			grant.ProjectID != session.ProjectID || (grant.Mode != "both" && grant.Mode != session.Mode) ||
			session.ExpiresAt > grant.ExpiresAt || (session.DesktopFallback && !grant.DesktopFallback) {
			return domain.Fail(domain.Denied, "sharing session is outside its exact time-limited Founder approval")
		}
		if err := s.grantAuthority(ctx, q, grant); err != nil {
			return err
		}
		if session.Mode == "vnc" {
			var protected int
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sessions
WHERE target_id=? AND expires_at>? AND state IN ('consented','active')
AND json_extract(data,'$.purpose')='authentication'
AND json_extract(data,'$.screen_id')=?`, target.ID, s.timestamp(), session.ScreenID).Scan(&protected); err != nil {
				return err
			}
			if protected != 0 {
				return domain.Fail(domain.Denied, "protected authentication UI is human Founder-only")
			}
		}
	} else if session.GrantID != "" {
		return domain.Fail(domain.Invalid, "Founder sharing uses current role authority, not a delegate approval")
	}
	if session.Mode == "tmux" {
		if session.ScreenID != "" || session.ApplicationID != "" || session.DesktopFallback {
			return domain.Fail(domain.Invalid, "terminal sharing cannot carry screen/application capture scope")
		}
		for _, terminal := range record.Terminals {
			if terminal.ID == session.TerminalID && session.TerminalID == record.TerminalID {
				if terminal.Protected && !requester.Founder {
					return domain.Fail(domain.Denied, "protected terminals are human Founder-only")
				}
				return nil
			}
		}
		return domain.Fail(domain.Prerequisite, "selected terminal is unavailable")
	}
	if session.Mode != "vnc" || record.ScreenID != session.ScreenID || session.ApplicationID != "" || session.TerminalID != "" {
		return domain.Fail(domain.Denied, "only the target-selected screen may be observed")
	}
	_, err = assistanceScreen(target, session.Mode, session.ScreenID, "", session.DesktopFallback)
	return err
}

func (s *Store) protectAuthenticationScreen(ctx context.Context, tx *sql.Tx, a domain.Agent, session domain.Session) error {
	if session.Mode != "vnc" || session.Purpose != "authentication" {
		return nil
	}
	record, err := sharingRecord(ctx, tx, session.TargetID)
	if err != nil {
		return err
	}
	if record.ScreenID != session.ScreenID || record.Protected {
		return nil
	}
	record.Protected = true
	record.SelectionRevision++
	_, err = s.saveSharing(ctx, tx, a, record, "sharing.protect", "protect credential UI before native authentication screen access")
	return err
}

func (s *Store) requestSharingSession(ctx context.Context, tx *sql.Tx, a domain.Agent, req domain.SessionRequest, session domain.Session) (domain.Session, error) {
	if req.Provider != "" || req.HelpMessageID != "" || req.TaskID != "" || req.ExpiresAt != 0 ||
		req.LifetimeSeconds < 1 || req.LifetimeSeconds > domain.MaxSharingSeconds {
		return session, domain.Fail(domain.Invalid, "sharing needs a server-timed 1-86400 second lifetime and no provider/help/task or caller expiry")
	}
	session.ExpiresAt = s.timestamp() + req.LifetimeSeconds
	if !a.Founder {
		grant, err := get[domain.AssistanceGrant](ctx, tx, "assistance_grants", req.GrantID)
		if err != nil {
			return session, err
		}
		session.ExpiresAt = min(session.ExpiresAt, grant.ExpiresAt)
	}
	if err := domain.CheckText(req.Reason, "sharing reason", 8192, true); err != nil {
		return session, err
	}
	if err := s.sharingSessionAuthority(ctx, tx, session); err != nil {
		return session, err
	}
	session.State = "consented"
	return session, s.saveSession(ctx, tx, session, a.ID, "sharing.request")
}

func (s *Store) changeSharingInput(ctx context.Context, tx *sql.Tx, a domain.Agent, session *domain.Session, action string) error {
	if (!session.Sharing() && !session.InputLease()) || !slices.Contains([]string{"consented", "active"}, session.State) {
		return domain.Fail(domain.Invalid, "input handoff requires a current unified sharing session")
	}

	switch action {
	case "request-input":
		if !a.Founder || !session.Sharing() || !session.OpenRequested {
			return domain.Fail(domain.Denied, "only Founder may ask the target to grant input")
		}
		if session.InputRequestedBy != "" {
			return domain.Fail(domain.Conflict, "input is already requested; inspect the same handoff")
		}
		if session.InputLeaseID != "" {
			previous, err := get[domain.Session](ctx, tx, "sessions", session.InputLeaseID)
			if err != nil {
				return err
			}
			if slices.Contains([]string{"consented", "active"}, previous.State) && previous.ExpiresAt > s.timestamp() {
				return domain.Fail(domain.Prerequisite, "previous input cleanup must finish before another handoff")
			}
		}
		if session.Mode == "tmux" {
			record, err := sharingRecord(ctx, tx, session.TargetID)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(record.Terminals, func(t domain.Terminal) bool { return t.ID == session.TerminalID && t.CanInput }) {
				return domain.Fail(domain.Prerequisite, "selected execution output is view-only; select an input-capable managed terminal for help")
			}
		}
		session.InputRequestedBy = a.ID
		lease := *session
		lease.ID, lease.ParentSessionID, lease.InputLeaseID = domain.NewID("handoff"), session.ID, ""
		lease.Purpose, lease.State, lease.OpenRequested = "sharing-input", "consented", true
		lease.Revision, lease.CreatedAt = 1, s.timestamp()
		if err := s.saveSession(ctx, tx, lease, a.ID, "sharing.request-input"); err != nil {
			return err
		}
		session.InputLeaseID = lease.ID
	case "grant-input":
		if !session.InputLease() || a.ID != session.TargetID || session.InputRequestedBy == "" {
			return domain.Fail(domain.Denied, "only the target may acknowledge an exact Founder-requested task-safe handoff")
		}
		founder, err := activeIdentitySubject(ctx, tx, session.InputRequestedBy)
		if err != nil || !founder.Founder {
			return errors.Join(err, domain.Fail(domain.Denied, "input request has no current Founder authority"))
		}
		var busy int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks
WHERE json_extract(data,'$.active_run.agent_id')=?`, session.TargetID).Scan(&busy); err != nil {
			return err
		}
		if busy != 0 {
			return domain.Fail(domain.Prerequisite, "active execution must reach a task-safe checkpoint before input is granted")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE target_id=? AND id<>?
AND state IN ('consented','active') AND expires_at>? AND json_extract(data,'$.input_granted')=1`,
			session.TargetID, session.ID, s.timestamp()).Scan(&busy); err != nil {
			return err
		}
		if busy != 0 {
			return domain.Fail(domain.Prerequisite, "another input handoff already owns this target")
		}
		session.InputGranted = true
		session.State = "active"
		parent, err := get[domain.Session](ctx, tx, "sessions", session.ParentSessionID)
		if err != nil {
			return err
		}
		parent.InputGranted = true
		parent.Revision++
		if err := s.saveSession(ctx, tx, parent, a.ID, "sharing.input-ready"); err != nil {
			return err
		}
	case "revoke-input":
		if !session.Sharing() || (!a.Founder && a.ID != session.TargetID) {
			return domain.Fail(domain.Denied, "only Founder or the target may revoke input")
		}
		session.InputRequestedBy, session.InputGranted = "", false
		session.InputEpoch++
		if session.InputLeaseID != "" {
			lease, err := get[domain.Session](ctx, tx, "sessions", session.InputLeaseID)
			if err != nil {
				return err
			}
			lease.InputRequestedBy, lease.InputGranted = "", false
			if lease.State == "consented" {
				lease.State = "closed"
			}
			lease.Revision++
			if err := s.saveSession(ctx, tx, lease, a.ID, "sharing.input-revoke"); err != nil {
				return err
			}
		}
	default:
		return domain.Fail(domain.Invalid, "unsupported sharing input action")
	}
	return nil
}

func (s *Store) inputLeaseAuthority(ctx context.Context, q queryer, lease domain.Session) error {
	parent, err := get[domain.Session](ctx, q, "sessions", lease.ParentSessionID)
	if err != nil {
		return err
	}
	if !parent.Sharing() || parent.InputLeaseID != lease.ID || parent.RequesterID != lease.RequesterID ||
		parent.TargetID != lease.TargetID || parent.SelectionRevision != lease.SelectionRevision ||
		parent.TerminalID != lease.TerminalID || parent.ScreenID != lease.ScreenID ||
		parent.InputRequestedBy == "" || parent.InputRequestedBy != lease.InputRequestedBy ||
		!slices.Contains([]string{"consented", "active"}, parent.State) {
		return domain.Fail(domain.Denied, "input lease is no longer the exact requested sharing handoff")
	}
	return s.sharingSessionAuthority(ctx, q, parent)
}

func (s *Store) closeSharingControl(ctx context.Context, tx *sql.Tx, actor string, session *domain.Session) error {
	if session.InputLease() {
		parent, err := get[domain.Session](ctx, tx, "sessions", session.ParentSessionID)
		if err != nil {
			return err
		}
		if parent.InputLeaseID == session.ID {
			parent.InputRequestedBy, parent.InputGranted, parent.InputLeaseID = "", false, ""
			parent.InputEpoch++
			parent.Revision++
			return s.saveSession(ctx, tx, parent, actor, "sharing.input-finished")
		}
		return nil
	}
	if session.Sharing() && session.InputLeaseID != "" {
		lease, err := get[domain.Session](ctx, tx, "sessions", session.InputLeaseID)
		if err != nil {
			return err
		}
		lease.InputRequestedBy, lease.InputGranted = "", false
		if lease.State == "consented" {
			lease.State = "closed"
		}
		lease.Revision++
		session.InputRequestedBy, session.InputGranted = "", false
		return s.saveSession(ctx, tx, lease, actor, "sharing.viewer-closed")
	}
	return nil
}

func (s *Store) createSharingApproval(ctx context.Context, tx *sql.Tx, a domain.Agent, req domain.AssistanceGrantRequest) (domain.AssistanceGrant, error) {
	if !slices.Contains([]string{"tmux", "vnc", "both"}, req.Mode) || req.DurationSeconds < 1 || req.DurationSeconds > domain.MaxSharingSeconds ||
		req.Provider != "" || req.ScreenID != "" || req.ApplicationID != "" {
		return domain.AssistanceGrant{}, domain.Fail(domain.Invalid, "sharing approval requires tmux/vnc/both and 1-86400 seconds; resources remain target-selected")
	}
	if err := domain.CheckText(req.Reason, "sharing approval reason", 8192, true); err != nil {
		return domain.AssistanceGrant{}, err
	}
	requester, err := activeIdentitySubject(ctx, tx, req.RequesterID)
	if err != nil {
		return domain.AssistanceGrant{}, err
	}
	if requester.Founder || req.RequesterID == req.TargetID {
		return domain.AssistanceGrant{}, domain.Fail(domain.Invalid, "sharing approvals are for a different delegate; Founder has current role-based viewing")
	}
	grant := domain.AssistanceGrant{ID: domain.NewID("assistance"), RequesterID: req.RequesterID, TargetID: req.TargetID,
		ProjectID: req.ProjectID, Mode: req.Mode, Purpose: "sharing", Active: true, Revision: 1, CreatedBy: a.ID,
		CreatedAt: s.timestamp(), UpdatedAt: s.timestamp(), ExpiresAt: s.timestamp() + req.DurationSeconds,
		Reason: req.Reason, DesktopFallback: req.DesktopFallback}
	if err := s.grantAuthority(ctx, tx, grant); err != nil {
		return grant, err
	}
	data, err := encode(grant)
	if err != nil {
		return grant, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO assistance_grants(id,data,requester_id,target_id,active) VALUES(?,?,?,?,1)",
		grant.ID, data, grant.RequesterID, grant.TargetID); err != nil {
		return grant, err
	}
	return grant, s.history(ctx, tx, grant.ID, a.ID, "sharing.approve", grant, req.Reason)
}
