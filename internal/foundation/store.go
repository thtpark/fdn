package foundation

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

const schema = `
CREATE TABLE agents (id TEXT PRIMARY KEY, data BLOB NOT NULL, credential_hash TEXT NOT NULL UNIQUE, active INTEGER NOT NULL);
CREATE TABLE invitations (id TEXT PRIMARY KEY, data BLOB NOT NULL, token_hash TEXT NOT NULL UNIQUE);
CREATE TABLE projects (id TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE project_creators (agent_id TEXT PRIMARY KEY REFERENCES agents(id));
CREATE TABLE tasks (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id),
 assignee_id TEXT NOT NULL, status TEXT NOT NULL, priority INTEGER NOT NULL, archived INTEGER NOT NULL
);
CREATE INDEX tasks_assignment ON tasks(assignee_id, status, archived, id);
CREATE INDEX tasks_project ON tasks(project_id, archived, id);
CREATE INDEX tasks_active_agent ON tasks(json_extract(data, '$.active_run.agent_id'));
CREATE TABLE runs (id TEXT PRIMARY KEY, data BLOB NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), agent_id TEXT NOT NULL REFERENCES agents(id), token_hash TEXT NOT NULL);
CREATE INDEX runs_task ON runs(task_id, id);
CREATE TABLE entries (id TEXT PRIMARY KEY, data BLOB NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), author_id TEXT NOT NULL REFERENCES agents(id), kind TEXT NOT NULL);
CREATE INDEX entries_task ON entries(task_id, id);
CREATE TABLE attention (
 agent_id TEXT NOT NULL REFERENCES agents(id), entry_id TEXT NOT NULL REFERENCES entries(id),
 generation INTEGER NOT NULL, task_id TEXT NOT NULL, state TEXT NOT NULL,
 PRIMARY KEY(agent_id, entry_id, generation)
);
CREATE INDEX attention_pending ON attention(agent_id, state, entry_id);
CREATE TABLE messages (id TEXT PRIMARY KEY, data BLOB NOT NULL, sender_id TEXT NOT NULL REFERENCES agents(id), recipient_id TEXT NOT NULL REFERENCES agents(id), state TEXT NOT NULL, urgent INTEGER NOT NULL);
CREATE INDEX messages_inbox ON messages(recipient_id, state, urgent DESC, id);
CREATE TABLE sessions (id TEXT PRIMARY KEY, data BLOB NOT NULL, requester_id TEXT NOT NULL REFERENCES agents(id), target_id TEXT NOT NULL REFERENCES agents(id), state TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE INDEX sessions_target ON sessions(target_id, state, expires_at);
CREATE TABLE histories (sequence INTEGER PRIMARY KEY AUTOINCREMENT, source_id TEXT NOT NULL, actor_id TEXT NOT NULL, action TEXT NOT NULL, reason TEXT NOT NULL, created_at INTEGER NOT NULL, snapshot BLOB NOT NULL);
CREATE INDEX histories_source ON histories(source_id, sequence DESC);
CREATE TABLE events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, topic TEXT NOT NULL, source_id TEXT NOT NULL, task_id TEXT NOT NULL, project_id TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE INDEX events_project ON events(project_id, sequence);
CREATE TABLE operations (actor_id TEXT NOT NULL, operation_id TEXT NOT NULL, fingerprint TEXT NOT NULL, result BLOB NOT NULL, PRIMARY KEY(actor_id, operation_id));
CREATE TABLE audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT, actor_id TEXT NOT NULL, action TEXT NOT NULL, outcome TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

const autonomousSchema = `
UPDATE agents SET data = json_set(data, '$.revision', 1, '$.mode',
 CASE WHEN json_extract(data, '$.founder') = 1 THEN 'manual' ELSE 'ai' END);
CREATE TABLE assistance_grants (id TEXT PRIMARY KEY, data BLOB NOT NULL,
 requester_id TEXT NOT NULL REFERENCES agents(id), target_id TEXT NOT NULL REFERENCES agents(id), active INTEGER NOT NULL);
