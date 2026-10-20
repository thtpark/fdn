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
	var sessionDone chan error
	if sessions != nil {
		sessionDone = make(chan error, 1)
		go func() { sessionDone <- sessions.Run(ctx) }()
	}
	defer func() {
		cancel()
		<-watchDone
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
				return domain.Fail(domain.Internal, "interactive-session supervisor stopped unexpectedly")
			}
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
		if c.Mode == "ai" && c.AutomaticProviderUpdates && e.Now().Sub(lastUpdate) > time.Hour {
			lastUpdate = e.Now()
			if manager, ok := e.Executor.(*provider.Manager); ok {
				if updateErr := manager.MaybeUpdate(ctx); updateErr != nil {
					e.recordProblem(context.WithoutCancel(ctx), "provider-update-problem", updateErr)
				}
			}
		}
		if _, stepErr := e.Step(ctx, false); stepErr != nil && ctx.Err() == nil {
			e.recordProblem(context.WithoutCancel(ctx), "last-problem", stepErr)
		}
		timer.Reset(time.Duration(c.PollSeconds) * time.Second)
	}
}

func (e *Engine) watch(ctx context.Context, wake chan<- struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cursor string
	lastHeartbeat := time.Time{}
	lastMode := e.Config.Mode
	if _, err := e.State.Get(ctx, "daemon-watch-cursor", &cursor); err != nil {
		e.recordProblem(ctx, "watch-problem", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c, err := e.currentConfig(); err != nil {
				e.recordProblem(ctx, "configuration-problem", err)
			} else if c.Mode != lastMode {
				lastMode = c.Mode
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			if e.Now().Sub(lastHeartbeat) >= 30*time.Second {
				if _, err := api.Call[domain.Agent](ctx, e.Client, "POST", "/v1/heartbeat", domain.Command{}); err != nil {
					if ctx.Err() == nil {
						e.recordProblem(ctx, "heartbeat-problem", err)
					}
				} else {
					lastHeartbeat = e.Now()
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
		}
	}
}
