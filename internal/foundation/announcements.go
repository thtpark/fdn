package foundation

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func announcementAuthority(ctx context.Context, q queryer, actor domain.Agent, scope, projectID string) (int64, error) {
	if scope == "project" {
		project, err := get[domain.Project](ctx, q, "projects", projectID)
		if err != nil {
			return 0, err
		}
		if project.Archived || !authority(actor, project) {
			return 0, domain.Fail(domain.Denied, "project announcements require current authority for that active project")
		}
		return project.Epoch, nil
	}
	if scope != "global" || projectID != "" {
		return 0, domain.Fail(domain.Invalid, "choose global or an exact project announcement scope")
	}
	if actor.Founder {
		return 0, nil
	}
	var delegated bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects p
WHERE json_extract(p.data,'$.archived')=0
AND EXISTS(SELECT 1 FROM json_each(p.data,'$.delegates') WHERE value=?))`, actor.ID).Scan(&delegated)
	if err != nil {
		return 0, err
	}
	if !delegated {
		return 0, domain.Fail(domain.Denied, "global announcements require Founder or a current active-project delegate")
	}
	return 0, nil
}

func announcementAudience(ctx context.Context, q queryer, actor domain.Agent, scope, projectID string) (domain.AnnouncementAudience, error) {
	result := domain.AnnouncementAudience{ActorID: actor.ID, Scope: scope, ProjectID: projectID, Recipients: []domain.AnnouncementRecipient{}}
	epoch, err := announcementAuthority(ctx, q, actor, scope, projectID)
	if err != nil {
		return result, err
	}
	result.ProjectEpoch = epoch
	members := map[string]bool{}
	if scope == "project" {
		project, err := get[domain.Project](ctx, q, "projects", projectID)
		if err != nil {
			return result, err
		}
		members[project.OwnerID] = true
		for _, id := range project.Delegates {
			members[id] = true
		}
		rows, err := q.QueryContext(ctx, `SELECT data FROM tasks WHERE project_id=? AND archived=0 AND status NOT IN (?,?,?)`,
			projectID, domain.Done, domain.WontDo, domain.CancelledStatus)
		if err != nil {
			return result, err
		}
		tasks, err := scanJSON[domain.Task](rows)
		if err != nil {
			return result, err
		}
		for _, task := range tasks {
			members[task.AssigneeID] = true
			if task.ActiveRun != nil {
				members[task.ActiveRun.AgentID] = true
			}
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 ORDER BY id")
	if err != nil {
		return result, err
	}
	agents, err := scanJSON[domain.Agent](rows)
	if err != nil {
		return result, err
	}
	for _, agent := range agents {
		if agent.ID == actor.ID || agent.Banned || (scope == "project" && !members[agent.ID]) {
			continue
		}
		result.Recipients = append(result.Recipients, domain.AnnouncementRecipient{AgentID: agent.ID, Name: agent.Name})
		if len(result.Recipients) > domain.MaxAnnouncementRecipients {
			return result, domain.Fail(domain.Invalid, "announcement audience exceeds the bounded recipient limit")
		}
	}
	return result, nil
}

func (s *Store) AnnouncementAudience(ctx context.Context, actor, scope, projectID string) (result domain.AnnouncementAudience, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return result, err
	}
	return announcementAudience(ctx, snapshot, a, scope, projectID)
}

func (s *Store) Announce(ctx context.Context, actor string, req domain.AnnouncementRequest) (domain.AnnouncementReceipt, error) {
	return mutate(ctx, s, actor, "announcement.send", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AnnouncementReceipt, error) {
		var result domain.AnnouncementReceipt
		if err := domain.ValidateAnnouncement(req); err != nil {
			return result, err
		}
		audience, err := announcementAudience(ctx, tx, a, req.Scope, req.ProjectID)
		if err != nil {
			return result, err
		}
		ids := make([]string, len(audience.Recipients))
		for i, recipient := range audience.Recipients {
			ids[i] = recipient.AgentID
		}
		proposed := slices.Clone(req.RecipientIDs)
		slices.Sort(proposed)
		if audience.ProjectEpoch != req.ProjectEpoch || !slices.Equal(ids, proposed) {
			return result, domain.Fail(domain.Conflict, "announcement audience or project epoch changed; review the current recipients before a new proposal")
		}
		senderGeneration, active, err := agentOperationIdentity(ctx, tx, a)
		if err != nil || !active {
			if err != nil {
				return result, err
			}
			return result, domain.Fail(domain.Denied, "announcement sender identity is not current")
		}
		result = domain.AnnouncementReceipt{ID: domain.NewID("announcement"), Scope: req.Scope, ProjectID: req.ProjectID,
			Kind: req.Kind, CreatedAt: s.timestamp(), Deliveries: []domain.AnnouncementDelivery{}}
		for _, recipient := range audience.Recipients {
			target, err := activeIdentitySubject(ctx, tx, recipient.AgentID)
			if err != nil {
				return result, err
			}
			generation, active, err := agentOperationIdentity(ctx, tx, target)
			if err != nil {
				return result, err
			}
			if !active {
				return result, domain.Fail(domain.Denied, "announcement recipient identity is not current")
			}
			message := domain.Message{ID: domain.NewID("message"), SenderID: a.ID, RecipientID: recipient.AgentID,
				Body: req.Title + "\n\n" + req.Body, Origin: "human", Kind: "direct", ResponseRequest: req.Kind != "notice",
				State: "pending", CreatedAt: result.CreatedAt, Revision: 1,
				Announcement: &domain.AnnouncementContext{ID: result.ID, Scope: req.Scope, ProjectID: req.ProjectID, Kind: req.Kind,
					ProjectEpoch: req.ProjectEpoch, SenderGeneration: senderGeneration, RecipientGeneration: generation}}
			message.ThreadID = message.ID
			if _, err := insertMessage(ctx, tx, message); err != nil {
				return result, err
			}
			result.Deliveries = append(result.Deliveries, domain.AnnouncementDelivery{AgentID: recipient.AgentID, MessageID: message.ID})
		}
		return result, nil
	})
}

func (s *Store) AnnouncementMessageAuthority(ctx context.Context, actor, messageID string) (_ map[string]bool, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return nil, err
	}
	message, err := get[domain.Message](ctx, snapshot, "messages", messageID)
	if err != nil {
		return nil, err
	}
	if message.SenderID != actor && message.RecipientID != actor {
		return nil, domain.Fail(domain.Denied, "announcement source is private to its sender and recipient")
	}
	meta := message.Announcement
	if meta == nil {
		return nil, domain.Fail(domain.Invalid, "message is not an announcement source")
	}
	sender, err := activeIdentitySubject(ctx, snapshot, message.SenderID)
	if err != nil {
		return nil, err
	}
	generation, active, err := agentOperationIdentity(ctx, snapshot, sender)
	if err != nil {
		return nil, err
	}
	if !active || generation != meta.SenderGeneration {
		return nil, domain.Fail(domain.Denied, "announcement sender key generation changed")
	}
	target, err := activeIdentitySubject(ctx, snapshot, message.RecipientID)
	if err != nil {
		return nil, err
	}
	generation, active, err = agentOperationIdentity(ctx, snapshot, target)
	if err != nil {
		return nil, err
	}
	if !active || generation != meta.RecipientGeneration {
		return nil, domain.Fail(domain.Denied, "announcement recipient key generation changed")
	}
	epoch, err := announcementAuthority(ctx, snapshot, sender, meta.Scope, meta.ProjectID)
	if err != nil {
		return nil, err
	}
	if epoch != meta.ProjectEpoch {
		return nil, domain.Fail(domain.Conflict, "announcement project epoch changed")
	}
	if meta.Scope == "project" {
		project, err := get[domain.Project](ctx, snapshot, "projects", meta.ProjectID)
		if err != nil {
			return nil, err
		}
		if target.ID != project.OwnerID && !slices.Contains(project.Delegates, target.ID) {
			var assigned bool
			err := snapshot.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks
WHERE project_id=? AND archived=0 AND status NOT IN (?,?,?)
AND (assignee_id=? OR json_extract(data,'$.active_run.agent_id')=?))`,
				meta.ProjectID, domain.Done, domain.WontDo, domain.CancelledStatus, target.ID, target.ID).Scan(&assigned)
			if err != nil {
				return nil, err
			}
			if !assigned {
				return nil, domain.Fail(domain.Denied, "announcement recipient is no longer a current project participant")
			}
		}
	}
	return map[string]bool{"authorized": true}, nil
}
