package observe

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type testSource struct {
	mu      sync.Mutex
	reads   map[string]int
	private chan domain.Update
	public  chan domain.Event
}

func (s *testSource) Identity(context.Context) (domain.AgentIdentity, error) {
	return domain.AgentIdentity{AgentID: "agent_test", InstanceID: "instance_test", Generation: 1}, nil
}
func (s *testSource) Snapshot(_ context.Context, q Query) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[q.ID]++
	return json.Marshal(map[string]any{"id": q.ID, "revision": s.reads[q.ID]})
}
func (s *testSource) Updates(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Update], error) {
	if !wait {
		return domain.Page[domain.Update]{Items: []domain.Update{}, NextCursor: "private"}, nil
	}
	for {
		select {
		case <-ctx.Done():
			return domain.Page[domain.Update]{}, ctx.Err()
		case update := <-s.private:
			if filter.Matches(update, "agent_test") {
				return domain.Page[domain.Update]{Items: []domain.Update{update}, NextCursor: "private"}, nil
			}
		}
	}
}
func (s *testSource) Events(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Event], error) {
	if !wait {
		return domain.Page[domain.Event]{Items: []domain.Event{}, NextCursor: "public"}, nil
	}
	for {
		select {
		case <-ctx.Done():
			return domain.Page[domain.Event]{}, ctx.Err()
		case event := <-s.public:
			if filter.Matches(domain.Update{Topic: event.Topic, SourceID: event.SourceID, TaskID: event.TaskID}, "agent_test") {
				return domain.Page[domain.Event]{Items: []domain.Event{event}, NextCursor: "public"}, nil
			}
		}
	}
}

func TestObserverInitialSnapshotsEntityIsolationAndNoBusinessPolling(t *testing.T) {
	source := &testSource{reads: map[string]int{}, private: make(chan domain.Update, 8), public: make(chan domain.Event, 8)}
	input, send := io.Pipe()
	read, output := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, input, output, source); output.Close() }()
	frames := make(chan Frame, 16)
	go func() {
		decoder := json.NewDecoder(read)
		for {
			var frame Frame
			if err := decoder.Decode(&frame); err != nil {
				return
			}
			frames <- frame
		}
	}()
	next := func() Frame {
		t.Helper()
		select {
		case frame := <-frames:
			return frame
		case <-time.After(2 * time.Second):
			t.Fatal("observer frame timed out")
			return Frame{}
		}
	}
	if frame := next(); frame.Type != "ready" || frame.Protocol != 1 {
		t.Fatal(frame)
	}
	encoder := json.NewEncoder(send)
	if err := encoder.Encode(Request{ID: "sub", Op: "subscribe", Queries: []Query{{Kind: "task", ID: "task_a"}, {Kind: "task", ID: "task_b"}}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		frame := next()
		if frame.Type == "snapshot" {
			seen[frame.Query.ID] = true
		}
	}
	time.Sleep(400 * time.Millisecond)
	source.mu.Lock()
	a, b := source.reads["task_a"], source.reads["task_b"]
	source.mu.Unlock()
	if a != 1 || b != 1 {
		t.Fatal("idle subscriptions polled business data", a, b)
	}
	source.public <- domain.Event{Topic: "task", TaskID: "task_a", SourceID: "task_a"}
	for {
		frame := next()
		if frame.Type == "snapshot" {
			if frame.Query.ID != "task_a" {
				t.Fatal("task A update refetched task B", frame)
			}
			break
		}
	}
	source.mu.Lock()
	b = source.reads["task_b"]
	source.mu.Unlock()
	if b != 1 {
		t.Fatal("unrelated entity was re-read", b)
	}
	send.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF did not stop observer")
	}
	read.Close()
}

func TestObserverRejectsMutatingAndUnboundedQueries(t *testing.T) {
	for _, kind := range []string{"work run", "message send", "provider login", "agent reset"} {
		if err := (Query{Kind: kind}).Validate(); err == nil {
			t.Fatal("mutating command entered read-only observer", kind)
		}
	}
	if err := (Query{Kind: "tasks", Limit: 201}).Validate(); err == nil {
		t.Fatal("oversize page accepted")
	}
	if (Query{Kind: "provider"}).Subscribable() {
		t.Fatal("background provider probing was permitted")
	}
}
