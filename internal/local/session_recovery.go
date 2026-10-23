package local

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

// CommitSessionRecovery keeps the exact fence transition and its immutable local
// operation result together; a lost remote reply never authorizes a second clear.
func (s *State) CommitSessionRecovery(ctx context.Context, sessionID, fingerprint, receiptKey string, receipt any) error {
	if !domain.ValidOperation(sessionID) || !domain.ValidHash(fingerprint) ||
		!strings.HasPrefix(receiptKey, "agent-operation-result:") || !domain.ValidOperation(strings.TrimPrefix(receiptKey, "agent-operation-result:")) {
		return domain.Fail(domain.Invalid, "session recovery requires exact fence and operation references")
	}
	result, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", receiptKey).Scan(&previous)
	if err == nil {
		if string(previous) != string(result) {
			return domain.Fail(domain.Conflict, "session recovery operation already has a different local result")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var data []byte
	if err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = 'session-fence'").Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Fail(domain.Conflict, "exact session fence is no longer present")
		}
		return err
	}
	if domain.Hash(string(data)) != fingerprint {
		return domain.Fail(domain.Conflict, "exact session fence changed; recovery evidence was retained")
	}
	var fence struct {
		Active        bool   `json:"active"`
		ID            string `json:"id"`
		CleanupProven bool   `json:"cleanup_proven"`
		Generation    string `json:"generation,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fence); err != nil {
		return domain.Fail(domain.Prerequisite, "session ownership evidence has an unsupported format; no recovery was performed")
	}
	if !fence.Active || fence.ID != sessionID {
		return domain.Fail(domain.Conflict, "exact active session fence does not match")
	}
	if !fence.CleanupProven {
		return domain.Fail(domain.Prerequisite, "session cleanup lacks durable native proof; an authorized native operator is required")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE kv SET value = ? WHERE key = 'session-fence'", []byte(`{"active":false,"id":""}`)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES(?,?)", receiptKey, result); err != nil {
		return err
	}
	return tx.Commit()
}
