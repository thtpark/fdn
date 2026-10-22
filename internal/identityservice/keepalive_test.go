package identityservice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerKeepaliveIsIndependentOfCertificateRenewalAndJoinsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var count atomic.Int64
	reached := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ownerKeepalive(ctx, time.Millisecond, func(check context.Context) error {
			if _, ok := check.Deadline(); !ok {
				t.Error("heartbeat has no bounded deadline")
			}
			if count.Add(1) == 3 {
				close(reached)
			}
			return nil
		})
	}()
	select {
	case <-reached:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("owner remained idle until certificate renewal")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat worker did not join")
	}
}

func TestOwnerKeepaliveFailureIsObservable(t *testing.T) {
	failure := errors.New("owner ping failed")
	if err := ownerKeepalive(context.Background(), time.Millisecond, func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("heartbeat failure hidden", err)
	}
}
