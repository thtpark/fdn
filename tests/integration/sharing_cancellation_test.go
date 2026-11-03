//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

func TestSharingOwnedTransportCancellationPreservesLogicalView(t *testing.T) {
	h := newHarness(t)
	engine := newEngine(t, h, &executor{})
	backend, ctx := startSharingManager(t, h, engine, context.Canceled)
	session := openSharing(t, h, h.founder, "tmux", "")
	first, err := remote.Dial(ctx, h.founder, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	readSharingMarker(t, first, "shell")
	backend.mu.Lock()
	producer := backend.peers[len(backend.peers)-1]
	backend.mu.Unlock()
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSharing(t, func() bool { return backend.opens.Load() >= 2 })
	current, err := api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
	if err != nil || current.State != "active" || current.ID != session.ID || current.ExpiresAt != session.ExpiresAt {
		t.Fatal("owned transport cancellation retired or renewed the logical view", current, err)
	}
	second, err := remote.Dial(ctx, h.founder, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	readSharingMarker(t, second, "shell")
}
