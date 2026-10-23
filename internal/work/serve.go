package work

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type SessionRunner interface{ Run(context.Context) error }

func (e *Engine) Serve(ctx context.Context, sessions SessionRunner) (err error) {
	lock, err := secure.Acquire(filepath.Join(e.Config.StateDir, "daemon.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := make(chan struct{}, 1)
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); e.watch(ctx, wake) }()
	operationDone := make(chan struct{})
	go func() { defer close(operationDone); e.operationLoop(ctx, wake) }()
	var sessionDone chan error
	if sessions != nil {
		sessionDone = make(chan error, 1)
		go func() { sessionDone <- sessions.Run(ctx) }()
	}
	defer func() {
		cancel()
		<-watchDone
		<-operationDone
		if sessionDone != nil {
			sessionErr := <-sessionDone
			if sessionErr != nil && !errors.Is(sessionErr, context.Canceled) {
				err = errors.Join(err, sessionErr)
			}
		}
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastUpdate := time.Time{}
	for {
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
		case <-timer.C:
		}
		c, loadErr := e.currentConfig()
		if loadErr != nil {
			e.recordProblem(context.WithoutCancel(ctx), "configuration-problem", loadErr)
			timer.Reset(time.Second)
			continue
		}
		e.clearProblem(ctx, "configuration-problem")
		me, modeErr := api.Call[domain.Agent](ctx, e.Client, "GET", "/v1/me", nil)
		if modeErr == nil {
			c, modeErr = e.applyMode(ctx, c, me)
		}
		if modeErr != nil {
			if ctx.Err() == nil {
				e.recordProblem(context.WithoutCancel(ctx), "mode-control-problem", modeErr)
			}
			timer.Reset(time.Duration(c.PollSeconds) * time.Second)
			continue
		}
		e.clearProblem(ctx, "mode-control-problem")
		if c.Mode == "ai" && c.AutomaticProviderUpdates && e.Now().Sub(lastUpdate) > time.Hour {
			lastUpdate = e.Now()
			if manager, ok := e.Executor.(*provider.Manager); ok {
				if updateErr := manager.MaybeUpdate(ctx); updateErr != nil {
					e.recordProblem(context.WithoutCancel(ctx), "provider-update-problem", updateErr)
				} else {
					e.clearProblem(ctx, "provider-update-problem")
				}
			}
		}
		if _, stepErr := e.Step(ctx, false); stepErr != nil {
			if ctx.Err() == nil {
				e.recordProblem(context.WithoutCancel(ctx), "last-problem", stepErr)
			}
		} else {
			e.clearProblem(ctx, "last-problem")
		}
		timer.Reset(time.Duration(c.PollSeconds) * time.Second)
	}
}

func (e *Engine) watch(ctx context.Context, wake chan<- struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cursor string
	lastHeartbeat := time.Time{}
	initialConfig, err := e.currentConfig()
	if err != nil {
		e.recordProblem(ctx, "watch-problem", err)
		return
	}
	lastMode := initialConfig.Mode
	if _, err := e.State.Get(ctx, "daemon-watch-cursor", &cursor); err != nil {
		e.recordProblem(ctx, "watch-problem", err)
		return
	}
	for {
		forceStatus := false
		select {
		case <-ctx.Done():
			return
		case <-e.statusWake:
			forceStatus = true
		case <-ticker.C:
		}
		if c, err := e.currentConfig(); err != nil {
			e.recordProblem(ctx, "configuration-problem", err)
		} else {
			e.clearProblem(ctx, "configuration-problem")
			if c.Mode != lastMode {
				lastMode = c.Mode
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
		if forceStatus || e.Now().Sub(lastHeartbeat) >= 30*time.Second {
			health, healthErr := e.health(ctx)
			if healthErr != nil {
				e.recordProblem(ctx, "heartbeat-problem", healthErr)
			}
			if _, err := api.Call[domain.Agent](ctx, e.Client, "POST", "/v1/heartbeat", domain.HeartbeatRequest{Health: health}); err != nil {
				if ctx.Err() == nil {
					e.recordProblem(ctx, "heartbeat-problem", err)
				}
			} else {
				lastHeartbeat = e.Now()
				if healthErr == nil {
					e.clearProblem(ctx, "heartbeat-problem")
				}
			}
		}
		path := "/v1/events?limit=100"
		if cursor != "" {
			path += "&since=" + cursor
		}
		page, err := api.Call[domain.Page[domain.Event]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			if ctx.Err() == nil {
				e.recordProblem(ctx, "watch-problem", err)
			}
			continue
		}
		if len(page.Items) > 0 {
			modeChanged := false
			for _, event := range page.Items {
				if event.Topic == "agent-mode" && event.SourceID == e.Client.Credential.AgentID {
					modeChanged = true
				}
			}
			if modeChanged {
				me, err := api.Call[domain.Agent](ctx, e.Client, "GET", "/v1/me", nil)
				if err == nil {
					c, loadErr := e.currentConfig()
					if loadErr != nil {
						err = loadErr
					} else {
						_, err = e.applyMode(ctx, c, me)
					}
				}
				if err != nil {
					e.recordProblem(ctx, "mode-control-problem", err)
					continue
				}
				e.clearProblem(ctx, "mode-control-problem")
			}
			if err := e.State.Put(ctx, "pending-wake", page.Items[len(page.Items)-1].Sequence); err != nil {
				e.recordProblem(ctx, "watch-problem", err)
				continue
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		if err := e.State.Put(ctx, "daemon-watch-cursor", page.NextCursor); err != nil {
			e.recordProblem(ctx, "watch-problem", err)
			continue
		}
		cursor = page.NextCursor
		inbox, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, "GET", "/v1/messages?urgent=true&limit=1", nil)
		if err != nil {
			if ctx.Err() == nil {
				e.recordProblem(ctx, "watch-problem", err)
			}
			continue
		}
		if len(inbox.Items) > 0 {
			if err := e.State.Put(ctx, "pending-urgent-message", inbox.Items[0].ID); err != nil {
				e.recordProblem(ctx, "watch-problem", err)
				continue
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		e.clearProblem(ctx, "watch-problem")
	}
}
