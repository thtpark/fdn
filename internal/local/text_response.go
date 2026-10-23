package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *State) Journal(ctx context.Context, id string) (Journal, error) {
	var journal Journal
	if !domain.ValidOperation(id) {
		return journal, domain.Fail(domain.Invalid, "exact invocation reference required")
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM journals WHERE id=?", id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return journal, domain.Fail(domain.NotFound, "invocation not found")
		}
		return journal, err
	}
	err := json.Unmarshal(data, &journal)
	return journal, err
}

// The response fence must survive together with its retained journal; neither a
// restart nor a lost recovery response may silently authorize another model call.
func (s *State) SaveTextResponse(ctx context.Context, journal Journal, actionHash string, block any) error {
	if !domain.ValidOperation(journal.ID) || !domain.ValidHash(actionHash) || journal.TextActionHash != actionHash ||
		(journal.State != "reply-rejected" && journal.State != "abandoned") || journal.Kind == domain.ExecutionRun || journal.Kind == domain.VerificationRun {
		return domain.Fail(domain.Invalid, "text response requires an exact non-execution journal and action fence")
	}
	journalData, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	blockData, err := json.Marshal(block)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "text-response:"+actionHash, blockData); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO journals(id,data,state,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,state=excluded.state`, journal.ID, journalData, journal.State, journal.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *State) TextResponseValues(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM kv WHERE key LIKE 'text-response:%' AND json_extract(value,'$.state')='blocked' ORDER BY key LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		items = append(items, json.RawMessage(data))
	}
	return items, rows.Err()
}
