package display

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type unnotifiedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c unnotifiedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestInventoryPreservesDeadlineBeforeContextNotification(t *testing.T) {
	f := screenFixtureFor(t)
	hung := rfb(t, "")
	f.config.Screens = f.config.Screens[:1]
	f.config.Screens[0].VNCAddress = hung.address
	source, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Deliberately withhold Done/Err notification after the socket deadline.
	ctx := unnotifiedDeadlineContext{Context: source, deadline: time.Now().Add(30 * time.Millisecond)}
	got, err := Inventory(ctx, f.config)
	if ctx.Err() != nil {
		t.Fatal("regression did not exercise an unnotified source deadline")
	}
	if len(got) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("socket deadline was swallowed before context notification", got, err)
	}
	if hung.received.Load() != 0 {
		t.Fatal("deadline probe sent a login or framebuffer request")
	}
}

func TestBackendPreservesCancellationDuringBannerRead(t *testing.T) {
	hung := rfb(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, _, err := dialBackend(ctx, hung.address)
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	waitFor(t, func() bool { return hung.accepted.Load() != 0 })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("source cancellation became ordinary backend unavailability", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source cancellation did not interrupt the backend read")
	}
}

func TestBackendLocalProbeTimeoutIsNotSourceCancellation(t *testing.T) {
	hung := rfb(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := dialBackend(ctx, hung.address)
	if conn != nil {
		conn.Close()
	}
	if ctx.Err() != nil || err == nil || domain.AsFault(err).Code != domain.Prerequisite ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("backend-local timeout was confused with cancellation of its source", err)
	}
}

func TestBackendPreservesAlreadyCanceledSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, _, err := dialBackend(ctx, "127.0.0.1:5900")
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("already canceled source was converted to backend unavailability", err)
	}
}
