package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thtpark/fdn/internal/domain"
)

const replyLoopWarningMessages = 200

type replyLoop struct {
	Version        int    `json:"version"`
	LastID         string `json:"last_id"`
	SenderID       string `json:"sender_id"`
	RecipientID    string `json:"recipient_id"`
	Messages       int64  `json:"messages"`
	Warned         bool   `json:"warned"`
	WarningPending bool   `json:"warning_pending"`
}

func (s *Store) recordReplyLoop(ctx context.Context, tx *sql.Tx, message domain.Message) error {
	if message.Kind != "direct" || message.Origin == "service" || message.SenderID == message.RecipientID {
		return nil
	}
	key := "reply-loop:" + message.ThreadID
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	state := replyLoop{Version: 1}
	if err == nil {
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if state.Version != 1 {
			return domain.Fail(domain.Prerequisite, "unsupported retained reply-loop state")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if message.Origin == "human" {
		state.Messages = 0
	} else if message.ReplyToID == state.LastID && message.SenderID == state.RecipientID && message.RecipientID == state.SenderID {
		state.Messages++
	} else {
		state.Messages = 1
		parent := message.ReplyToID
		sender, recipient := message.SenderID, message.RecipientID
		for parent != "" && state.Messages < replyLoopWarningMessages {
			prior, err := get[domain.Message](ctx, tx, "messages", parent)
			if err != nil {
				return err
			}
			if prior.Kind != "direct" || prior.Origin != "ai" || prior.SenderID != recipient || prior.RecipientID != sender {
				break
			}
			state.Messages++
			parent, sender, recipient = prior.ReplyToID, prior.SenderID, prior.RecipientID
		}
	}
	state.LastID, state.SenderID, state.RecipientID = message.ID, message.SenderID, message.RecipientID
	if (state.Messages >= replyLoopWarningMessages || state.WarningPending) && !state.Warned {
		state.WarningPending = true
		founder, err := activeFounder(ctx, tx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) && domain.AsFault(err).Code != domain.Denied {
			return err
		}
		if err == nil {
			id := "message_loop_" + domain.Hash(message.ThreadID)[:32]
			warning := domain.Message{
				ID: id, ThreadID: id, SenderID: message.SenderID, RecipientID: founder.ID,
				Kind: "direct", Origin: "service", State: "pending", Revision: 1, CreatedAt: s.timestamp(),
				Body: fmt.Sprintf("Reply-loop notice: %s and %s have exchanged at least 100 AI round trips in conversation %s. Replies continue normally; no pause, interruption or task change was made. Only request a pause if you want them to stop or move on.", message.SenderID, message.RecipientID, message.ThreadID),
			}
			if _, err := insertMessage(ctx, tx, warning); err != nil {
				return err
			}
			state.Warned, state.WarningPending = true, false
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, data)
	return err
}
