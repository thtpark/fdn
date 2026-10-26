package foundation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/thtpark/fdn/internal/domain"
)

type updateCursor struct {
	Version    int    `json:"v"`
	Feed       string `json:"f"`
	Actor      string `json:"a"`
	Generation int64  `json:"g"`
	Sequence   int64  `json:"s"`
}

type updateWaiter struct {
	wake chan struct{}
	refs int
}

type eventScope struct {
	Project string `json:"project_id"`
	Task    string `json:"task_id"`
}

type updateHub struct {
	mu           sync.Mutex
	waiters      map[string]*updateWaiter
	eventWaiters map[eventScope]*updateWaiter
	closed       bool
	once         sync.Once
	err          error
}

func (s *Store) loadUpdateFeed(ctx context.Context) error {
	var key string
	if err := s.db.QueryRowContext(ctx, "SELECT feed_id,cursor_key FROM update_state WHERE id=1").Scan(&s.updateFeedID, &key); err != nil {
		return err
	}
	var err error
	s.updateCursorKey, err = hex.DecodeString(key)
	if err != nil || len(s.updateCursorKey) != sha256.Size || s.updateFeedID == "" {
		return domain.Fail(domain.Internal, "durable update feed identity is invalid")
	}
	return nil
}

func (s *Store) makeUpdateCursor(actor string, generation, sequence int64) string {
	data, _ := json.Marshal(updateCursor{Version: 1, Feed: s.updateFeedID, Actor: actor, Generation: generation, Sequence: sequence})
	signature := hmac.New(sha256.New, s.updateCursorKey)
	signature.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}

func (s *Store) readUpdateCursor(value, actor string, generation int64) (int64, error) {
	if len(value) > 4096 {
		return 0, domain.Fail(domain.Invalid, "update cursor is too long")
	}
	payload, signature, found := strings.Cut(value, ".")
	data, dataErr := base64.RawURLEncoding.DecodeString(payload)
	mac, macErr := base64.RawURLEncoding.DecodeString(signature)
	expected := hmac.New(sha256.New, s.updateCursorKey)
	expected.Write(data)
	var cursor updateCursor
	if !found || dataErr != nil || macErr != nil || !hmac.Equal(mac, expected.Sum(nil)) || json.Unmarshal(data, &cursor) != nil || cursor.Sequence < 0 {
		return 0, domain.Fail(domain.Invalid, "invalid update cursor")
	}
	if cursor.Actor != actor {
		return 0, domain.Fail(domain.Denied, "update cursor belongs to a different actor")
	}
	if cursor.Version != 1 || cursor.Feed != s.updateFeedID || cursor.Generation != generation {
		return 0, domain.Fail(domain.Conflict, "stale update cursor; initialize a current identity watermark and resnapshot")
	}
	return cursor.Sequence, nil
}

// Updates returns a bounded, actor/key-generation-scoped replay. An empty since
// initializes at one consistent current watermark and intentionally returns no
// history: callers must then snapshot/reconsider current work. NextCursor is
// always the continuation watermark, even on an empty or final page.
//
// Hints carry no authority, message body, implicit read receipt or new expiry.
// Consumers recheck authoritative records and retain their own deadline handling.
func (s *Store) Updates(ctx context.Context, actor, since string, limit int) (domain.Page[domain.Update], error) {
	page := domain.Page[domain.Update]{Items: []domain.Update{}}
	if err := s.updateOpen(); err != nil {
		return page, err
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
		return page, domain.Fail(domain.Denied, "update feed requires a current active identity")
	}
	var floor, watermark int64
	if err := snapshot.QueryRowContext(ctx, `SELECT floor_sequence,
COALESCE((SELECT seq FROM sqlite_sequence WHERE name='update_feed'),0) FROM update_state WHERE id=1`).Scan(&floor, &watermark); err != nil {
		return page, err
	}
	last := watermark
	if since != "" {
		last, err = s.readUpdateCursor(since, actor, generation)
		if err != nil {
			return page, err
		}
		if last > watermark {
			return page, domain.Fail(domain.Conflict, "update cursor is ahead of the current watermark")
		}
		if last < floor {
			return page, domain.Fail(domain.Conflict, "stale update cursor; retained replay begins after this watermark; resnapshot")
		}
		rows, err := snapshot.QueryContext(ctx, `SELECT f.sequence,f.topic,f.source_id,f.task_id,f.project_id,f.urgent
FROM update_recipients r JOIN update_feed f ON f.sequence=r.sequence
WHERE r.actor_id=? AND r.key_generation=? AND r.sequence>? AND r.sequence<=?
ORDER BY r.sequence LIMIT ?`, actor, generation, last, watermark, limit+1)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var update domain.Update
			if err := rows.Scan(&update.Sequence, &update.Topic, &update.SourceID, &update.TaskID, &update.ProjectID, &update.Urgent); err != nil {
				rows.Close()
				return page, err
			}
			page.Items = append(page.Items, update)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return page, err
		}
		if closeErr != nil {
			return page, closeErr
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last = page.Items[limit-1].Sequence
	} else {
		last = watermark
	}
	page.NextCursor = s.makeUpdateCursor(actor, generation, last)
	return page, nil
}

