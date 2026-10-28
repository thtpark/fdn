package local

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

type sessionRecoveryFence struct {
	Active        bool   `json:"active"`
	ID            string `json:"id"`
	CleanupProven bool   `json:"cleanup_proven"`
	Generation    string `json:"generation,omitempty"`
}

func decodeSessionRecoveryFence(data []byte) (sessionRecoveryFence, error) {
	var fence sessionRecoveryFence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fence); err != nil {
		return fence, domain.Fail(domain.Prerequisite, "session ownership evidence has an unsupported format; no recovery was performed")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fence, domain.Fail(domain.Prerequisite, "session ownership evidence has an unsupported format; no recovery was performed")
	}
	return fence, nil
}

func (s *State) ProveSessionCleanup(ctx context.Context, sessionID, fingerprint string) error {
	if !domain.ValidOperation(sessionID) || !domain.ValidHash(fingerprint) {
		return domain.Fail(domain.Invalid, "native cleanup requires the exact session fence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key='session-fence'").Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Fail(domain.Conflict, "exact session fence is no longer present")
		}
		return err
	}
	if domain.Hash(string(data)) != fingerprint {
		return domain.Fail(domain.Conflict, "session fence changed before native cleanup proof could be committed")
	}
	fence, err := decodeSessionRecoveryFence(data)
	if err != nil {
		return err
	}
	if !fence.Active || fence.ID != sessionID {
		return domain.Fail(domain.Conflict, "native cleanup belongs to another session fence")
	}
	fence.CleanupProven = true
	data, err = json.Marshal(fence)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE kv SET value=? WHERE key='session-fence'", data); err != nil {
		return err
	}
	return tx.Commit()
}

// CommitSessionRecovery keeps the exact fence transition and its immutable local
// operation result together; a lost remote reply never authorizes a second clear.
func (s *State) CommitSessionRecovery(ctx context.Context, sessionID, fingerprint, receiptKey string, receipt any) error {
	return s.commitSessionRecovery(ctx, sessionID, fingerprint, receiptKey, "agent-operation-result:", receipt)
}

func (s *State) CommitSessionCleanup(ctx context.Context, sessionID, fingerprint, receiptKey string, receipt any) error {
	return s.commitSessionRecovery(ctx, sessionID, fingerprint, receiptKey, "session-cleanup-result:", receipt)
}

func (s *State) commitSessionRecovery(ctx context.Context, sessionID, fingerprint, receiptKey, prefix string, receipt any) error {
	if !domain.ValidOperation(sessionID) || !domain.ValidHash(fingerprint) ||
		!strings.HasPrefix(receiptKey, prefix) || !domain.ValidOperation(strings.TrimPrefix(receiptKey, prefix)) {
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
	fence, err := decodeSessionRecoveryFence(data)
	if err != nil {
		return err
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
