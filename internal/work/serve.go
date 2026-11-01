package work

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type SessionRunner interface{ Run(context.Context) error }

func (e *Engine) Serve(ctx context.Context, sessions SessionRunner) (err error) {
	e.configMu.RLock()
	root := e.Config.StateDir
	e.configMu.RUnlock()
	lock, err := secure.Acquire(filepath.Join(root, "daemon.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.authorityMu.Lock()
	e.feedRunning, e.feedOnline = true, false
	e.authorityMu.Unlock()
	defer func() {
		e.setFeedOnline(false)
		e.authorityMu.Lock()
		e.feedRunning = false
		e.authorityMu.Unlock()
	}()
	wake := make(chan struct{}, 1)
	ready := make(chan struct{})
	watchDone, heartbeatDone, localDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { defer close(watchDone); e.watch(ctx, wake, ready, sessions) }()
	go func() { defer close(heartbeatDone); e.heartbeatLoop(ctx) }()
	go func() { defer close(localDone); e.observeLocal(ctx, wake, sessions) }()
	var operationDone chan struct{}
	var sessionDone chan error
	var sessionControl *sessionRuntime
	if sessions != nil {
		if manager, ok := sessions.(interface{ UseExternalUpdates() }); ok {
			manager.UseExternalUpdates()
		}
		sessionControl = newSessionRuntime(ctx, sessions)
		sessionDone = sessionControl.failure
		e.runtimeMu.Lock()
		e.sessions = sessionControl
		e.runtimeMu.Unlock()
	}
	defer func() {
		cancel()
		<-watchDone
		<-heartbeatDone
		<-localDone
		if operationDone != nil {
			<-operationDone
		}
		if sessionControl != nil {
			err = errors.Join(err, sessionControl.stop())
			e.runtimeMu.Lock()
			e.sessions = nil
			e.runtimeMu.Unlock()
		}
	}()
	// Establish the watermark before any initial business snapshot. Changes
	// racing that snapshot are replayed by the already-running scoped receiver.
	select {
	case <-ctx.Done():
		return nil
	case sessionErr := <-sessionDone:
		sessionDone = nil
		if sessionErr == nil {
			sessionErr = domain.Fail(domain.Internal, "interactive-session supervisor stopped unexpectedly")
		}
		e.recordProblem(context.WithoutCancel(ctx), "session-problem", sessionErr)
		return sessionErr
	case <-ready:
	}
	operationDone = make(chan struct{})
	go func() { defer close(operationDone); e.operationLoop(ctx, wake) }()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var timerC <-chan time.Time
	lastUpdate := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		checkpoint, readErr := e.updateCheckpoint(ctx, nil)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return readErr
		}
		nextProviderUpdate := time.Time{}
		if manager, ok := e.Executor.(*provider.Manager); ok {
			c, configErr := e.currentConfig()
			if configErr == nil && c.Mode == "ai" && c.AutomaticProviderUpdates {
				if lastUpdate.IsZero() || !e.Now().Before(lastUpdate.Add(time.Hour)) {
					lastUpdate = e.Now()
					if updateErr := manager.MaybeUpdate(ctx); updateErr != nil {
						e.recordProblem(context.WithoutCancel(ctx), "provider-update-problem", updateErr)
					} else {
						e.clearProblem(ctx, "provider-update-problem")
					}
				}
				nextProviderUpdate = lastUpdate.Add(time.Hour)
			}
		}
		if !checkpoint.Work.pending() && checkpoint.WorkDueAt != 0 && checkpoint.WorkDueAt <= e.Now().UnixNano() {
			checkpoint, err = e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
				p.WorkDueAt = 0
				p.request(updateWork)
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		if !checkpoint.Work.pending() {
			timerC = nil
			if checkpoint.WorkDueAt != 0 {
				timer.Reset(max(time.Unix(0, checkpoint.WorkDueAt).Sub(e.Now()), 0))
				timerC = timer.C
			}
			if !nextProviderUpdate.IsZero() && (checkpoint.WorkDueAt == 0 || nextProviderUpdate.UnixNano() < checkpoint.WorkDueAt) {
				timer.Reset(max(nextProviderUpdate.Sub(e.Now()), 0))
				timerC = timer.C
			}
			select {
			case <-ctx.Done():
				return nil
			case sessionErr := <-sessionDone:
				sessionDone = nil
				if sessionErr == nil {
					sessionErr = domain.Fail(domain.Internal, "interactive-session supervisor stopped unexpectedly")
				}
				e.recordProblem(context.WithoutCancel(ctx), "session-problem", sessionErr)
				return sessionErr
			case <-wake:
			case <-timerC:
			}
			timer.Stop()
			continue
		}
		c, loadErr := e.currentConfig()
		if loadErr != nil {
			e.recordProblem(context.WithoutCancel(ctx), "configuration-problem", loadErr)
			if err := e.finishUpdateWork(ctx, checkpoint, Status{}, loadErr, e.Now().Add(time.Second)); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			continue
		}
		e.clearProblem(ctx, "configuration-problem")
		schedule := &workDeadline{}
		stepCtx := context.WithValue(ctx, workDeadlineContext{}, schedule)
		status, stepErr := e.Step(stepCtx, false)
		if stepErr != nil {
			if ctx.Err() == nil {
				e.recordProblem(context.WithoutCancel(ctx), "last-problem", stepErr)
			}
		} else {
			e.clearProblem(ctx, "last-problem")
		}
		if ctx.Err() != nil {
			return nil
		}
		if stepErr != nil && domain.AsFault(stepErr).Code == domain.Unavailable {
			deferWorkUntil(stepCtx, e.Now().Add(time.Duration(c.PollSeconds)*time.Second))
		}
		pending, pendingErr := e.pendingWorkRetry(ctx, c)
		if pendingErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return pendingErr
		}
		if pending {
			deferWorkUntil(stepCtx, e.Now().Add(time.Duration(c.PollSeconds)*time.Second))
		}
		if err := e.finishUpdateWork(ctx, checkpoint, status, stepErr, schedule.at); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (e *Engine) finishUpdateWork(ctx context.Context, observed updateCheckpoint, status Status, stepErr error, due time.Time) error {
	_, err := e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
		p.Work.Handled = max(p.Work.Handled, observed.Work.Requested)
		if p.Work.Requested == observed.Work.Requested {
			p.WorkDueAt = 0
			if !due.IsZero() {
				p.WorkDueAt = due.UnixNano()
			}
		}
		if stepErr == nil {
			switch status.State {
			case "onboarding", "task", "verification", "urgent-message", "attention", "message", "research", "reconciled", "authentication-refreshed":
				p.request(updateWork)
			}
		}
	})
	return err
}

