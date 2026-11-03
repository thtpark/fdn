package identityservice

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
)

func TestDrainingOwnerLeaseStillExpiresOrStopsImmediatelyOnAuthorityLoss(t *testing.T) {
	for _, mode := range []string{"release", "expiry", "authority-loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := &idleTrackingTransport{}
			client := &api.Client{HTTP: &http.Client{Transport: transport}}
			lifetime := time.Minute
			if mode == "expiry" {
				lifetime = 50 * time.Millisecond
			}
			var closed sync.WaitGroup
			lease := newOwnerLease(ctx, client, time.Now().Add(lifetime), lifetime, &closed)
			if !lease.acquire() {
				t.Fatal("current owner request was not admitted")
			}
			lease.retire()
			if transport.closed.Load() != 0 || lease.acquire() {
				t.Fatal("retirement interrupted an active call or admitted new calls")
			}
			switch mode {
			case "release":
				lease.release()
			case "authority-loss":
				cancel()
			}
			done := make(chan struct{})
			go func() { closed.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("retired transport survived request completion, expiry or revoked authority")
			}
			if transport.closed.Load() != 1 || lease.context.Err() == nil {
				t.Fatal("expired/revoked transport remained available", transport.closed.Load())
			}
		})
	}
}
