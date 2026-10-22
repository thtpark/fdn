package foundation

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) SendMessage(ctx context.Context, actor string, req domain.MessageRequest) (domain.Message, error) {
	return mutate(ctx, s, actor, "message.send", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Message, error) {
		if err := domain.CheckText(req.Body, "message body", 32768, true); err != nil {
			return domain.Message{}, err
		}
		if _, err := activeIdentitySubject(ctx, tx, req.RecipientID); err != nil {
			return domain.Message{}, err
		}
		if !slices.Contains([]string{"human", "ai", "service"}, req.Origin) || !slices.Contains([]string{"direct", "authentication", "permission", "session"}, req.Kind) {
			return domain.Message{}, domain.Fail(domain.Invalid, "message requires a supported declared origin and kind")
		}
		if req.Kind == "permission" && req.Origin != "service" {
			return domain.Message{}, domain.Fail(domain.Invalid, "permission handoffs require service origin")
		}
		if req.Kind != "direct" && req.ResponseRequest {
			return domain.Message{}, domain.Fail(domain.Invalid, "protected handoffs cannot request automatic AI responses")
		}
		now := s.timestamp()
		expiresAt := req.ExpiresAt
		if req.Kind == "authentication" || req.Kind == "permission" {
			if expiresAt == 0 {
				expiresAt = now + domain.AuthenticationRequestTTL
			}
			if expiresAt <= now || expiresAt > now+domain.AuthenticationRequestTTL {
				return domain.Message{}, domain.Fail(domain.Invalid, "authentication and permission message expiry must be in the next 900 seconds")
			}
		} else if expiresAt != 0 {
			return domain.Message{}, domain.Fail(domain.Invalid, "only authentication and permission messages may set an expiry")
		}
		m := domain.Message{ID: domain.NewID("message"), SenderID: a.ID, RecipientID: req.RecipientID, Body: req.Body,
			Origin: req.Origin, Kind: req.Kind, Urgent: req.Urgent, ResponseRequest: req.ResponseRequest, State: "pending", CreatedAt: now, ExpiresAt: expiresAt, Revision: 1}
		return insertMessage(ctx, tx, m)
	})
}

func insertMessage(ctx context.Context, tx *sql.Tx, m domain.Message) (domain.Message, error) {
	data, err := encode(m)
	if err != nil {
		return m, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO messages(id, data, sender_id, recipient_id, state, urgent) VALUES(?, ?, ?, ?, ?, ?)", m.ID, data, m.SenderID, m.RecipientID, m.State, m.Urgent)
	return m, err
}

// Only the help transaction assigns this namespace; clients cannot choose IDs.
func assistanceHelpMessage(m domain.Message) bool {
	return m.Kind == "session" && m.Origin == "service" && strings.HasPrefix(m.ID, "message_help_")
}

func messageAt(m domain.Message, now int64) domain.Message {
	if m.Kind == "authentication" || m.Kind == "authentication-refresh" || m.Kind == "permission" || assistanceHelpMessage(m) {
		if m.ExpiresAt == 0 {
			m.ExpiresAt = m.CreatedAt + domain.AuthenticationRequestTTL
		}
		if m.ExpiresAt <= now && (m.State == "pending" || m.State == "deferred") {
			m.State = "expired"
		}
	}
	return m
}

func (s *Store) Message(ctx context.Context, actor, id string) (domain.Message, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Message{}, err
	}
	m, err := get[domain.Message](ctx, s.db, "messages", id)
	if err != nil {
		return m, err
	}
	if m.SenderID != actor && m.RecipientID != actor {
		return domain.Message{}, domain.Fail(domain.Denied, "message is private to its sender and recipient")
	}
	return messageAt(m, s.timestamp()), nil
}

func (s *Store) Inbox(ctx context.Context, actor, cursor string, limit int, urgent bool) (domain.Page[domain.Message], error) {
	page := domain.Page[domain.Message]{Items: []domain.Message{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "inbox"
	if urgent {
		key += ":urgent"
	}
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	now := s.timestamp()
	query := `SELECT data FROM messages WHERE recipient_id = ? AND state = 'pending' AND id > ?
AND ((COALESCE(json_extract(data, '$.kind'), '') NOT IN ('authentication', 'authentication-refresh', 'permission')
AND NOT (COALESCE(json_extract(data, '$.kind'), '') = 'session' AND COALESCE(json_extract(data, '$.origin'), '') = 'service' AND substr(id, 1, 13) = 'message_help_'))
OR COALESCE(NULLIF(json_extract(data, '$.expires_at'), 0), json_extract(data, '$.created_at') + ?) > ?)`
	if urgent {
		query += " AND urgent = 1"
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id LIMIT ?", actor, last, domain.AuthenticationRequestTTL, now, limit+1)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.Message](rows)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		page.Items[i] = messageAt(page.Items[i], now)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].ID)
	}
	return page, nil
}

func (s *Store) ReceiveMessage(ctx context.Context, actor, id string, req domain.MessageReceipt) (domain.Message, error) {
	return mutate(ctx, s, actor, "message.receipt:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Message, error) {
		m, err := get[domain.Message](ctx, tx, "messages", id)
		if err != nil {
			return m, err
		}
		if m.RecipientID != a.ID {
			return m, domain.Fail(domain.Denied, "only the recipient changes disposition")
		}
		if err := expect(m.Revision, req.ExpectedRevision); err != nil {
			return m, err
		}
		if !slices.Contains([]string{"handled", "deferred", "stale", "pending"}, req.State) {
			return m, domain.Fail(domain.Invalid, "invalid receipt state")
		}
		if m.State == "handled" || m.State == "stale" {
			return m, domain.Fail(domain.Conflict, "final receipts cannot reopen")
		}
		if messageAt(m, s.timestamp()).State == "expired" && req.State != "stale" {
			return m, domain.Fail(domain.Conflict, "expired protected messages cannot be handled or reopened; a fresh request is required")
		}
		if req.State == "pending" && m.State != "deferred" {
			return m, domain.Fail(domain.Invalid, "only deferred messages can return to pending")
		}
		m.State = req.State
		m.Revision++
		data, err := encode(m)
		if err != nil {
			return m, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE messages SET data = ?, state = ? WHERE id = ?", data, m.State, m.ID)
		return m, err
	})
}
