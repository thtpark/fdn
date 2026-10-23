package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

type WorkSnapshot struct {
	Values         map[string]json.RawMessage
	Journals       []Journal
	PendingReports int
}

// WorkSnapshot keeps phase and journal observations on one SQLite read snapshot.
func (s *State) WorkSnapshot(ctx context.Context, keys []string) (WorkSnapshot, error) {
	snapshot := WorkSnapshot{Values: map[string]json.RawMessage{}, Journals: []Journal{}}
	if len(keys) > 16 {
		return snapshot, domain.Fail(domain.Invalid, "work snapshot key budget exceeded")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	for _, key := range keys {
		var data []byte
		if err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", key).Scan(&data); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return snapshot, err
		}
		snapshot.Values[key] = json.RawMessage(data)
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM journals WHERE state NOT IN ('done','stale','abandoned','reply-rejected') ORDER BY created_at,id")
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var data []byte
		var journal Journal
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return snapshot, err
		}
		if err := json.Unmarshal(data, &journal); err != nil {
			rows.Close()
			return snapshot, err
		}
		snapshot.Journals = append(snapshot.Journals, journal)
	}
	readErr := rows.Err()
	if err := errors.Join(readErr, rows.Close()); err != nil {
		return snapshot, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox").Scan(&snapshot.PendingReports); err != nil {
		return snapshot, err
	}
	return snapshot, tx.Commit()
}
