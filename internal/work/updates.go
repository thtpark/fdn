package work

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

const updateCheckpointKey = "daemon-updates"
const updateCheckpointVersion = 1

type updateTargets uint8

const (
	updateWork updateTargets = 1 << iota
	updateMode
	updateOperations
	updateAuthentication
	updateSessions
)

const initialUpdateTargets = updateWork | updateMode | updateOperations | updateAuthentication | updateSessions

type updateGeneration struct {
	Requested uint64 `json:"requested"`
	Handled   uint64 `json:"handled"`
}

func (g updateGeneration) pending() bool { return g.Requested != g.Handled }

// Cursor and unhandled generations are one durable write. A controller may only
// acknowledge its captured generation, never an update received during its step.
type updateCheckpoint struct {
	Version        int              `json:"version"`
	AgentID        string           `json:"agent_id"`
	Cursor         string           `json:"cursor"`
	Work           updateGeneration `json:"work"`
	Mode           updateGeneration `json:"mode"`
	Operations     updateGeneration `json:"operations"`
	Authentication updateGeneration `json:"authentication"`
	WorkDueAt      int64            `json:"work_due_at,omitempty"`
}

func (p *updateCheckpoint) request(targets updateTargets) {
	if targets&updateWork != 0 {
		p.Work.Requested++
	}
	if targets&updateMode != 0 {
		p.Mode.Requested++
	}
	if targets&updateOperations != 0 {
		p.Operations.Requested++
	}
	if targets&updateAuthentication != 0 {
		p.Authentication.Requested++
	}
}

func (e *Engine) updateCheckpoint(ctx context.Context, change func(*updateCheckpoint)) (updateCheckpoint, error) {
	e.updateMu.Lock()
	defer e.updateMu.Unlock()
	p := updateCheckpoint{Version: updateCheckpointVersion, AgentID: e.Client.Credential.AgentID}
	found, err := e.State.Get(ctx, updateCheckpointKey, &p)
	if err != nil {
		return p, err
	}
	if p.Version != updateCheckpointVersion || p.AgentID != e.Client.Credential.AgentID ||
		p.Work.Handled > p.Work.Requested || p.Mode.Handled > p.Mode.Requested ||
		p.Operations.Handled > p.Operations.Requested || p.Authentication.Handled > p.Authentication.Requested {
		return p, domain.Fail(domain.Prerequisite, "unsupported or mismatched update checkpoint; preserve local work and cursor")
	}
	previous := p
	if change != nil {
		change(&p)
		if !found || p != previous {
			err = e.State.Put(ctx, updateCheckpointKey, p)
		}
	}
	return p, err
}

func signalUpdate(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (e *Engine) requestUpdates(ctx context.Context, targets updateTargets, wake chan<- struct{}) error {
	if _, err := e.updateCheckpoint(ctx, func(p *updateCheckpoint) { p.request(targets) }); err != nil {
		return err
	}
	if targets&updateWork != 0 {
		signalUpdate(wake)
	}
	if targets&(updateMode|updateOperations|updateAuthentication) != 0 {
		signalUpdate(e.controlWake)
	}
	return nil
}

func updateRoutes(update domain.Update, actor string) updateTargets {
	switch update.Topic {
	case "agent-operation":
		return updateOperations
	case "agent-mode":
		if update.SourceID == actor {
			return updateWork | updateMode | updateSessions
		}
		return updateWork | updateSessions
	case "message":
		return updateWork | updateAuthentication
	case "session", "assistance":
		return updateWork | updateSessions
	case "project", "agent":
		targets := updateWork | updateSessions
		if update.Topic == "agent" && update.SourceID == actor {
			targets |= updateMode
		}
		return targets
	case "task", "run":
		return updateWork | updateSessions
	case "comment", "worklog", "attention":
		return updateWork
	default:
		// Unknown future invalidations must not silently strand approved work.
		return initialUpdateTargets
	}
}

func updatePath(cursor string, wait bool) string {
	query := url.Values{"limit": {"100"}}
	if cursor != "" {
		query.Set("since", cursor)
	}
	if wait {
		query.Set("wait", "true")
	}
	return "/v1/updates?" + query.Encode()
}

func (e *Engine) receiveUpdates(ctx context.Context, page domain.Page[domain.Update], initial bool, wake chan<- struct{}, sessions SessionRunner) error {
	if page.NextCursor == "" {
		return domain.Fail(domain.Unavailable, "update reception omitted its durable cursor")
	}
	targets := updateTargets(0)
	if initial {
		targets = initialUpdateTargets
	}
	for _, update := range page.Items {
		targets |= updateRoutes(update, e.Client.Credential.AgentID)
	}
	if _, err := e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
		if p.Cursor != page.NextCursor {
			p.Cursor = page.NextCursor
			p.request(targets)
		} else if initial {
			p.request(initialUpdateTargets)
		}
	}); err != nil {
		return err
	}
	e.setFeedOnline(true)
	for _, update := range page.Items {
		e.invalidateAuthority(update)
	}
	if targets&updateWork != 0 {
		signalUpdate(wake)
	}
	if targets&(updateMode|updateOperations|updateAuthentication) != 0 {
		signalUpdate(e.controlWake)
	}
	if targets&updateSessions != 0 {
		if manager, ok := sessions.(interface{ Wake() }); ok {
			manager.Wake()
		}
	}
	return nil
}

func (e *Engine) watch(ctx context.Context, wake chan<- struct{}, ready chan<- struct{}, sessions SessionRunner) {
	initial := true
	resync := true
	backoff := time.Second
	for ctx.Err() == nil {
		checkpoint, err := e.updateCheckpoint(ctx, nil)
		if err == nil {
			var page domain.Page[domain.Update]
			page, err = api.Call[domain.Page[domain.Update]](ctx, e.Client, http.MethodGet, updatePath(checkpoint.Cursor, !resync), nil)
			if err == nil {
				err = e.receiveUpdates(ctx, page, resync, wake, sessions)
			}
			resync = false
		}
		if err == nil {
			e.clearProblem(ctx, "watch-problem")
			if initial {
				close(ready)
				initial = false
			}
			backoff = time.Second
			continue
		}
		e.setFeedOnline(false)
		resync = true
		if ctx.Err() != nil {
			return
		}
		e.recordProblem(ctx, "watch-problem", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

type workDeadline struct{ at time.Time }
type workDeadlineContext struct{}

func deferWorkUntil(ctx context.Context, at time.Time) {
	if schedule, ok := ctx.Value(workDeadlineContext{}).(*workDeadline); ok && (schedule.at.IsZero() || at.Before(schedule.at)) {
		schedule.at = at
	}
}
