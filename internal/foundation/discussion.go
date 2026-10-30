package foundation

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

var (
	mentionPattern   = regexp.MustCompile(`@\{([^{}\r\n]{1,128})\}`)
	mentionIDPattern = regexp.MustCompile(`^agent_[0-9a-f]{32}$`)
)

func resolveMentions(ctx context.Context, tx *sql.Tx, body, author string) ([]string, error) {
	out := []string{}
	var names map[string][]string
	for _, match := range mentionPattern.FindAllStringSubmatch(body, -1) {
		id := match[1]
		if !mentionIDPattern.MatchString(id) {
			if names == nil {
				rows, err := tx.QueryContext(ctx, "SELECT data FROM agents WHERE active = 1")
				if err != nil {
					return nil, err
				}
				agents, err := scanJSON[domain.Agent](rows)
				if err != nil {
					return nil, err
				}
				names = make(map[string][]string, len(agents))
				for _, agent := range agents {
					key := strings.ToLower(agent.Name)
					names[key] = append(names[key], agent.ID)
				}
			}
			matches := names[strings.ToLower(id)]
			if len(matches) == 0 {
				return nil, domain.Fail(domain.NotFound, "mentioned agent name is not active or does not exist; use an active agent name or agent ID")
			}
			if len(matches) != 1 {
				return nil, domain.Fail(domain.Conflict, "mentioned agent name is ambiguous; use the agent ID")
			}
			id = matches[0]
		}
		if id != author && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (s *Store) saveEntry(ctx context.Context, tx *sql.Tx, entry *domain.Entry, actor, action string) error {
	mentions, err := resolveMentions(ctx, tx, entry.Body, entry.AuthorID)
	if err != nil {
		return err
	}
	entry.Mentions = mentions
	data, err := encode(entry)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO entries(id, data, task_id, author_id, kind) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, entry.ID, data, entry.TaskID, entry.AuthorID, entry.Kind)
	if err != nil {
		return err
	}
	if entry.Kind == "comment" {
		if _, err := tx.ExecContext(ctx, "UPDATE attention SET state = 'stale' WHERE entry_id = ? AND generation <> ?", entry.ID, entry.Generation); err != nil {
			return err
		}
		for _, recipient := range entry.Mentions {
			var active int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id = ? AND active = 1", recipient).Scan(&active); err != nil {
				return err
			}
			if active != 0 {
				if _, err := tx.ExecContext(ctx, "INSERT INTO attention(agent_id, entry_id, generation, task_id, state) VALUES(?, ?, ?, ?, 'pending') ON CONFLICT DO NOTHING", recipient, entry.ID, entry.Generation, entry.TaskID); err != nil {
					return err
				}
			}
		}
	}
	if err := s.history(ctx, tx, entry.ID, actor, action, entry); err != nil {
		return err
	}
	task, err := get[domain.Task](ctx, tx, "tasks", entry.TaskID)
	if err != nil {
		return err
	}
	if entry.Kind == "comment" {
		task.DiscussionGeneration++
		task.Clarity = nil
		if err := s.saveTask(ctx, tx, &task, actor, "discussion"); err != nil {
			return err
		}
	}
	return s.event(ctx, tx, entry.Kind, entry.ID, task.ID, task.ProjectID)
}

func (s *Store) CreateEntry(ctx context.Context, actor, taskID string, req domain.EntryRequest) (domain.Entry, error) {
	return mutate(ctx, s, actor, "entry.create:"+taskID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Entry, error) {
		var e domain.Entry
		if req.Kind != "comment" && req.Kind != "worklog" {
			return e, domain.Fail(domain.Invalid, "entry kind must be comment or worklog")
		}
		if err := domain.CheckText(req.Body, "entry body", 65536, true); err != nil {
			return e, err
		}
		if _, err := get[domain.Task](ctx, tx, "tasks", taskID); err != nil {
			return e, err
		}
		if req.ParentID != "" {
			parent, err := get[domain.Entry](ctx, tx, "entries", req.ParentID)
			if err != nil {
				return e, err
			}
			if parent.TaskID != taskID || parent.Kind != "comment" || req.Kind != "comment" {
				return e, domain.Fail(domain.Invalid, "replies must reference a comment in this task")
			}
		}
		e = domain.Entry{ID: domain.NewID(req.Kind), TaskID: taskID, Kind: req.Kind, Body: req.Body,
			ParentID: req.ParentID, AuthorID: a.ID, Generation: 1, Revision: 1,
			CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		if err := s.saveEntry(ctx, tx, &e, a.ID, "create"); err != nil {
			return e, err
		}
		return e, nil
	})
}