CREATE INDEX assistance_parties ON assistance_grants(requester_id,target_id,active,id);
`

type Store struct {
	db  *sql.DB
	now func() time.Time

	writer          chan struct{}
	writerWaiters   atomic.Int64
	closed          chan struct{}
	updates         updateHub
	updateFeedID    string
	updateCursorKey []byte
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := database.Open(ctx, path, []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, notesSchema}, 5)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, now: time.Now, writer: make(chan struct{}, 1), closed: make(chan struct{})}
	if err := store.loadUpdateFeed(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error                    { return s.closeUpdates() }
func (s *Store) Ping(ctx context.Context) error  { return s.db.PingContext(ctx) }
func (s *Store) Check(ctx context.Context) error { return database.Check(ctx, s.db) }
func (s *Store) timestamp() int64                { return s.now().UTC().Unix() }

// SQLite already serializes writes. Admit them before leasing a connection so
// competing IMMEDIATE transactions cannot occupy the entire WAL reader pool.
// Callers always defer finish, including on replay, denial and storage failure.
func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.writerWaiters.Add(1)
	select {
	case <-ctx.Done():
		s.writerWaiters.Add(-1)
		return nil, nil, ctx.Err()
	case <-s.closed:
		s.writerWaiters.Add(-1)
		return nil, nil, domain.Fail(domain.Unavailable, "update store is closed")
	case s.writer <- struct{}{}:
		s.writerWaiters.Add(-1)
	}
	release := func() { <-s.writer }
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	select {
	case <-s.closed:
		release()
		return nil, nil, domain.Fail(domain.Unavailable, "update store is closed")
	default:
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		release()
		select {
		case <-s.closed:
			return nil, nil, domain.Fail(domain.Unavailable, "update store is closed")
		default:
		}
		return nil, nil, err
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			tx.Rollback()
			release()
		})
	}
	return tx, finish, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func get[T any](ctx context.Context, q queryer, table, id string) (T, error) {
	var value T
	var data []byte
	err := q.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return value, domain.Fail(domain.NotFound, "record not found")
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	return value, err
}

func encode(value any) ([]byte, error) { return json.Marshal(value) }

func (s *Store) actor(ctx context.Context, q queryer, id string) (domain.Agent, error) {
	agent, err := activeIdentitySubject(ctx, q, id)
	if err != nil {
		return agent, err
	}
	if err := s.checkActorIdentityFence(ctx, q, id); err != nil {
		return agent, err
	}
	return agent, nil
}

func transact[T any](ctx context.Context, s *Store, actor, action, op string, request any, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	if !domain.ValidOperation(op) {
		return zero, domain.Fail(domain.Invalid, "a valid operation_id is required")
	}
	requestBytes, err := encode(struct {
		Action string `json:"action"`
		Input  any    `json:"input"`
	}{action, request})
	if err != nil {
		return zero, err
	}
	fingerprint := domain.Hash(string(requestBytes))
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return zero, err
	}
	defer finish()
	var previousHash string
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT fingerprint, result FROM operations WHERE actor_id = ? AND operation_id = ?", actor, op).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != fingerprint {
			return zero, domain.Fail(domain.Conflict, "operation_id was already used for a different request")
		}
		if len(actor) > 6 && actor[:6] == "agent_" {
			if _, err := s.actor(ctx, tx, actor); err != nil {
				return zero, err
			}
		}
		return replayResult[T](ctx, tx, actor, previous)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT mutation"); err != nil {
		return zero, err
	}
	result, applyErr := fn(tx)
	if applyErr != nil {
		var fault *domain.Fault
		if !errors.As(applyErr, &fault) {
			return zero, applyErr
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO mutation"); err != nil {
			return zero, errors.Join(applyErr, err)
		}
		if err := s.audit(ctx, tx, actor, action, string(fault.Code)); err != nil {
			return zero, errors.Join(applyErr, err)
		}
		if err := s.commit(ctx, tx); err != nil {
			return zero, errors.Join(applyErr, err)
		}
		return zero, applyErr
	}
	response, err := resultReceipt(result)
	if err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id, operation_id, fingerprint, result) VALUES(?, ?, ?, ?)", actor, op, fingerprint, response); err != nil {
		return zero, err
	}
	if err = s.audit(ctx, tx, actor, action, "accepted"); err != nil {
		return zero, err
	}
	if err = s.commit(ctx, tx); err != nil {
		return zero, err
	}
	return result, nil
}

func mutate[T any](ctx context.Context, s *Store, actor, action string, command domain.Command, request any, fn func(*sql.Tx, domain.Agent) (T, error)) (T, error) {
	return transact(ctx, s, actor, action, command.OperationID, request, func(tx *sql.Tx) (T, error) {
		var zero T
		a, err := s.actor(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		return fn(tx, a)
	})
}

func (s *Store) audit(ctx context.Context, tx *sql.Tx, actor, action, outcome string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit(actor_id, action, outcome, created_at) VALUES(?, ?, ?, ?)", actor, action, outcome, s.timestamp())
	return err
}

func (s *Store) history(ctx context.Context, tx *sql.Tx, source, actor, action string, value any, reasons ...string) error {
	data, err := encode(value)
	if err != nil {
		return err
	}
	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO histories(source_id, actor_id, action, reason, created_at, snapshot) VALUES(?, ?, ?, ?, ?, ?)", source, actor, action, reason, s.timestamp(), data); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM histories WHERE source_id = ? AND sequence NOT IN (SELECT sequence FROM histories WHERE source_id = ? ORDER BY sequence DESC LIMIT ?)", source, source, domain.HistoryLimit)
	return err
}

func (s *Store) event(ctx context.Context, tx *sql.Tx, topic, source, task, project string) error {
	result, err := tx.ExecContext(ctx, "INSERT INTO events(topic, source_id, task_id, project_id, created_at) VALUES(?, ?, ?, ?, ?)", topic, source, task, project, s.timestamp())
	if err != nil {
		return err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO update_changes(source_table,source_id,before_data,after_data)
VALUES('events',?,NULL,json_object('project_id',?,'task_id',?))`, sequence, project, task)
	return err
}

func expect(actual, requested int64) error {
	if requested < 1 {
		return domain.Fail(domain.Invalid, "expected_revision is required")
	}
	if actual != requested {
		return domain.Fail(domain.Conflict, "revision changed; read current state before deciding again")
	}
	return nil
}

func limitSize(limit int) (int, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return 0, domain.Fail(domain.Invalid, "limit must be between 1 and 200")
	}
	return limit, nil
}

type cursor struct {
	Actor string `json:"a"`
	Query string `json:"q"`
	Last  string `json:"l"`
}

func makeCursor(actor, query, last string) string {
	data, _ := json.Marshal(cursor{Actor: actor, Query: domain.Hash(query), Last: last})
	return base64.RawURLEncoding.EncodeToString(data)
}

func readCursor(value, actor, query string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 4096 {
		return "", domain.Fail(domain.Invalid, "cursor is too long")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	var c cursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Actor != actor || c.Query != domain.Hash(query) {
		return "", domain.Fail(domain.Conflict, "cursor belongs to a different actor or query")
	}
	return c.Last, nil
}

func scanJSON[T any](rows *sql.Rows) ([]T, error) {
	defer rows.Close()
	values := []T{}
	for rows.Next() {
		var data []byte
		var value T
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("decode stored record: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
