package identityservice

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func TestOwnerRenewalDoesNotPublishAnUnvalidatedNativeConnection(t *testing.T) {
	f := newFixture(t)
	f.now = time.Now().UTC()
	f.remote.now = f.now
	f.options.RenewInterval = 100 * time.Millisecond
	f.options.RetryInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	var clients atomic.Int64
	f.remote.client = func(c config.Worker, _ tls.Certificate) (*api.Client, error) {
		if clients.Add(1) != 1 {
			return api.NewMessageClient(c.URL, func(context.Context) (net.Conn, error) {
				return nil, domain.Fail(domain.Unavailable, "replacement connection was not established")
			}), nil
		}
		client := ownerWatchClient(t, ctx, func(call context.Context, request message.Request) ([]byte, error) {
			close(entered)
			<-call.Done()
			return nil, call.Err()
		})
		client.BaseURL = c.URL
		return client, nil
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
		_, err := client.Message(ctx, message.Request{
			Operation: "query.updates", Query: map[string]string{"wait": "true"}})
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original watch was not admitted")
	}
	select {
	case err := <-result:
		if err == nil || service.Status().Ready || clients.Load() < 2 {
			t.Fatal("unvalidated replacement kept the authority watch healthy", err)
		}
	case <-ctx.Done():
		t.Fatal("failed replacement did not close the old authority boundary")
	}
}

func ownerWatchClient(t *testing.T, ctx context.Context, handler message.Handler) *api.Client {
	t.Helper()
	var children sync.WaitGroup
	client := api.NewMessageClient("https://localhost", func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		children.Add(1)
		go func() {
			defer children.Done()
			defer left.Close()
			session, err := message.NewSession(ctx, left, handler)
			if err != nil {
				return
			}
			defer session.Close()
			<-session.Done()
		}()
		return right, nil
	})
	t.Cleanup(func() {
		client.Close()
		children.Wait()
	})
	return client
}

func TestOwnerRenewalReissuesOnlyExactBlockingWatchReads(t *testing.T) {
	for _, test := range []struct {
		name, operation, wait string
		reissue               bool
	}{
		{"updates", "query.updates", "true", true},
		{"events", "query.events", "true", true},
		{"nonblocking-updates", "query.updates", "false", false},
		{"ordinary-read", "query.me", "", false},
		{"mutation", "command.messages", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			authority, revoke := context.WithCancel(ctx)
			defer revoke()
			entered, release := make(chan struct{}), make(chan struct{})
			request := message.Request{Operation: test.operation, Query: map[string]string{
				"since": "exact-original-cursor", "wait": test.wait, "limit": "7"}}
			if test.operation == "command.messages" {
				request.Query = nil
				request.Payload = []byte("exact original mutation payload")
			}
			var oldCalls, newCalls atomic.Int64
			old := ownerWatchClient(t, ctx, func(call context.Context, received message.Request) ([]byte, error) {
				if !reflect.DeepEqual(received, request) {
					t.Error("original request changed")
				}
				if oldCalls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
					return []byte("original-server-reply"), nil
				case <-call.Done():
					return nil, call.Err()
				}
			})
			current := ownerWatchClient(t, ctx, func(_ context.Context, received message.Request) ([]byte, error) {
				newCalls.Add(1)
				if !reflect.DeepEqual(received, request) {
					t.Error("handoff changed the operation, cursor, query or payload")
				}
				return []byte("renewed-server-reply"), nil
			})
			var closed sync.WaitGroup
			oldLease := newOwnerLease(authority, old, 1, time.Now().Add(time.Minute), time.Minute, &closed)
			newLease := newOwnerLease(authority, current, 1, time.Now().Add(time.Minute), time.Minute, &closed)
			service := &Service{options: Options{}.defaults(), lease: oldLease}
			type answer struct {
				data []byte
				err  error
			}
			result := make(chan answer, 1)
			go func() {
				data, err := service.forwardOwnerMessage(ctx, request)
				result <- answer{data, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original request was not admitted")
			}
			service.mu.Lock()
			service.lease = newLease
			service.mu.Unlock()
			oldLease.retire()
			expected := "renewed-server-reply"
			if !test.reissue {
				select {
				case <-result:
					t.Fatal("renewal interrupted a request outside the watch allowlist")
				case <-time.After(30 * time.Millisecond):
				}
				close(release)
				expected = "original-server-reply"
			}
			select {
			case answer := <-result:
				if answer.err != nil || string(answer.data) != expected {
					t.Fatal("request did not return its actual backend response", answer.err)
				}
			case <-ctx.Done():
				t.Fatal("handoff did not settle")
			}
			wantNew := int64(0)
			if test.reissue {
				wantNew = 1
			}
			if oldCalls.Load() != 1 || newCalls.Load() != wantNew {
				t.Fatal("unexpected replay count", oldCalls.Load(), newCalls.Load())
			}
			revoke()
			closed.Wait()
		})
	}
}