func (s *Store) EditEntry(ctx context.Context, actor, id string, req domain.EntryRequest) (domain.Entry, error) {
	return mutate(ctx, s, actor, "entry.edit:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Entry, error) {
		e, err := get[domain.Entry](ctx, tx, "entries", id)
		if err != nil {
			return e, err
		}
		if e.AuthorID != a.ID {
			return e, domain.Fail(domain.Denied, "only the creator may edit a comment or worklog, including when requester is Founder")
		}
		if err := expect(e.Revision, req.ExpectedRevision); err != nil {
			return e, err
		}
		if (req.Kind != "" && req.Kind != e.Kind) || (req.ParentID != "" && req.ParentID != e.ParentID) {
			return e, domain.Fail(domain.Invalid, "kind and reply parent are immutable")
		}
		if err := domain.CheckText(req.Body, "entry body", 65536, true); err != nil {
			return e, err
		}
		e.Body, e.UpdatedAt = req.Body, s.timestamp()
		e.Generation++
		e.Revision++
		if err := s.saveEntry(ctx, tx, &e, a.ID, "edit"); err != nil {
			return e, err
		}
		return e, nil
	})
}

func (s *Store) Entry(ctx context.Context, actor, id string) (domain.Entry, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Entry{}, err
	}
	return get[domain.Entry](ctx, s.db, "entries", id)
}

func (s *Store) Entries(ctx context.Context, actor, taskID, after string, limit int) (domain.Page[domain.Entry], error) {
	page := domain.Page[domain.Entry]{Items: []domain.Entry{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	if _, err := get[domain.Task](ctx, s.db, "tasks", taskID); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	last, err := readCursor(after, actor, "entries:"+taskID)
	if err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM entries WHERE task_id = ? AND id > ? ORDER BY id LIMIT ?", taskID, last, limit+1)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.Entry](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, "entries:"+taskID, page.Items[limit-1].ID)
	}
	return page, nil
}

func (s *Store) Attention(ctx context.Context, actor, after string, limit int) (domain.Page[domain.Attention], error) {
	page := domain.Page[domain.Attention]{Items: []domain.Attention{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	last, err := readCursor(after, actor, "attention")
	if err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.task_id,json_extract(e.data,'$.generation'),coalesce(a.state,'pending')
FROM entries e JOIN tasks t ON t.id=e.task_id
LEFT JOIN attention a ON a.agent_id=? AND a.entry_id=e.id AND a.generation=json_extract(e.data,'$.generation')
WHERE e.kind='comment' AND e.author_id<>? AND e.id>? AND coalesce(a.state,'pending')='pending'
AND (t.assignee_id=? OR EXISTS(SELECT 1 FROM json_each(e.data,'$.mentions') WHERE value=?))
ORDER BY e.id LIMIT ?`, actor, actor, last, actor, actor, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var a domain.Attention
		if err := rows.Scan(&a.EntryID, &a.TaskID, &a.Generation, &a.State); err != nil {
			return page, err
		}
		page.Items = append(page.Items, a)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, "attention", page.Items[limit-1].EntryID)
	}
	return page, rows.Err()
}

func (s *Store) AttentionReceipt(ctx context.Context, actor, entryID string, req domain.AttentionRequest) (domain.Attention, error) {
	return mutate(ctx, s, actor, "attention.receipt:"+entryID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Attention, error) {
		e, err := get[domain.Entry](ctx, tx, "entries", entryID)
		if err != nil {
			return domain.Attention{}, err
		}
		task, err := get[domain.Task](ctx, tx, "tasks", e.TaskID)
		if err != nil {
			return domain.Attention{}, err
		}
		if !domain.RelevantComment(e, task, a.ID) || e.Generation != req.Generation {
			return domain.Attention{}, domain.Fail(domain.Conflict, "comment assignment, mention or body generation changed")
		}
		if req.State != "handled" && req.State != "deferred" {
			return domain.Attention{}, domain.Fail(domain.Invalid, "receipt state must be handled or deferred")
		}
		var current string
		err = tx.QueryRowContext(ctx, "SELECT state FROM attention WHERE agent_id = ? AND entry_id = ? AND generation = ?", a.ID, entryID, req.Generation).Scan(&current)
		if err == sql.ErrNoRows {
			current = "pending"
		} else if err != nil {
			return domain.Attention{}, err
		}
		if current != "pending" && current != req.State {
			return domain.Attention{}, domain.Fail(domain.Conflict, "attention is already disposed")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO attention(agent_id,entry_id,generation,task_id,state) VALUES(?,?,?,?,?)
ON CONFLICT(agent_id,entry_id,generation) DO UPDATE SET state=excluded.state`, a.ID, entryID, req.Generation, e.TaskID, req.State)
		return domain.Attention{EntryID: e.ID, TaskID: e.TaskID, Generation: e.Generation, State: req.State}, err
	})
}

