package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

type blockedObservationSource struct {
	started chan struct{}
	release chan struct{}
	first   sync.Once
}

func (s *blockedObservationSource) Identity(context.Context) (domain.AgentIdentity, error) {
	return domain.AgentIdentity{AgentID: "agent_test", InstanceID: "instance_test", DeviceID: "device_test", Generation: 1}, nil
}

func (s *blockedObservationSource) Snapshot(_ context.Context, query observe.Query) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"id": query.ID, "kind": query.Kind})
}

func (s *blockedObservationSource) Updates(ctx context.Context, _ string, _ domain.UpdateFilter, wait bool) (domain.Page[domain.Update], error) {
	if wait {
		<-ctx.Done()
		return domain.Page[domain.Update]{}, ctx.Err()
	}
	first := false
	s.first.Do(func() {
		first = true
		close(s.started)
	})
	if first {
		select {
		case <-s.release:
		case <-ctx.Done():
			return domain.Page[domain.Update]{}, ctx.Err()
		}
	}
	return domain.Page[domain.Update]{Items: []domain.Update{}, NextCursor: "private"}, nil
}

func (s *blockedObservationSource) Events(ctx context.Context, _ string, _ domain.UpdateFilter, wait bool) (domain.Page[domain.Event], error) {
	if wait {
		<-ctx.Done()
		return domain.Page[domain.Event]{}, ctx.Err()
	}
	return domain.Page[domain.Event]{Items: []domain.Event{}, NextCursor: "public"}, nil
}

func TestMobileCancelledSubscriptionPreservesOwnedObserver(t *testing.T) {
	source := &blockedObservationSource{started: make(chan struct{}), release: make(chan struct{})}
	observer, err := newObservation(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.close)
	ready := nextObservationFrame(t, observer)
	if ready.Type != "ready" || ready.Stream == "" {
		t.Fatal("observer did not become ready", ready)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- observer.subscribe(ctx, []observe.Query{{Kind: "task", ID: "task_old"}})
	}()
	select {
	case <-source.started:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not reach its watermark")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled subscription lost its cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription cancellation did not return")
	}
	observer.mu.Lock()
	pending := len(observer.pending)
	observer.mu.Unlock()
	if pending != 0 {
		t.Fatal("cancelled subscription retained its reply waiter", pending)
	}
	close(source.release)
	replacement, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	if err := observer.subscribe(replacement, []observe.Query{{Kind: "task", ID: "task_current"}}); err != nil {
		t.Fatal("cancelled interest replacement terminated the shared observer", err)
	}
	for {
		frame := nextObservationFrame(t, observer)
		if frame.Error != nil || frame.Stream != ready.Stream {
			t.Fatal("interest replacement lost the owned observer binding", frame)
		}
		if frame.Type == "snapshot" && frame.Query != nil && frame.Query.ID == "task_current" {
			break
		}
	}
}

func TestMobileCancelledSubscriptionDoesNotDispatch(t *testing.T) {
	source := &blockedObservationSource{started: make(chan struct{}), release: make(chan struct{})}
	observer, err := newObservation(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := observer.subscribe(ctx, []observe.Query{{Kind: "task", ID: "task_cancelled"}}); !errors.Is(err, context.Canceled) {
		t.Fatal("already cancelled subscription was dispatched", err)
	}
	if err := observer.context.Err(); err != nil {
		t.Fatal("already cancelled interest stopped the shared observer", err)
	}
	select {
	case <-source.started:
		t.Fatal("already cancelled interest changed the observer")
	default:
	}
}

func TestMobileObserverCloseCancelsBlockedSubscription(t *testing.T) {
	source := &blockedObservationSource{started: make(chan struct{}), release: make(chan struct{})}
	observer, err := newObservation(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- observer.subscribe(t.Context(), []observe.Query{{Kind: "task", ID: "task_blocked"}})
	}()
	select {
	case <-source.started:
	case <-time.After(2 * time.Second):
		observer.close()
		t.Fatal("subscription did not reach its watermark")
	}
	closed := make(chan struct{})
	go func() {
		observer.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground observer did not close its blocked source")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed observer acknowledged a blocked subscription")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed observer retained the subscription caller")
	}
}

func nextObservationFrame(t *testing.T, observer *observation) observe.Frame {
	t.Helper()
	select {
	case data := <-observer.frames:
		var frame observe.Frame
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	case <-observer.done:
		t.Fatal("owned observer ended", observer.err)
	case <-time.After(2 * time.Second):
		t.Fatal("observation frame timed out")
	}
	return observe.Frame{}
}
