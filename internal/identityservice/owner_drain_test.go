package identityservice

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func TestOwnerRenewalDrainsInflightNativeQueriesWithoutDisconnectingTheWatch(t *testing.T) {
	f := newFixture(t)
	f.now = time.Now().UTC()
	f.remote.now = f.now
	f.options.RenewInterval = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	entered, release, oldClosed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var clients atomic.Int64
	f.remote.client = func(c config.Worker, _ tls.Certificate) (*api.Client, error) {
		number := clients.Add(1)
		return api.NewMessageClient(c.URL, func(context.Context) (net.Conn, error) {
			left, right := net.Pipe()
			go func() {
				if number == 1 {
					defer close(oldClosed)
				}

				session, err := message.NewSession(ctx, left, func(call context.Context, request message.Request) ([]byte, error) {
					if request.Operation == "query.updates" {
						close(entered)
						select {
						case <-release:
						case <-call.Done():
							return nil, call.Err()
						}
						return message.MarshalPayload(domain.Page[domain.Update]{Items: []domain.Update{}, NextCursor: "renewal_cursor"})
					}
					return message.MarshalPayload(domain.Agent{ID: f.profile.Identity.AgentID, Active: true})
				})
				if err != nil {
					return
				}
				defer session.Close()
				<-session.Done()
			}()
			return right, nil
		}), nil
	}
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result := make(chan error, 1)
	go func() {
		page, err := api.Call[domain.Page[domain.Update]](ctx, client, "GET", "/v1/updates?wait=true", nil)
		if err == nil && page.NextCursor != "renewal_cursor" {
			err = domain.Fail(domain.Internal, "renewal changed the exact watch result")
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native authority watch did not start")
	}
	eventually(t, "owner did not publish its renewed certificate", func() bool { return clients.Load() >= 2 && service.Status().Ready })
	select {
	case err := <-result:
		t.Fatal("routine renewal interrupted an admitted authority watch", err)
	case <-oldClosed:
		t.Fatal("routine renewal closed a transport with an in-flight call")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("original watch did not finish normally across renewal", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("draining watch did not complete")
	}
	select {
	case <-oldClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("retired transport was not closed after its last call")
	}
	me, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
	if err != nil || me.ID != f.profile.Identity.AgentID {
		t.Fatal("new calls did not use the renewed current owner transport", me, err)
	}
}
