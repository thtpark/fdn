package work

import (
	"context"
	"errors"
	"sync"

	"github.com/thtpark/fdn/internal/domain"
)

type sessionAttempt struct {
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	unexpected bool
}

type sessionRuntime struct {
	mu      sync.Mutex
	parent  context.Context
	runner  SessionRunner
	current *sessionAttempt
	failure chan error
}

func newSessionRuntime(ctx context.Context, runner SessionRunner) *sessionRuntime {
	r := &sessionRuntime{parent: ctx, runner: runner, failure: make(chan error, 1)}
	r.start()
	return r
}

func (r *sessionRuntime) start() {
	ctx, cancel := context.WithCancel(r.parent)
	attempt := &sessionAttempt{cancel: cancel, done: make(chan struct{})}
	r.current = attempt
	go func() {
		attempt.err = r.runner.Run(ctx)
		attempt.unexpected = ctx.Err() == nil
		close(attempt.done)
		if attempt.unexpected {
			select {
			case r.failure <- attempt.err:
			case <-r.parent.Done():
			}
		}
	}()
}

func (r *sessionRuntime) pause(ctx context.Context) error {
	r.mu.Lock()
	attempt := r.current
	attempt.cancel()
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-attempt.done:
		if attempt.unexpected {
			return domain.Fail(domain.Prerequisite, "session supervisor stopped unexpectedly before reset could join it")
		}
		if attempt.err != nil && !errors.Is(attempt.err, context.Canceled) {
			return attempt.err
		}
		return nil
	}
}

func (r *sessionRuntime) resume() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parent.Err() != nil {
		return r.parent.Err()
	}
	select {
	case <-r.current.done:
		if r.current.unexpected {
			return domain.Fail(domain.Prerequisite, "session supervisor stopped unexpectedly; runtime restart was not proven safe")
		}
		r.start()
		return nil
	default:
		return domain.Fail(domain.Prerequisite, "previous session supervisor has not joined; no second supervisor was started")
	}
}

func (r *sessionRuntime) stop() error {
	r.mu.Lock()
	attempt := r.current
	attempt.cancel()
	r.mu.Unlock()
	<-attempt.done
	if errors.Is(attempt.err, context.Canceled) {
		return nil
	}
	return attempt.err
}
