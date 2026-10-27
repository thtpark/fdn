package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) prepareMessageAction(ctx context.Context, tx *sql.Tx, sender domain.Agent, req domain.MessageRequest) (*domain.MessageActionState, bool, error) {
	if req.Request == nil && req.Kind != "request" && !req.Insistence {
		return nil, false, nil
	}
	instance, err := identityInstance(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	system := req.RecipientID == instance
	if req.Request == nil {
		if req.Kind == "request" || system || req.Insistence {
			return nil, false, domain.Fail(domain.Invalid, "operational requests require an explicit bounded request descriptor")
		}
		return nil, false, nil
	}
	action := *req.Request
	if req.Kind != "request" || !req.ResponseRequest || (action.Kind != "work" && action.Kind != "upgrade") || len(action.Dependencies) > 32 {
		return nil, false, domain.Fail(domain.Invalid, "requests require kind=request, a requested response and supported work/upgrade scope")
	}
	if action.Kind != "upgrade" && (action.Upgrade != nil || len(action.Dependencies) != 0) {
		return nil, false, domain.Fail(domain.Invalid, "only exact native upgrade requests may carry permits or rollout dependencies")
	}
	if action.Kind == "upgrade" && (action.ProjectID != "" || action.TaskID != "") {
		return nil, false, domain.Fail(domain.Invalid, "native upgrades cannot substitute for project task execution")
	}
	generation, active, err := agentOperationIdentity(ctx, tx, sender)
	if err != nil {
		return nil, false, err
	}
	if !active {
		return nil, false, domain.Fail(domain.Denied, "request sender identity is not current")
	}
	state := &domain.MessageActionState{Action: action, State: "queued", SenderGeneration: generation, UpdatedAt: s.timestamp()}
	if !system {
		target, err := activeIdentitySubject(ctx, tx, req.RecipientID)
		if err != nil {
			return nil, false, err
		}
		state.RecipientGeneration, active, err = agentOperationIdentity(ctx, tx, target)
		if err != nil {
			return nil, false, err
		}
		if !active {
			return nil, false, domain.Fail(domain.Denied, "request target identity is not current")
		}
	}
	if action.ProjectID != "" {
		project, err := get[domain.Project](ctx, tx, "projects", action.ProjectID)
		if err != nil {
			return nil, false, err
		}
		if project.Archived || !authority(sender, project) {
			return nil, false, domain.Fail(domain.Denied, "request requires current project authority")
		}
		state.ProjectEpoch = project.Epoch
	} else if !sender.Founder {
		return nil, false, domain.Fail(domain.Denied, "non-Founder operational requests require current scoped project delegation")
	}
	if action.TaskID != "" {
		task, err := get[domain.Task](ctx, tx, "tasks", action.TaskID)
		if err != nil {
			return nil, false, err
		}
		if task.ProjectID != action.ProjectID || task.AssigneeID != req.RecipientID || task.ArchivedAt != 0 {
			return nil, false, domain.Fail(domain.Denied, "work request must name the target's current unarchived task and project")
		}
		state.SpecGeneration, state.ResultGeneration = task.SpecGeneration, task.ResultGeneration
	}
	if action.Kind == "upgrade" {
		permit := action.Upgrade
		if !sender.Founder || permit == nil || permit.ActorID != sender.ID || permit.ActorGeneration != generation || permit.ExpiresAt <= s.timestamp() {
			return nil, false, domain.Fail(domain.Denied, "upgrades require the issuing current Founder's exact unexpired native authorization")
		}
		challenge := permit.Proposal.Challenge
		if challenge.InstanceID != instance || challenge.TargetID != req.RecipientID ||
			(system && challenge.Role != "foundation") || (!system && (challenge.Role != "agent" || challenge.Generation != state.RecipientGeneration)) {
			return nil, false, domain.Fail(domain.Denied, "upgrade request permit belongs to another exact target/generation")
		}
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "upgrade-permit:"+domain.Hash(permit.Signature)).Scan(&raw); err != nil {
			return nil, false, err
		}
		var stored domain.UpgradePermit
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return nil, false, err
		}
		if !reflect.DeepEqual(stored, *permit) {
			return nil, false, domain.Fail(domain.Denied, "request changed the original native authorization")
		}
		for _, id := range action.Dependencies {
			dependency, err := get[domain.Message](ctx, tx, "messages", id)
			if err != nil {
				return nil, false, err
			}
			if dependency.SenderID != sender.ID || dependency.Request == nil || dependency.Request.Action.Kind != "upgrade" || dependency.Request.Action.Upgrade == nil ||
				dependency.Request.Action.Upgrade.Proposal.Release.Version != permit.Proposal.Release.Version ||
				dependency.Request.Action.Upgrade.Proposal.Release.Commit != permit.Proposal.Release.Commit {
				return nil, false, domain.Fail(domain.Denied, "rollout dependencies require the same issuer and exact software release")
			}
		}
	} else if system {
		return nil, false, domain.Fail(domain.Invalid, "Foundation handles only explicitly authorized deterministic maintenance requests")
	}
	return state, system, nil
}

