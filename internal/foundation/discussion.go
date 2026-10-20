package foundation

import (
	"context"
	"database/sql"
	"regexp"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

var mentionPattern = regexp.MustCompile(`@\{(agent_[0-9a-f]{32})\}`)

func ExtractMentions(body, author string) []string {
	out := []string{}
	for _, match := range mentionPattern.FindAllStringSubmatch(body, -1) {
		if match[1] != author && !slices.Contains(out, match[1]) {
			out = append(out, match[1])
		}
	}
	slices.Sort(out)
	return out
}

func (s *Store) saveEntry(ctx context.Context, tx *sql.Tx, entry domain.Entry, actor, action string) error {
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
			ParentID: req.ParentID, AuthorID: a.ID, Mentions: ExtractMentions(req.Body, a.ID), Generation: 1, Revision: 1,
			CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		return e, s.saveEntry(ctx, tx, e, a.ID, "create")
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
		e.Body, e.Mentions, e.UpdatedAt = req.Body, ExtractMentions(req.Body, a.ID), s.timestamp()
		e.Generation++
		e.Revision++
		return e, s.saveEntry(ctx, tx, e, a.ID, "edit")
	})
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
	rows, err := s.db.QueryContext(ctx, "SELECT entry_id, task_id, generation, state FROM attention WHERE agent_id = ? AND state = 'pending' AND entry_id > ? ORDER BY entry_id LIMIT ?", actor, last, limit+1)
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
		if e.Kind != "comment" || !slices.Contains(e.Mentions, a.ID) || e.Generation != req.Generation {
			return domain.Attention{}, domain.Fail(domain.Conflict, "mention was removed or its body generation changed")
		}
		if req.State != "handled" && req.State != "deferred" {
			return domain.Attention{}, domain.Fail(domain.Invalid, "receipt state must be handled or deferred")
		}
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM attention WHERE agent_id = ? AND entry_id = ? AND generation = ?", a.ID, entryID, req.Generation).Scan(&current); err != nil {
			if err == sql.ErrNoRows {
				return domain.Attention{}, domain.Fail(domain.NotFound, "attention not found")
			}
			return domain.Attention{}, err
		}
		if current != "pending" && current != req.State {
			return domain.Attention{}, domain.Fail(domain.Conflict, "attention is already disposed")
		}
		_, err = tx.ExecContext(ctx, "UPDATE attention SET state = ? WHERE agent_id = ? AND entry_id = ? AND generation = ?", req.State, a.ID, entryID, req.Generation)
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
		if _, err := s.actor(ctx, tx, id); err != nil {
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
		if _, err := s.actor(ctx, tx, e.AuthorID); err != nil {
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
