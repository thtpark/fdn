package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

const schema = `
CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE knowledge (id TEXT PRIMARY KEY, data BLOB NOT NULL, archived INTEGER NOT NULL);
CREATE TABLE knowledge_history (id TEXT NOT NULL, revision INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(id, revision));
CREATE TABLE bindings (id TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE journals (id TEXT PRIMARY KEY, data BLOB NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE INDEX journals_pending ON journals(state, created_at);
CREATE TABLE outbox (operation_id TEXT PRIMARY KEY, method TEXT NOT NULL, path TEXT NOT NULL, body BLOB NOT NULL, created_at INTEGER NOT NULL);
`

type State struct {
	db   *sql.DB
	Root string
}

func Open(ctx context.Context, root string) (*State, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, filepath.Join(root, "local.sqlite"), []string{schema}, 1)
	if err != nil {
		return nil, err
	}
	return &State{db: db, Root: root}, nil
}

func (s *State) Close() error { return s.db.Close() }

func (s *State) BindIdentity(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = 'identity'").Scan(&existing)
	if err == nil && existing != agentID {
		return domain.Fail(domain.Denied, "local state is bound to a different enrolled identity; do not borrow credentials")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('identity',?) ON CONFLICT DO NOTHING", agentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *State) Get(ctx context.Context, key string, value any) (bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key = ?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, value)
}

func (s *State) Put(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, data)
	return err
}

type Journal struct {
	ID                   string                `json:"id"`
	Kind                 string                `json:"kind"`
	State                string                `json:"state"`
	TaskID               string                `json:"task_id,omitempty"`
	SpecGeneration       int64                 `json:"spec_generation,omitempty"`
	DiscussionGeneration int64                 `json:"discussion_generation,omitempty"`
	Provider             string                `json:"provider"`
	ProviderVersion      string                `json:"provider_version,omitempty"`
	Model                string                `json:"model,omitempty"`
	WorkspaceRef         string                `json:"workspace_ref,omitempty"`
	Token                string                `json:"token,omitempty"`
	Claim                *domain.ClaimRequest  `json:"claim,omitempty"`
	RunID                string                `json:"run_id,omitempty"`
	Reply                json.RawMessage       `json:"reply,omitempty"`
	Report               *domain.ReportRequest `json:"report,omitempty"`
	Context              json.RawMessage       `json:"context,omitempty"`
	PromptHash           string                `json:"prompt_hash"`
	PID                  int                   `json:"pid,omitempty"`
	CreatedAt            int64                 `json:"created_at"`
	Problem              string                `json:"problem,omitempty"`
	TextActionHash       string                `json:"text_action_hash,omitempty"`
}

func (s *State) SaveJournal(ctx context.Context, j Journal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO journals(id,data,state,created_at) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data,state=excluded.state`, j.ID, data, j.State, j.CreatedAt)
	return err
}

func (s *State) Journals(ctx context.Context, pending bool) ([]Journal, error) {
	query := "SELECT data FROM journals"
	if pending {
		query += " WHERE state NOT IN ('done','stale','abandoned','reply-rejected')"
	}
	query += " ORDER BY created_at,id"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Journal{}
	for rows.Next() {
		var data []byte
		var j Journal
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type Outbox struct {
	OperationID string          `json:"operation_id"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Body        json.RawMessage `json:"body"`
	CreatedAt   int64           `json:"created_at"`
}

func (s *State) Enqueue(ctx context.Context, item Outbox) error {
	var command domain.Command
	if err := json.Unmarshal(item.Body, &command); err != nil {
		return err
	}
	if command.OperationID != item.OperationID || !domain.ValidOperation(item.OperationID) {
		return domain.Fail(domain.Invalid, "outbox operation does not match its body")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var method, path string
	var body []byte
	err = tx.QueryRowContext(ctx, "SELECT method,path,body FROM outbox WHERE operation_id = ?", item.OperationID).Scan(&method, &path, &body)
	if err == nil {
		if method != item.Method || path != item.Path || string(body) != string(item.Body) {
			return domain.Fail(domain.Conflict, "outbox operation already has a different immutable proposal")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = time.Now().UTC().Unix()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO outbox(operation_id,method,path,body,created_at) VALUES(?,?,?,?,?)", item.OperationID, item.Method, item.Path, item.Body, item.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *State) Outbox(ctx context.Context) ([]Outbox, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT operation_id,method,path,body,created_at FROM outbox ORDER BY created_at,operation_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Outbox{}
	for rows.Next() {
		var item Outbox
		if err := rows.Scan(&item.OperationID, &item.Method, &item.Path, &item.Body, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *State) Acknowledge(ctx context.Context, operation string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM outbox WHERE operation_id = ?", operation)
	return err
}
