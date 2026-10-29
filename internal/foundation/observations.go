package foundation

import (
	"context"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

type observationKey struct {
	Actor, Scope string
	Public       bool
}

type observationWaiter struct {
	waiter *updateWaiter
	filter domain.UpdateFilter
}

func observationClause(filter domain.UpdateFilter, actor, prefix string) (string, []any) {
	if filter.Empty() {
		return "", nil
	}
	var clauses []string
	var args []any
	for _, group := range []struct {
		column string
		values []string
	}{{"topic", filter.Topics}, {"source_id", filter.Sources}, {"task_id", filter.Tasks}, {"project_id", filter.Projects}} {
		if len(group.values) != 0 {
			clauses = append(clauses, prefix+group.column+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(group.values)), ",")+")")
			for _, value := range group.values {
				args = append(args, value)
			}
		}
	}
	if filter.Self {
		clauses = append(clauses, "("+prefix+"source_id=? AND "+prefix+"topic IN ('agent','agent-mode','agent-status'))")
		args = append(args, actor)
	}
	return " AND (" + strings.Join(clauses, " OR ") + ")", args
}

func (s *Store) subscribeObservation(actor string, filter domain.UpdateFilter, public bool) (observationKey, *updateWaiter, error) {
	key := observationKey{Actor: actor, Scope: filter.Fingerprint(), Public: public}
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return key, nil, domain.Fail(domain.Unavailable, "update store is closed")
	}
	if s.updates.observers == nil {
		s.updates.observers = make(map[observationKey]*observationWaiter)
	}
	entry := s.updates.observers[key]
	if entry == nil {
		if len(s.updates.observers) >= 256 {
			return key, nil, domain.Fail(domain.Unavailable, "scoped observer limit reached")
		}
		entry = &observationWaiter{waiter: &updateWaiter{wake: make(chan struct{})}, filter: filter}
		s.updates.observers[key] = entry
	}
	entry.waiter.refs++
	return key, entry.waiter, nil
}

func (s *Store) unsubscribeObservation(key observationKey) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	entry := s.updates.observers[key]
	if entry != nil {
		entry.waiter.refs--
		if entry.waiter.refs == 0 {
			delete(s.updates.observers, key)
		}
	}
}

func (s *Store) WaitScopedUpdates(ctx context.Context, actor, since string, limit int, filter domain.UpdateFilter) (domain.Page[domain.Update], error) {
	filter, err := filter.Normalize()
	if err != nil {
		return domain.Page[domain.Update]{}, err
	}
	if since == "" {
		return s.ScopedUpdates(ctx, actor, since, limit, filter)
	}
	key, waiter, err := s.subscribeObservation(actor, filter, false)
	if err != nil {
		return domain.Page[domain.Update]{}, err
	}
	defer s.unsubscribeObservation(key)
	return waitFeed(ctx, s, waiter, since, func(cursor string) (domain.Page[domain.Update], error) {
		return s.ScopedUpdates(ctx, actor, cursor, limit, filter)
	})
}

func (s *Store) ScopedEvents(ctx context.Context, actor, since string, limit int, filter domain.UpdateFilter) (domain.Page[domain.Event], error) {
	page := domain.Page[domain.Event]{Items: []domain.Event{}}
	filter, err := filter.Normalize()
	if err != nil {
		return page, err
	}
	limit, err = limitSize(limit)
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
		return page, domain.Fail(domain.Denied, "scoped events require a current active identity")
	}
	var watermark int64
	if err := snapshot.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events").Scan(&watermark); err != nil {
		return page, err
	}
	key := "observe-events:" + filter.Fingerprint() + ":" + strconv.FormatInt(generation, 10)
	last := watermark
	if since != "" {
		value, err := readCursor(since, actor, key)
		if err != nil {
			return page, err
		}
		last, err = strconv.ParseInt(value, 10, 64)
		if err != nil || last < 0 || last > watermark {
			return page, domain.Fail(domain.Conflict, "invalid or future scoped event cursor; resnapshot")
		}
		clause, arguments := observationClause(filter, actor, "")
		arguments = append([]any{last, watermark}, arguments...)
		arguments = append(arguments, limit+1)
		rows, err := snapshot.QueryContext(ctx, "SELECT sequence,topic,source_id,task_id,project_id,created_at FROM events WHERE sequence>? AND sequence<=?"+clause+" ORDER BY sequence LIMIT ?", arguments...)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var event domain.Event
			if err := rows.Scan(&event.Sequence, &event.Topic, &event.SourceID, &event.TaskID, &event.ProjectID, &event.CreatedAt); err != nil {
				rows.Close()
				return page, err
			}
			page.Items = append(page.Items, event)
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
		last = page.Items[len(page.Items)-1].Sequence
	} else {
		last = watermark
	}
	page.NextCursor = makeCursor(actor, key, strconv.FormatInt(last, 10))
	return page, nil
}

func (s *Store) WaitScopedEvents(ctx context.Context, actor, since string, limit int, filter domain.UpdateFilter) (domain.Page[domain.Event], error) {
	filter, err := filter.Normalize()
	if err != nil {
		return domain.Page[domain.Event]{}, err
	}
	if since == "" {
		return s.ScopedEvents(ctx, actor, since, limit, filter)
	}
	key, waiter, err := s.subscribeObservation(actor, filter, true)
	if err != nil {
		return domain.Page[domain.Event]{}, err
	}
	defer s.unsubscribeObservation(key)
	return waitFeed(ctx, s, waiter, since, func(cursor string) (domain.Page[domain.Event], error) {
		return s.ScopedEvents(ctx, actor, cursor, limit, filter)
	})
}

func (s *Store) notifyObservations(private []routedUpdate, public []domain.Update) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if s.updates.closed {
		return
	}
	for key, entry := range s.updates.observers {
		matches := false
		if key.Public {
			for _, update := range public {
				if entry.filter.Matches(update, key.Actor) {
					matches = true
					break
				}
			}
		} else {
			for _, update := range private {
				if _, party := update.parties[key.Actor]; party && entry.filter.Matches(update.update, key.Actor) {
					matches = true
					break
				}
			}
		}
		if matches {
			close(entry.waiter.wake)
			entry.waiter.wake = make(chan struct{})
		}
	}
}
