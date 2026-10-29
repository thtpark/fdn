package mobile

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

type observation struct {
	context context.Context
	cancel  context.CancelFunc
	input   *io.PipeWriter
	done    chan struct{}
	ready   chan struct{}
	err     error
	frames  chan []byte
	mu      sync.Mutex
	send    sync.Mutex
	pending map[string]chan observe.Frame
	reading atomic.Bool
}

func newObservation(parent context.Context, source observe.Source) (*observation, error) {
	ctx, cancel := context.WithCancel(parent)
	reader, input := io.Pipe()
	o := &observation{context: ctx, cancel: cancel, input: input, done: make(chan struct{}),
		ready: make(chan struct{}), frames: make(chan []byte, 32), pending: make(map[string]chan observe.Frame)}
	go func() {
		o.err = observe.Run(ctx, reader, o, source)
		cancel()
		reader.Close()
		input.Close()
		close(o.done)
	}()
	select {
	case <-o.ready:
		return o, nil
	case <-o.done:
		if o.err != nil {
			return nil, o.err
		}
		return nil, domain.Fail(domain.Unavailable, "embedded observer stopped before becoming ready")
	case <-ctx.Done():
		o.close()
		return nil, ctx.Err()
	}
}

func (o *observation) Write(data []byte) (int, error) {
	if len(data) > observe.MaxFrameBytes {
		return 0, domain.Fail(domain.Invalid, "embedded observation frame exceeds its budget")
	}
	var frame observe.Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	if frame.Type == "result" && frame.ID != "" {
		o.mu.Lock()
		waiter := o.pending[frame.ID]
		o.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- frame:
			case <-o.context.Done():
				return 0, o.context.Err()
			}
		}
		return len(data), nil
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case o.frames <- append([]byte(nil), data...):
		if frame.Type == "ready" {
			close(o.ready)
		}
		return len(data), nil
	case <-o.context.Done():
		return 0, o.context.Err()
	case <-timer.C:
		return 0, domain.Fail(domain.Unavailable, "embedded observation consumer exceeded its bounded frame queue")
	}
}

func (o *observation) subscribe(ctx context.Context, queries []observe.Query) error {
	id := domain.NewID("mobile")
	waiter := make(chan observe.Frame, 1)
	o.mu.Lock()
	o.pending[id] = waiter
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.pending, id)
		o.mu.Unlock()
	}()
	data, err := json.Marshal(observe.Request{ID: id, Op: "subscribe", Queries: queries})
	if err != nil {
		return err
	}
	o.send.Lock()
	_, err = o.input.Write(append(data, '\n'))
	o.send.Unlock()
	if err != nil {
		return err
	}
	select {
	case frame := <-waiter:
		if frame.Error != nil {
			return frame.Error
		}
		return nil
	case <-ctx.Done():
		o.cancel()
		o.input.CloseWithError(ctx.Err())
		return ctx.Err()
	case <-o.done:
		if o.err != nil {
			return o.err
		}
		return domain.Fail(domain.Unavailable, "embedded observation ended before acknowledging subscriptions")
	}
}

func (o *observation) close() {
	o.cancel()
	o.input.Close()
	<-o.done
}

func (r *Runtime) observation() (*observation, error) {
	f, err := r.active()
	if err != nil {
		return nil, err
	}
	f.observerMu.Lock()
	defer f.observerMu.Unlock()
	if f.observer == nil {
		f.observer, err = newObservation(f.context, f.cli)
	}
	return f.observer, err
}

func (r *Runtime) subscribe(ctx context.Context, queries []observe.Query) error {
	if len(queries) > observe.MaxQueries {
		return domain.Fail(domain.Invalid, "mobile observation supports at most 32 active interests")
	}
	f, err := r.active()
	if err != nil {
		return err
	}
	for _, query := range queries {
		if err := f.cli.ValidateQuery(query); err != nil {
			return err
		}
	}
	o, err := r.observation()
	if err != nil {
		return err
	}
	return o.subscribe(ctx, queries)
}

// Next blocks on the existing observer, not a UI polling interval.
func (r *Runtime) Next() []byte {
	o, err := r.observation()
	if err != nil {
		return observationError(err)
	}
	if !o.reading.CompareAndSwap(false, true) {
		return observationError(domain.Fail(domain.Conflict, "embedded observation has one owned reader"))
	}
	defer o.reading.Store(false)
	select {
	case frame := <-o.frames:
		return frame
	case <-o.done:
		if o.err != nil {
			return observationError(o.err)
		}
		return observationError(domain.Fail(domain.Unavailable, "foreground observation closed"))
	}
}

func observationError(err error) []byte {
	data, marshalErr := json.Marshal(observe.Frame{Protocol: observe.Protocol, Type: "connection", Error: domain.AsFault(err)})
	if marshalErr != nil {
		return []byte(`{"protocol":1,"type":"connection","error":{"code":"unavailable","message":"observation encoding failed","retryable":false}}`)
	}
	return data
}