// WaitUpdates replays immediately, or sleeps on post-commit actor notifications.
// It never retains a SQL transaction/connection while idle and never polls.
// Notifications coalesce; the durable log, not an in-memory queue, holds replay.
func (s *Store) WaitUpdates(ctx context.Context, actor, since string, limit int) (domain.Page[domain.Update], error) {
	if since == "" {
		return s.Updates(ctx, actor, since, limit)
	}
	waiter, err := s.subscribeUpdates(actor)
	if err != nil {
		return domain.Page[domain.Update]{Items: []domain.Update{}}, err
	}
	defer s.unsubscribeUpdates(actor, waiter)
	return waitFeed(ctx, s, waiter, since, func(cursor string) (domain.Page[domain.Update], error) {
		return s.Updates(ctx, actor, cursor, limit)
	})
}

// WaitEvents preserves Events' team-readable scope, cursor and pagination
// contract. In particular, empty since replays from the beginning, not from a
// new watermark. Only a caught-up request waits, without holding SQL resources
// or polling. Private changes never notify this public project/task feed.
func (s *Store) WaitEvents(ctx context.Context, actor, project, task, since string, limit int) (domain.Page[domain.Event], error) {
	scope := eventScope{project, task}
	waiter, err := s.subscribeEvents(scope)
	if err != nil {
		return domain.Page[domain.Event]{Items: []domain.Event{}}, err
	}
	defer s.unsubscribeEvents(scope, waiter)
	return waitFeed(ctx, s, waiter, since, func(cursor string) (domain.Page[domain.Event], error) {
		return s.Events(ctx, actor, project, task, cursor, limit)
	})
}

func waitFeed[T any](ctx context.Context, s *Store, waiter *updateWaiter, since string, replay func(string) (domain.Page[T], error)) (domain.Page[T], error) {
	for {
		// Subscribe before replay. A commit must either be visible to that
		// query or close this channel (possibly both).
		s.updates.mu.Lock()
		wake, closed := waiter.wake, s.updates.closed
		s.updates.mu.Unlock()
		if closed {
			return domain.Page[T]{Items: []T{}}, domain.Fail(domain.Unavailable, "update store is closed")
		}
		page, err := replay(since)
		if err != nil {
			if ctx.Err() != nil {
				return page, ctx.Err()
			}
			if closedErr := s.updateOpen(); closedErr != nil {
				return page, closedErr
			}
			return page, err
		}
		if len(page.Items) != 0 {
			return page, nil
		}
		since = page.NextCursor
		select {
		case <-ctx.Done():
			return page, ctx.Err()
		case <-wake:
		}
	}
}

func (s *Store) updateOpen() error {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return domain.Fail(domain.Unavailable, "update store is closed")
	}
	return nil
}

func (s *Store) subscribeUpdates(actor string) (*updateWaiter, error) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return nil, domain.Fail(domain.Unavailable, "update store is closed")
	}
	if s.updates.waiters == nil {
		s.updates.waiters = make(map[string]*updateWaiter)
	}
	waiter := s.updates.waiters[actor]
	if waiter == nil {
		waiter = &updateWaiter{wake: make(chan struct{})}
		s.updates.waiters[actor] = waiter
	}
	waiter.refs++
	return waiter, nil
}

func (s *Store) unsubscribeUpdates(actor string, waiter *updateWaiter) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	waiter.refs--
	if waiter.refs == 0 {
		delete(s.updates.waiters, actor)
	}
}

func (s *Store) subscribeEvents(scope eventScope) (*updateWaiter, error) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return nil, domain.Fail(domain.Unavailable, "update store is closed")
	}
	if s.updates.eventWaiters == nil {
		s.updates.eventWaiters = make(map[eventScope]*updateWaiter)
	}
	waiter := s.updates.eventWaiters[scope]
	if waiter == nil {
		waiter = &updateWaiter{wake: make(chan struct{})}
		s.updates.eventWaiters[scope] = waiter
	}
	waiter.refs++
	return waiter, nil
}

func (s *Store) unsubscribeEvents(scope eventScope, waiter *updateWaiter) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	waiter.refs--
	if waiter.refs == 0 {
		delete(s.updates.eventWaiters, scope)
	}
}

func (s *Store) notifyUpdates(actors map[string]struct{}) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return
	}
	for actor := range actors {
		if waiter := s.updates.waiters[actor]; waiter != nil {
			close(waiter.wake)
			waiter.wake = make(chan struct{})
		}
	}
}

func (s *Store) notifyEvents(scopes map[eventScope]struct{}) {
	if len(scopes) == 0 {
		return
	}
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return
	}
	woken := make(map[*updateWaiter]struct{})
	for scope := range scopes {
		for _, key := range []eventScope{{}, {Project: scope.Project}, {Task: scope.Task}, scope} {
			waiter := s.updates.eventWaiters[key]
			if waiter == nil {
				continue
			}
			if _, ok := woken[waiter]; ok {
				continue
			}
			woken[waiter] = struct{}{}
			close(waiter.wake)
			waiter.wake = make(chan struct{})
		}
	}
}

func (s *Store) closeUpdates() error {
	s.updates.once.Do(func() {
		s.updates.mu.Lock()
		s.updates.closed = true
		close(s.closed)
		for _, waiter := range s.updates.waiters {
			close(waiter.wake)
		}
		for _, waiter := range s.updates.eventWaiters {
			close(waiter.wake)
		}
		s.updates.mu.Unlock()
		s.updates.err = s.db.Close()
	})
	return s.updates.err
}