func (s *Store) messageActionCurrent(ctx context.Context, tx *sql.Tx, m domain.Message, next string) error {
	if m.Request == nil {
		return domain.Fail(domain.Invalid, "message has no operational request")
	}
	sender, err := activeIdentitySubject(ctx, tx, m.SenderID)
	if err != nil {
		return err
	}
	generation, active, err := agentOperationIdentity(ctx, tx, sender)
	if err != nil {
		return err
	}
	if !active || generation != m.Request.SenderGeneration {
		return domain.Fail(domain.Denied, "request issuer identity generation changed")
	}
	instance, err := identityInstance(ctx, tx)
	if err != nil {
		return err
	}
	if m.RecipientID != instance {
		target, err := activeIdentitySubject(ctx, tx, m.RecipientID)
		if err != nil {
			return err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, target)
		if err != nil {
			return err
		}
		if !active || generation != m.Request.RecipientGeneration {
			return domain.Fail(domain.Denied, "request target identity generation changed")
		}
	}
	action := m.Request.Action
	if action.ProjectID != "" {
		project, err := get[domain.Project](ctx, tx, "projects", action.ProjectID)
		if err != nil {
			return err
		}
		if project.Archived || project.Epoch != m.Request.ProjectEpoch || !authority(sender, project) {
			return domain.Fail(domain.Denied, "request project authority or epoch changed")
		}
	} else if !sender.Founder {
		return domain.Fail(domain.Denied, "request issuer no longer has Founder authority")
	}
	if action.TaskID != "" {
		task, err := get[domain.Task](ctx, tx, "tasks", action.TaskID)
		if err != nil {
			return err
		}
		if task.ProjectID != action.ProjectID || task.AssigneeID != m.RecipientID ||
			task.SpecGeneration != m.Request.SpecGeneration || task.ArchivedAt != 0 {
			return domain.Fail(domain.Conflict, "request task assignment, specification or archive state changed")
		}
		if next == "accepted" {
			project, err := get[domain.Project](ctx, tx, "projects", task.ProjectID)
			if err != nil {
				return err
			}
			approved, err := s.validApproval(ctx, tx, project, task, task.Readiness, false)
			if err != nil {
				return err
			}
			if !approved || task.Status != domain.Todo || task.Repetitive || task.ActiveRun != nil || task.Waiting != nil || task.WorkspaceRef == "" {
				return domain.Fail(domain.Prerequisite, "accepting task execution requires current readiness, assignment, workspace and resolved blockers")
			}
		}
		if next == "executing" && (task.ActiveRun == nil || task.ActiveRun.Kind == domain.VerificationRun ||
			task.ActiveRun.AgentID != m.RecipientID || task.ActiveRun.SpecGeneration != m.Request.SpecGeneration || task.ActiveRun.StopRequested) {
			return domain.Fail(domain.Prerequisite, "request execution requires the linked task's actual current execution admission")
		}
	}
	if action.Kind == "upgrade" && (!sender.Founder || action.Upgrade == nil ||
		(next != "completed" && action.Upgrade.ExpiresAt <= s.timestamp())) {
		return domain.Fail(domain.Denied, "upgrade request authority expired or changed; no automatic renewal")
	}
	return nil
}

func (s *Store) ReportMessageAction(ctx context.Context, actor, id string, req domain.MessageActionReport) (domain.Message, error) {
	return mutate(ctx, s, actor, "message.request:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Message, error) {
		return s.reportMessageAction(ctx, tx, a.ID, id, req)
	})
}

func (s *Store) ReportSystemMessageAction(ctx context.Context, id string, req domain.MessageActionReport) (domain.Message, error) {
	return transact(ctx, s, "native-maintenance", "message.request:"+id, req.OperationID, req, func(tx *sql.Tx) (domain.Message, error) {
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return domain.Message{}, err
		}
		return s.reportMessageAction(ctx, tx, instance, id, req)
	})
}

