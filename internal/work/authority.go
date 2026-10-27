package work

import (
	"context"
	"net/http"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

type authoritySubscription struct {
	taskID, projectID string
	modeOnly          bool
	wake              chan struct{}
	failure           error
}

func (s *authoritySubscription) relevant(update domain.Update) bool {
	switch update.Topic {
	case "agent", "agent-mode":
		return true
	case "project":
		return !s.modeOnly && (update.ProjectID == s.projectID || update.SourceID == s.projectID)
	case "task", "run", "comment", "worklog":
		return !s.modeOnly && (update.TaskID == s.taskID || update.SourceID == s.taskID)
	case "message", "request", "attention", "agent-operation", "session", "assistance":
		return false
	default:
		return true
	}
}

func (e *Engine) setFeedOnline(online bool) {
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	e.feedOnline = online
	if !online {
		for subscription := range e.authorities {
			subscription.failure = domain.Fail(domain.Unavailable, "current authority update reception was interrupted")
			signalUpdate(subscription.wake)
		}
	}
}

func (e *Engine) invalidateAuthority(update domain.Update) {
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	for subscription := range e.authorities {
		if subscription.relevant(update) {
			signalUpdate(subscription.wake)
		}
	}
}

func (e *Engine) authorityFailure(subscription *authoritySubscription) error {
	e.authorityMu.Lock()
	defer e.authorityMu.Unlock()
	return subscription.failure
}

// Admission subscribes before validating. Notifications racing validation remain
// buffered; a disconnected subscription cannot become healthy after reconnect.
func (e *Engine) guardAuthority(ctx context.Context, cancel context.CancelCauseFunc, subscription *authoritySubscription, validate func(context.Context) error) error {
	subscription.wake = make(chan struct{}, 1)
	e.authorityMu.Lock()
	shared := e.feedRunning
	if shared && !e.feedOnline {
		e.authorityMu.Unlock()
		return domain.Fail(domain.Unavailable, "current authority update reception is unavailable")
	}
	if shared {
		e.authorities[subscription] = struct{}{}
	}
	e.authorityMu.Unlock()
	unsubscribe := func() {
		e.authorityMu.Lock()
		delete(e.authorities, subscription)
		e.authorityMu.Unlock()
	}
	var cursor string
	if !shared {
		// Manual work has no daemon receiver, but must have the same race-free
		// cancellation boundary, not an unguarded or periodically polled run.
		page, err := api.Call[domain.Page[domain.Update]](ctx, e.Client, http.MethodGet, updatePath("", false), nil)
		if err != nil {
			return err
		}
		if page.NextCursor == "" {
			return domain.Fail(domain.Unavailable, "authority subscription omitted its initial cursor")
		}
		cursor = page.NextCursor
	}
	if err := validate(ctx); err != nil {
		unsubscribe()
		return err
	}
	if err := e.authorityFailure(subscription); err != nil {
		unsubscribe()
		return err
	}
	if !shared {
		go func() {
			for ctx.Err() == nil {
				page, err := api.Call[domain.Page[domain.Update]](ctx, e.Client, http.MethodGet, updatePath(cursor, true), nil)
				if err == nil && page.NextCursor == "" {
					err = domain.Fail(domain.Unavailable, "authority update reception omitted its cursor")
				}
				if err != nil {
					if ctx.Err() == nil {
						cancel(err)
					}
					return
				}
				cursor = page.NextCursor
				for _, update := range page.Items {
					if subscription.relevant(update) {
						signalUpdate(subscription.wake)
					}
				}
			}
		}()
	}
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case <-subscription.wake:
			}
			if err := e.authorityFailure(subscription); err != nil {
				cancel(err)
				return
			}
			if err := validate(ctx); err != nil {
				cancel(err)
				return
			}
		}
	}()
	return nil
}

func (e *Engine) validateRunMode(ctx context.Context, manual bool) error {
	me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return err
	}
	if me.ID != e.Client.Credential.AgentID || !me.Active || me.Banned || (!manual && me.Mode != "ai") || (manual && !me.Founder && me.Mode != "ai") {
		return domain.Fail(domain.Denied, "current identity or Founder mode authorization changed")
	}
	return nil
}