func (e *Engine) pendingWorkRetry(ctx context.Context, c config.Worker) (bool, error) {
	journals, err := e.State.Journals(ctx, true)
	if err != nil {
		return false, err
	}
	for _, journal := range journals {
		switch journal.State {
		case "claim-prepared", "claimed", "report-prepared":
			return true, nil
		case "reply-prepared", "applying":
			if journal.Kind == domain.ExecutionRun || journal.Kind == domain.VerificationRun || c.Mode == "ai" {
				return true, nil
			}
		}
	}
	outbox, err := e.State.Outbox(ctx)
	return len(outbox) != 0, err
}

func (e *Engine) heartbeatLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	transition := time.NewTimer(time.Hour)
	transition.Stop()
	defer transition.Stop()
	var pending <-chan time.Time
	var nextEnvironment time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.statusWake:
		case <-e.healthWake:
			// Coalesce ordinary activity transitions without waiting for the
			// next liveness heartbeat or waking business discovery.
			if pending == nil {
				transition.Reset(100 * time.Millisecond)
				pending = transition.C
			}
			continue
		case <-pending:
		case <-timer.C:
		}
		transition.Stop()
		pending = nil
		health, healthErr := e.health(ctx)
		e.observeProjectedActivity(health)
		if healthErr != nil {
			e.recordProblem(ctx, "heartbeat-problem", healthErr)
		}
		_, err := api.Call[domain.Agent](ctx, e.Client, "POST", "/v1/heartbeat", domain.HeartbeatRequest{Health: health})
		if err != nil {
			if ctx.Err() == nil {
				e.recordProblem(ctx, "heartbeat-problem", err)
			}
		} else if healthErr == nil {
			e.clearProblem(ctx, "heartbeat-problem")
		}
		if err == nil && !e.Now().Before(nextEnvironment) {
			nextEnvironment = e.Now().Add(60 * time.Second)
			if reportErr := e.publishEnvironment(ctx); reportErr != nil {
				if ctx.Err() == nil {
					e.recordProblem(ctx, "environment-problem", reportErr)
				}
			} else {
				e.clearProblem(ctx, "environment-problem")
			}
		}
		timer.Reset(30 * time.Second)
	}
}

func (e *Engine) localSafetyFingerprint(ctx context.Context) (string, error) {
	c, err := e.currentConfig()
	if err != nil {
		return "", err
	}
	snapshot, err := e.State.WorkSnapshot(ctx, []string{"maintenance", "session-fence", "inventory", "initial-review"})
	if err != nil {
		return "", err
	}
	bindings, err := e.State.Bindings(ctx)
	if err != nil {
		return "", err
	}
	type journalState struct{ ID, State string }
	type workspaceState struct {
		Binding local.Binding
		Problem string
	}
	observation := struct {
		Config     config.Worker
		Values     map[string]json.RawMessage
		Journals   []journalState
		Workspaces []workspaceState
		Blocks     []TextResponseBlock
		Reports    int
	}{Config: c, Values: snapshot.Values, Reports: snapshot.PendingReports}
	for _, journal := range snapshot.Journals {
		observation.Journals = append(observation.Journals, journalState{journal.ID, journal.State})
	}
	for _, binding := range bindings {
		item := workspaceState{Binding: binding}
		if _, err := e.State.Workspace(ctx, binding.Ref); err != nil {
			fault := domain.AsFault(err)
			item.Problem = string(fault.Code) + ":" + fault.Message
		}
		observation.Workspaces = append(observation.Workspaces, item)
	}
	observation.Blocks, err = e.textResponseBlocks(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(observation)
	return domain.Hash(string(data)), err
}

// Local configuration, workspace identity and explicit recovery are a separate
// safety boundary. Only a changed observation wakes business reconsideration.
func (e *Engine) observeLocal(ctx context.Context, wake chan<- struct{}, sessions SessionRunner) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	previous, err := e.localSafetyFingerprint(ctx)
	if err != nil {
		e.recordProblem(ctx, "configuration-problem", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		current, err := e.localSafetyFingerprint(ctx)
		if err != nil {
			e.recordProblem(ctx, "configuration-problem", err)
			previous = ""
			continue
		}
		e.clearProblem(ctx, "configuration-problem")
		health, healthErr := e.health(ctx)
		if healthErr != nil {
			e.recordProblem(ctx, "heartbeat-problem", healthErr)
		} else if e.observeProjectedActivity(health) {
			signalUpdate(e.healthWake)
		}
		if current == previous {
			continue
		}
		previous = current
		if err := e.requestUpdates(ctx, updateWork|updateMode, wake); err != nil {
			e.recordProblem(ctx, "watch-problem", err)
		}
		if manager, ok := sessions.(interface{ Wake() }); ok {
			manager.Wake()
		}
	}
}