func (s *Store) reportMessageAction(ctx context.Context, tx *sql.Tx, actor, id string, req domain.MessageActionReport) (domain.Message, error) {
	m, err := get[domain.Message](ctx, tx, "messages", id)
	if err != nil {
		return m, err
	}
	if m.RecipientID != actor || m.Request == nil {
		return m, domain.Fail(domain.Denied, "only the exact target may decide or report its request")
	}
	instance, err := identityInstance(ctx, tx)
	if err != nil {
		return m, err
	}
	if actor != instance {
		target, err := activeIdentitySubject(ctx, tx, actor)
		if err != nil {
			return m, err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, target)
		if err != nil {
			return m, err
		}
		if !active || generation != m.Request.RecipientGeneration {
			return m, domain.Fail(domain.Denied, "request target key generation changed")
		}
	}
	if err := expect(m.Revision, req.ExpectedRevision); err != nil {
		return m, err
	}
	if err := domain.CheckText(req.Reason, "request decision/result reason", 8192, true); err != nil {
		return m, err
	}
	allowed := map[string][]string{
		"queued":    {"accepted", "deferred", "declined"},
		"accepted":  {"executing", "deferred", "completed", "failed"},
		"executing": {"completed", "failed"},
	}
	if !slices.Contains(allowed[m.Request.State], req.State) {
		return m, domain.Fail(domain.Conflict, "request decision/result is not a valid next state; a follow-up is not a force/retry")
	}
	if req.State == "accepted" || req.State == "executing" || req.State == "completed" {
		if err := s.messageActionCurrent(ctx, tx, m, req.State); err != nil {
			return m, err
		}
	}
	if req.State == "accepted" {
		for _, dependency := range m.Request.Action.Dependencies {
			previous, err := get[domain.Message](ctx, tx, "messages", dependency)
			if err != nil {
				return m, err
			}
			if previous.Request == nil || previous.Request.State != "completed" {
				return m, domain.Fail(domain.Prerequisite, "an earlier requested maintenance result is not complete")
			}
		}
	}
	if req.State == "completed" && m.Request.Action.TaskID != "" {
		task, err := get[domain.Task](ctx, tx, "tasks", m.Request.Action.TaskID)
		if err != nil {
			return m, err
		}
		if task.AssigneeID != actor || task.SpecGeneration != m.Request.SpecGeneration ||
			task.ResultGeneration <= m.Request.ResultGeneration || task.Result == nil || task.Result.SpecGeneration != m.Request.SpecGeneration ||
			task.Result.RunID != task.LastRunID || task.Status == domain.CancelledStatus {
			return m, domain.Fail(domain.Conflict, "a message acceptance/reply is not a new completed task result")
		}
	}
	m.Request.State, m.Request.Reason, m.Request.UpdatedAt = req.State, req.Reason, s.timestamp()
	m.Revision++
	if slices.Contains([]string{"deferred", "declined", "completed", "failed"}, req.State) {
		m.State = "handled"
	}
	raw, err := encode(m)
	if err != nil {
		return m, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE messages SET data=?,state=? WHERE id=?", raw, m.State, m.ID); err != nil {
		return m, err
	}
	reply := domain.Message{ID: domain.NewID("message"), SenderID: actor, RecipientID: m.SenderID,
		Body: req.Reason, Origin: "service", Kind: "request-update", State: "pending", CreatedAt: s.timestamp(), Revision: 1,
		ThreadID: m.ThreadID, ReplyToID: m.ID, RequestUpdate: &domain.MessageActionUpdate{RequestID: m.ID, State: req.State, Reason: req.Reason}}
	if _, err := insertMessage(ctx, tx, reply); err != nil {
		return m, err
	}
	return m, nil
}

func (s *Store) MessageThread(ctx context.Context, actor, id string) ([]domain.Message, error) {
	message, err := s.Message(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	thread := message.ThreadID
	if thread == "" {
		thread = message.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM messages WHERE (id=? OR json_extract(data,'$.thread_id')=?)
AND (sender_id=? OR recipient_id=?) ORDER BY rowid LIMIT 201`, thread, thread, actor, actor)
	if err != nil {
		return nil, err
	}
	items, err := scanJSON[domain.Message](rows)
	if err != nil {
		return nil, err
	}
	if len(items) > 200 {
		return nil, domain.Fail(domain.Prerequisite, "conversation exceeds the bounded thread view; inspect exact message IDs instead")
	}
	return items, nil
}

func (s *Store) SystemMessageRequests(ctx context.Context) ([]domain.Message, error) {
	var instance string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='instance_id'").Scan(&instance); err != nil {
		return nil, err
	}
	return s.nativeMessageRequests(ctx, instance)
}

func (s *Store) NativeMessageRequests(ctx context.Context, actor string) ([]domain.Message, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	return s.nativeMessageRequests(ctx, actor)
}

func (s *Store) nativeMessageRequests(ctx context.Context, recipient string) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.data FROM messages m WHERE m.recipient_id=? AND m.state='pending'
AND json_extract(m.data,'$.request.action.kind')='upgrade'
AND (NOT EXISTS (
 SELECT 1 FROM json_each(COALESCE(json_extract(m.data,'$.request.action.dependencies'),'[]')) d
 LEFT JOIN messages p ON p.id=d.value
 WHERE COALESCE(json_extract(p.data,'$.request.state'),'')<>'completed'
) OR EXISTS (
 SELECT 1 FROM json_each(COALESCE(json_extract(m.data,'$.request.action.dependencies'),'[]')) d
 JOIN messages p ON p.id=d.value
 WHERE json_extract(p.data,'$.request.state') IN ('deferred','declined','failed')
) OR json_extract(m.data,'$.request.action.upgrade.expires_at')<=?)
ORDER BY m.id LIMIT 32`, recipient, s.timestamp())
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.Message](rows)
}
