package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestScopedUpdatesOnlyWakeMatchingEntitiesAndBindCursors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := readFixtureMessage(t, f, false)
	second := readFixtureMessage(t, f, false)
	filter := domain.UpdateFilter{Sources: []string{first.ID}}
	watermark, err := f.s.ScopedUpdates(ctx, f.worker.ID, "", 50, filter)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan domain.Page[domain.Update], 1)
	errors := make(chan error, 1)
	go func() {
		page, err := f.s.WaitScopedUpdates(waiting, f.worker.ID, watermark.NextCursor, 50, filter)
		result <- page
		errors <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		f.s.updates.mu.Lock()
		count := len(f.s.updates.observers)
		f.s.updates.mu.Unlock()
		if count != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("observer did not subscribe")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := f.s.ReadMessage(ctx, f.worker.ID, second.ID, domain.MessageReadRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
		t.Fatal("unrelated entity woke observer")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := f.s.ReadMessage(ctx, f.worker.ID, first.ID, domain.MessageReadRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	select {
	case page := <-result:
		if err := <-errors; err != nil || len(page.Items) != 1 || page.Items[0].SourceID != first.ID {
			t.Fatal(page, err)
		}
	case <-time.After(time.Second):
		t.Fatal("matching update was not delivered")
	}
	if _, err := f.s.ScopedUpdates(ctx, f.worker.ID, watermark.NextCursor, 50, domain.UpdateFilter{Sources: []string{second.ID}}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("cursor was reused for another scope", err)
	}
	foreign, err := f.s.ScopedUpdates(ctx, f.other.ID, "", 50, domain.UpdateFilter{Topics: []string{"message"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.s.ReadMessage(ctx, f.worker.ID, first.ID, domain.MessageReadRequest{Command: op()})
	foreign, err = f.s.ScopedUpdates(ctx, f.other.ID, foreign.NextCursor, 50, domain.UpdateFilter{Topics: []string{"message"}})
	if err != nil || len(foreign.Items) != 0 {
		t.Fatal("private observer leaked another actor's message", foreign, err)
	}
}

func TestScopedPublicWatermarkDoesNotReplayOldEvents(t *testing.T) {
	f := newFixture(t)
	filter := domain.UpdateFilter{Topics: []string{"project"}}
	page, err := f.s.ScopedEvents(context.Background(), f.worker.ID, "", 50, filter)
	if err != nil || len(page.Items) != 0 || page.NextCursor == "" {
		t.Fatal("initial observer must return a current watermark, not historical events", page, err)
	}
	if _, err := f.s.ScopedEvents(context.Background(), f.worker.ID, page.NextCursor, 50, domain.UpdateFilter{Topics: []string{"task"}}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("shared cursor was not query-bound", err)
	}
}