func TestOwnerWatchHandoffRejectsChangedAuthority(t *testing.T) {
	for _, change := range []string{"generation", "owner-lifetime"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			authority, revoke := context.WithCancel(ctx)
			defer revoke()
			other, stopOther := context.WithCancel(ctx)
			defer stopOther()
			entered := make(chan struct{})
			old := ownerWatchClient(t, ctx, func(call context.Context, _ message.Request) ([]byte, error) {
				close(entered)
				<-call.Done()
				return nil, call.Err()
			})
			var newCalls atomic.Int64
			current := ownerWatchClient(t, ctx, func(context.Context, message.Request) ([]byte, error) {
				newCalls.Add(1)
				return nil, nil
			})
			var closed sync.WaitGroup
			oldLease := newOwnerLease(authority, old, 1, time.Now().Add(time.Minute), time.Minute, &closed)
			generation, parent := int64(1), authority
			if change == "generation" {
				generation = 2
			} else {
				parent = other
			}
			newLease := newOwnerLease(parent, current, generation, time.Now().Add(time.Minute), time.Minute, &closed)
			service := &Service{options: Options{}.defaults(), lease: oldLease}
			result := make(chan error, 1)
			go func() {
				_, err := service.forwardOwnerMessage(ctx, message.Request{
					Operation: "query.updates", Query: map[string]string{"wait": "true"}})
				result <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("watch was not admitted")
			}
			service.mu.Lock()
			service.lease = newLease
			service.mu.Unlock()
			oldLease.retire()
			select {
			case err := <-result:
				if err == nil || domain.AsFault(err).Code != domain.Denied || newCalls.Load() != 0 {
					t.Fatal("watch crossed an owner authority boundary", err)
				}
			case <-ctx.Done():
				t.Fatal("changed authority was not rejected")
			}
			revoke()
			stopOther()
			closed.Wait()
		})
	}
}

func TestOwnerWatchDoesNotHideFailureOrCallerCancellation(t *testing.T) {
	for _, fault := range []string{"transport", "denied", "caller-cancelled", "authority-lost"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			authority, revoke := context.WithCancel(ctx)
			defer revoke()
			call, stopCall := context.WithCancel(ctx)
			defer stopCall()
			entered, fail := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			client := ownerWatchClient(t, ctx, func(request context.Context, _ message.Request) ([]byte, error) {
				if calls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-fail:
					code := domain.Unavailable
					if fault == "denied" {
						code = domain.Denied
					}
					return nil, domain.Fail(code, "explicit backend fault")
				case <-request.Done():
					return nil, request.Err()
				}
			})
			var closed sync.WaitGroup
			lease := newOwnerLease(authority, client, 1, time.Now().Add(time.Minute), time.Minute, &closed)
			service := &Service{options: Options{}.defaults(), lease: lease}
			result := make(chan error, 1)
			go func() {
				_, err := service.forwardOwnerMessage(call, message.Request{
					Operation: "query.updates", Query: map[string]string{"wait": "true"}})
				result <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("watch was not admitted")
			}
			switch fault {
			case "caller-cancelled":
				stopCall()
			case "authority-lost":
				revoke()
			default:
				close(fail)
			}
			select {
			case err := <-result:
				if err == nil || calls.Load() != 1 {
					t.Fatal("unrelated failure was hidden or replayed", err, calls.Load())
				}
				if fault == "caller-cancelled" && !errors.Is(call.Err(), context.Canceled) {
					t.Fatal("original caller cancellation was not preserved")
				}
			case <-ctx.Done():
				t.Fatal("failure did not end the logical watch")
			}
			revoke()
			closed.Wait()
		})
	}
}
