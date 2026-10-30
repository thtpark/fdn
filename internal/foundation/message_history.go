package foundation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

type messageHistoryCursor struct {
	Version    int    `json:"v"`
	Actor      string `json:"a"`
	Generation int64  `json:"g"`
	Query      string `json:"q"`
	Ceiling    int64  `json:"c"`
	Time       int64  `json:"t"`
	ID         string `json:"i"`
}

func (s *Store) encodeHistoryCursor(cursor messageHistoryCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.updateCursorKey)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Store) decodeHistoryCursor(value string, expected messageHistoryCursor) (messageHistoryCursor, error) {
	if len(value) > 4096 {
		return expected, domain.Fail(domain.Invalid, "message cursor is too long")
	}
	payload, signature, found := strings.Cut(value, ".")
	data, dataErr := base64.RawURLEncoding.DecodeString(payload)
	proof, proofErr := base64.RawURLEncoding.DecodeString(signature)
	mac := hmac.New(sha256.New, s.updateCursorKey)
	mac.Write(data)
	var cursor messageHistoryCursor
	if !found || dataErr != nil || proofErr != nil || !hmac.Equal(proof, mac.Sum(nil)) ||
		json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Ceiling < 0 || cursor.ID == "" {
		return expected, domain.Fail(domain.Invalid, "invalid signed message cursor")
	}
	if cursor.Actor != expected.Actor || cursor.Generation != expected.Generation || cursor.Query != expected.Query {
		return expected, domain.Fail(domain.Conflict, "message cursor belongs to a different actor, identity generation or query")
	}
	return cursor, nil
}

// MessageHistory preserves a page's insertion ceiling while ordering by the
// immutable server creation time and ID. Later inserts cannot shift old pages.
func (s *Store) MessageHistory(ctx context.Context, actor, folder, state, peer, cursor string, limit int) (domain.Page[domain.Message], error) {
	return s.messageHistory(ctx, actor, folder, state, peer, "", cursor, limit)
}

func (s *Store) MessageThreadPage(ctx context.Context, actor, id, cursor string, limit int) (domain.Page[domain.Message], error) {
	message, err := s.Message(ctx, actor, id)
	if err != nil {
		return domain.Page[domain.Message]{Items: []domain.Message{}}, err
	}
	thread := message.ThreadID
	if thread == "" {
		thread = message.ID
	}
	return s.messageHistory(ctx, actor, "all", "all", "", thread, cursor, limit)
}

func (s *Store) messageHistory(ctx context.Context, actor, folder, state, peer, thread, since string, limit int) (domain.Page[domain.Message], error) {
	page := domain.Page[domain.Message]{Items: []domain.Message{}}
	if folder == "" {
		folder = "received"
	}
	if state == "" {
		state = "all"
	}
	if !slices.Contains([]string{"received", "sent", "all"}, folder) ||
		!slices.Contains([]string{"all", "pending", "handled", "deferred", "stale", "expired"}, state) ||
		(peer != "" && !domain.ValidOperation(peer)) {
		return page, domain.Fail(domain.Invalid, "invalid message folder, disposition or peer")
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return page, err
	}
	defer snapshot.Close()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return page, err
	}
	generation, active, err := agentOperationIdentity(ctx, snapshot, a)
	if err != nil {
		return page, err
	}
	if !active {
		return page, domain.Fail(domain.Denied, "message history requires a current active identity")
	}
	key, err := json.Marshal([]string{folder, state, peer, thread})
	if err != nil {
		return page, err
	}
	position := messageHistoryCursor{Version: 1, Actor: actor, Generation: generation, Query: domain.Hash(string(key))}
	if since != "" {
		position, err = s.decodeHistoryCursor(since, position)
		if err != nil {
			return page, err
		}
	} else if err := snapshot.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM messages").Scan(&position.Ceiling); err != nil {
		return page, err
	}
	query := `SELECT data FROM messages WHERE rowid<=?
AND COALESCE(json_extract(data,'$.kind'),'')<>'agent-operation'
AND json_extract(data,'$.agent_operation') IS NULL`
	args := []any{position.Ceiling}
	switch folder {
	case "received":
		query += " AND recipient_id=?"
		args = append(args, actor)
	case "sent":
		query += " AND sender_id=?"
		args = append(args, actor)
	case "all":
		query += " AND (sender_id=? OR recipient_id=?)"
		args = append(args, actor, actor)
	}
	if peer != "" {
		query += " AND (sender_id=? OR recipient_id=?)"
		args = append(args, peer, peer)
	}
	if thread != "" {
		query += " AND (id=? OR json_extract(data,'$.thread_id')=?)"
		args = append(args, thread, thread)
	}
	if position.ID != "" {
		query += " AND (json_extract(data,'$.created_at')<? OR (json_extract(data,'$.created_at')=? AND id<?))"
		args = append(args, position.Time, position.Time, position.ID)
	}
	if state != "all" {
		query += ` AND (CASE WHEN state IN ('pending','deferred')
AND (json_extract(data,'$.kind') IN ('authentication','authentication-refresh','permission')
OR (json_extract(data,'$.kind')='session' AND json_extract(data,'$.origin')='service' AND substr(id,1,13)='message_help_'))
AND COALESCE(NULLIF(json_extract(data,'$.expires_at'),0),json_extract(data,'$.created_at')+?)<=?
THEN 'expired' ELSE state END)=?`
		args = append(args, domain.AuthenticationRequestTTL, s.timestamp(), state)
	}
	args = append(args, limit+1)
	rows, err := snapshot.QueryContext(ctx, query+" ORDER BY json_extract(data,'$.created_at') DESC,id DESC LIMIT ?", args...)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.Message](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		position.Time, position.ID = last.CreatedAt, last.ID
		page.NextCursor, err = s.encodeHistoryCursor(position)
		if err != nil {
			return page, err
		}
	}
	now := s.timestamp()
	for i := range page.Items {
		page.Items[i] = messageAt(page.Items[i], now)
	}
	return page, nil
}
