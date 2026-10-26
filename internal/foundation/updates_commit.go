package foundation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

type updateChange struct {
	table, id     string
	before, after []byte
}

type updateParties map[string]struct{}

func (p updateParties) add(ids ...string) {
	for _, id := range ids {
		if id != "" {
			p[id] = struct{}{}
		}
	}
}

type routedUpdate struct {
	update  domain.Update
	parties updateParties
}

type updateRouteKey struct {
	topic, source, task, project string
}

type updateRouter struct {
	ctx            context.Context
	tx             *sql.Tx
	founders       []string
	foundersLoaded bool
	projects       map[string]domain.Project
	tasks          map[string]domain.Task
	updates        []routedUpdate
	updateKeys     map[updateRouteKey]int
}

// Every server write transaction commits here, including maintenance, health,
// identity/account/owner changes and session retirement outside mutate/transact.
// Triggers retain old parties even when a mutation withdraws their authority.
func (s *Store) commit(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT source_table,source_id,before_data,after_data FROM update_changes ORDER BY ordinal")
	if err != nil {
		return err
	}
	var changes []updateChange
	for rows.Next() {
		var change updateChange
		if err := rows.Scan(&change.table, &change.id, &change.before, &change.after); err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	affected := make(updateParties)
	publicScopes := make(map[eventScope]struct{})
	if len(changes) != 0 {
		router := updateRouter{ctx: ctx, tx: tx, projects: make(map[string]domain.Project), tasks: make(map[string]domain.Task), updateKeys: make(map[updateRouteKey]int)}
		for _, change := range changes {
			if change.table == "events" {
				var scope eventScope
				if err := json.Unmarshal(change.after, &scope); err != nil {
					return err
				}
				publicScopes[scope] = struct{}{}
				continue
			}
			if !bytes.Equal(change.before, change.after) {
				if err := router.route(change); err != nil {
					return err
				}
			}
		}
		generations := make(map[string]int64)
		for _, routed := range router.updates {
			for actor := range routed.parties {
				if _, ok := generations[actor]; ok {
					continue
				}
				var generation int64
				err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(i.data,'$.generation'),0)
FROM agents a LEFT JOIN agent_identities i ON i.id=a.id WHERE a.id=?`, actor).Scan(&generation)
				if err == sql.ErrNoRows {
					delete(routed.parties, actor)
					continue
				}
				if err != nil {
					return err
				}
				generations[actor] = generation
			}
			if len(routed.parties) == 0 {
				continue
			}
			u := routed.update
			result, err := tx.ExecContext(ctx, "INSERT INTO update_feed(topic,source_id,task_id,project_id,urgent,created_at) VALUES(?,?,?,?,?,?)",
				u.Topic, u.SourceID, u.TaskID, u.ProjectID, u.Urgent, s.timestamp())
			if err != nil {
				return err
			}
			sequence, err := result.LastInsertId()
			if err != nil {
				return err
			}
			for actor := range routed.parties {
				if _, err := tx.ExecContext(ctx, "INSERT INTO update_recipients(actor_id,key_generation,sequence) VALUES(?,?,?)", actor, generations[actor], sequence); err != nil {
					return err
				}
				affected.add(actor)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM update_changes"); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyUpdates(affected)
	s.notifyEvents(publicScopes)
	return nil
}

func changeValues[T any](change updateChange) ([]T, error) {
	var values []T
	for _, data := range [][]byte{change.before, change.after} {
		if len(data) == 0 {
			continue
		}
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (r *updateRouter) queue(u domain.Update, parties updateParties) {
	key := updateRouteKey{u.Topic, u.SourceID, u.TaskID, u.ProjectID}
	if index, ok := r.updateKeys[key]; ok {
		existing := &r.updates[index]
		existing.update.Urgent = existing.update.Urgent || u.Urgent
		for id := range parties {
			existing.parties.add(id)
		}
		return
	}
	r.updateKeys[key] = len(r.updates)
	r.updates = append(r.updates, routedUpdate{u, parties})
}

// Private messages/session controls already name their exact parties. Resolve
// Founder authority only when a public/authority routing path actually needs it,
// once in this transaction rather than scanning the fleet on every private write.
func (r *updateRouter) includeFounders(parties updateParties) error {
	if !r.foundersLoaded {
		rows, err := r.tx.QueryContext(r.ctx, "SELECT id FROM agents WHERE active=1 AND json_extract(data,'$.founder')=1 AND COALESCE(json_extract(data,'$.banned'),0)=0")
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			r.founders = append(r.founders, id)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		r.foundersLoaded = true
	}
	parties.add(r.founders...)
	return nil
}

func (r *updateRouter) project(id string, parties updateParties) error {
	if err := r.includeFounders(parties); err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	p, ok := r.projects[id]
	if !ok {
		var err error
		p, err = get[domain.Project](r.ctx, r.tx, "projects", id)
		if err != nil {
			return err
		}
		r.projects[id] = p
	}
	projectParties(p, parties)
	return nil
}

func projectParties(p domain.Project, parties updateParties) {
	parties.add(p.OwnerID)
	parties.add(p.Delegates...)
}

func (r *updateRouter) taskParties(t domain.Task, parties updateParties) error {
	parties.add(t.CreatedBy, t.AssigneeID)
	if t.ActiveRun != nil {
		parties.add(t.ActiveRun.AgentID)
	}
	for _, approval := range []*domain.Approval{t.Readiness, t.RetryApproval, t.Verification} {
		if approval != nil {
			parties.add(approval.ActorID)
		}
	}
	if t.Clarity != nil {
		parties.add(t.Clarity.ActorID)
	}
	parties.add(t.Policy.RequiredActors...)
	if t.Waiting != nil {
		parties.add(t.Waiting.RequestedIDs...)
	}
	return r.project(t.ProjectID, parties)
}

func (r *updateRouter) task(id string, parties updateParties) (domain.Task, error) {
	t, ok := r.tasks[id]
	if !ok {
		var err error
		t, err = get[domain.Task](r.ctx, r.tx, "tasks", id)
		if err != nil {
			return t, err
		}
		r.tasks[id] = t
	}
	return t, r.taskParties(t, parties)
}

func (r *updateRouter) taskRows(query string, parties updateParties, args ...any) error {
	rows, err := r.tx.QueryContext(r.ctx, query, args...)
	if err != nil {
		return err
	}
	tasks, err := scanJSON[domain.Task](rows)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := r.taskParties(task, parties); err != nil {
			return err
		}
	}
	return nil
}

func (r *updateRouter) partyRows(query string, parties updateParties, args ...any) error {
	rows, err := r.tx.QueryContext(r.ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var first, second string
		if err := rows.Scan(&first, &second); err != nil {
			return err
		}
		parties.add(first, second)
	}
	return rows.Err()
}

func (r *updateRouter) projectDependents(id string, parties updateParties) error {
	if err := r.taskRows("SELECT data FROM tasks WHERE project_id=?", parties, id); err != nil {
		return err
	}
	return r.partyRows(`SELECT requester_id,target_id FROM sessions WHERE json_extract(data,'$.project_id')=?
UNION SELECT requester_id,target_id FROM assistance_grants WHERE json_extract(data,'$.project_id')=?`, parties, id, id)
}

func (r *updateRouter) agentParties(id string, parties updateParties) error {
	parties.add(id)
	if err := r.includeFounders(parties); err != nil {
		return err
	}
	if err := r.taskRows(`SELECT data FROM tasks WHERE assignee_id=? OR json_extract(data,'$.active_run.agent_id')=?
OR json_extract(data,'$.readiness.actor_id')=? OR json_extract(data,'$.retry_approval.actor_id')=?
OR json_extract(data,'$.verification.actor_id')=?`, parties, id, id, id, id, id); err != nil {
		return err
	}
	rows, err := r.tx.QueryContext(r.ctx, `SELECT data FROM projects WHERE json_extract(data,'$.owner_id')=?
OR EXISTS(SELECT 1 FROM json_each(projects.data,'$.delegates') WHERE value=?)`, id, id)
	if err != nil {
		return err
	}
	projects, err := scanJSON[domain.Project](rows)
	if err != nil {
		return err
	}
	for _, project := range projects {
		projectParties(project, parties)
	}
	return r.partyRows(`SELECT requester_id,target_id FROM sessions WHERE requester_id=? OR target_id=?
UNION SELECT requester_id,target_id FROM assistance_grants WHERE requester_id=? OR target_id=?
UNION SELECT sender_id,recipient_id FROM messages WHERE (sender_id=? OR recipient_id=?)
AND json_extract(data,'$.kind')='agent-operation' AND state='pending'`, parties, id, id, id, id, id, id)
}

func (r *updateRouter) route(change updateChange) error {
	parties := make(updateParties)
	u := domain.Update{SourceID: change.id}
	switch change.table {
	case "tasks":
		values, err := changeValues[domain.Task](change)
		if err != nil {
			return err
		}
		u.Topic, u.TaskID = "task", change.id
		for _, task := range values {
			u.ProjectID = task.ProjectID
			u.Urgent = u.Urgent || task.ActiveRun != nil
			if err := r.taskParties(task, parties); err != nil {
				return err
			}
		}
	case "projects":
		values, err := changeValues[domain.Project](change)
		if err != nil {
			return err
		}
		u.Topic, u.ProjectID, u.Urgent = "project", change.id, len(change.before) != 0
		if err := r.includeFounders(parties); err != nil {
			return err
		}
		for _, project := range values {
			projectParties(project, parties)
		}
		if err := r.projectDependents(change.id, parties); err != nil {
			return err
		}
	case "runs":
		values, err := changeValues[domain.Run](change)
		if err != nil {
			return err
		}
		u.Topic = "run"
		for _, run := range values {
			parties.add(run.AgentID)
			task, err := r.task(run.TaskID, parties)
			if err != nil {
				return err
			}
			u.TaskID, u.ProjectID = task.ID, task.ProjectID
		}
	case "entries":
		values, err := changeValues[domain.Entry](change)
		if err != nil {
			return err
		}
		for _, entry := range values {
			parties.add(entry.AuthorID)
			parties.add(entry.Mentions...)
			task, err := r.task(entry.TaskID, parties)
			if err != nil {
				return err
			}
			u.Topic, u.TaskID, u.ProjectID = entry.Kind, task.ID, task.ProjectID
			u.Urgent = entry.Kind == "comment" && task.ActiveRun != nil
		}
	case "attention":
		values, err := changeValues[struct {
			AgentID string `json:"agent_id"`
			EntryID string `json:"entry_id"`
			TaskID  string `json:"task_id"`
		}](change)
		if err != nil {
			return err
		}
		u.Topic = "attention"
		for _, attention := range values {
			parties.add(attention.AgentID)
			u.SourceID, u.TaskID = attention.EntryID, attention.TaskID
			task, err := get[domain.Task](r.ctx, r.tx, "tasks", attention.TaskID)
			if err != nil {
				return err
			}
			u.ProjectID = task.ProjectID
		}
	case "messages":
		values, err := changeValues[domain.Message](change)
		if err != nil {
			return err
		}
		u.Topic = "message"
		for _, message := range values {
			parties.add(message.SenderID, message.RecipientID)
			u.Urgent = u.Urgent || message.Urgent
			if message.Kind == "agent-operation" || message.AgentOperation != nil {
				u.Topic, u.Urgent = "agent-operation", true
			}
		}
	case "sessions":
		values, err := changeValues[domain.Session](change)
		if err != nil {
			return err
		}
		u.Topic, u.Urgent = "session", true
		for _, session := range values {
			parties.add(session.RequesterID, session.TargetID)
			u.TaskID, u.ProjectID = session.TaskID, session.ProjectID
		}
	case "assistance_grants":
		values, err := changeValues[domain.AssistanceGrant](change)
		if err != nil {
			return err
		}
		u.Topic, u.Urgent = "assistance", true
		for _, grant := range values {
			parties.add(grant.RequesterID, grant.TargetID, grant.CreatedBy)
			u.ProjectID = grant.ProjectID
		}
	case "agents":
		values, err := changeValues[domain.Agent](change)
		if err != nil {
			return err
		}
		if len(values) == 2 {
			before, after := values[0], values[1]
			before.LastSeen, after.LastSeen, before.Status, after.Status = 0, 0, nil, nil
			beforeJSON, _ := encode(before)
			afterJSON, _ := encode(after)
			if bytes.Equal(beforeJSON, afterJSON) {
				return nil
			}
		}
		u.Topic, u.Urgent = "agent", len(change.before) != 0
		if len(values) == 2 && values[0].Mode != values[1].Mode {
			u.Topic = "agent-mode"
		}
		if err := r.agentParties(change.id, parties); err != nil {
			return err
		}
	case "agent_identities":
		u.Topic, u.Urgent = "agent", true
		if err := r.agentParties(change.id, parties); err != nil {
			return err
		}
	case "project_creators":
		values, err := changeValues[struct {
			AgentID string `json:"agent_id"`
		}](change)
		if err != nil {
			return err
		}
		u.Topic = "project-creation"
		if err := r.includeFounders(parties); err != nil {
			return err
		}
		for _, value := range values {
			parties.add(value.AgentID)
		}
	default:
		return domain.Fail(domain.Internal, "unrecognized update change source")
	}
	r.queue(u, parties)
	return nil
}