func (s *Store) validateWaiting(ctx context.Context, tx *sql.Tx, task domain.Task, actor string, waiting *domain.Waiting) error {
	if waiting == nil || waiting.Kind != "clarification" || len(waiting.RequestedIDs) == 0 || len(waiting.RequestedIDs) > 32 || len(waiting.QuestionIDs) == 0 || len(waiting.QuestionIDs) > 32 {
		return domain.Fail(domain.Invalid, "clarification requires bounded responsible actors and published question references")
	}
	if err := domain.CheckText(waiting.Reason, "waiting reason", 8192, true); err != nil {
		return err
	}
	mentioned := []string{}
	for _, id := range waiting.QuestionIDs {
		e, err := get[domain.Entry](ctx, tx, "entries", id)
		if err != nil {
			return err
		}
		if e.TaskID != task.ID || e.Kind != "comment" || e.AuthorID != actor {
			return domain.Fail(domain.Invalid, "blocking questions must be this worker's comments on this task")
		}
		mentioned = append(mentioned, e.Mentions...)
	}
	for _, id := range waiting.RequestedIDs {
		if _, err := activeIdentitySubject(ctx, tx, id); err != nil {
			return err
		}
		if !slices.Contains(mentioned, id) {
			return domain.Fail(domain.Invalid, "every requested respondent must be literally mentioned in a blocking question")
		}
	}
	return nil
}

func (s *Store) validateAnswers(ctx context.Context, tx *sql.Tx, task domain.Task, ids []string) error {
	if len(ids) == 0 || len(ids) > 64 {
		return domain.Fail(domain.Prerequisite, "review must identify current answers to every blocking question")
	}
	answered := map[string]bool{}
	for _, id := range ids {
		e, err := get[domain.Entry](ctx, tx, "entries", id)
		if err != nil {
			return err
		}
		if e.TaskID != task.ID || e.Kind != "comment" || !slices.Contains(task.Waiting.RequestedIDs, e.AuthorID) || !slices.Contains(task.Waiting.QuestionIDs, e.ParentID) {
			return domain.Fail(domain.Prerequisite, "answer must be a current reply from a requested respondent to a blocking question")
		}
		if _, err := activeIdentitySubject(ctx, tx, e.AuthorID); err != nil {
			return err
		}
		answered[e.ParentID] = true
	}
	for _, id := range task.Waiting.QuestionIDs {
		if !answered[id] {
			return domain.Fail(domain.Prerequisite, "a blocking question still lacks a reviewed answer")
		}
	}
	return nil
}
