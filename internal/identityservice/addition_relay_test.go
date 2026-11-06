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

func TestAgentAdditionRelayPinsOwnerAcrossRenewalAndCleansUpDisconnectedProxy(t *testing.T) {
	f := newFixture(t)
	f.now = time.Now().UTC()
	f.remote.now = f.now
	f.options.RenewInterval = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var clients atomic.Int64
	var connections sync.WaitGroup
	var retired sync.Map
	opened, cleaned := make(chan int64, 4), make(chan int64, 4)
	tickets := make(chan string, 4)
	f.remote.client = func(c config.Worker, _ tls.Certificate) (*api.Client, error) {
		number := clients.Add(1)
		return api.NewMessageClient(c.URL, func(context.Context) (net.Conn, error) {
			left, right := net.Pipe()
			connections.Go(func() {
				defer left.Close()
				defer retired.Store(number, true)
				var mu sync.Mutex
				ticket := ""
				session, err := message.NewSession(ctx, left, func(_ context.Context, request message.Request) ([]byte, error) {
					if request.Operation == "query.me" {
						return message.MarshalPayload(domain.Agent{ID: f.profile.Identity.AgentID, Active: true})
					}
					mu.Lock()
					defer mu.Unlock()
					if request.Operation == api.AgentAdditionRelayOperation {
						var input domain.AgentAdditionRelayOpen
						if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
							return nil, err
						}
						ticket = input.Ticket
						opened <- number
						tickets <- ticket
						return message.MarshalPayload(ticket)
					}
					if ticket == "" || request.Target != ticket {
						return nil, domain.Fail(domain.Denied, "ticket belongs to another owner connection")
					}
					switch request.Operation {
					case api.AgentAdditionRelayPrefix + ".read":
						return message.MarshalPayload(domain.RelayChunk{Data: []byte("owner-bound SSH bytes"), EOF: true})
					case api.AgentAdditionRelayPrefix + ".write":
						var chunk domain.RelayChunk
						if err := message.UnmarshalPayload(request.Payload, &chunk); err != nil {
							return nil, err
						}
						return message.MarshalPayload(domain.RelayWritten{Written: len(chunk.Data)})
					case api.AgentAdditionRelayPrefix + ".close":
						cleaned <- number
						return message.MarshalPayload(true)
					default:
						return nil, domain.Fail(domain.Denied, "unexpected relay operation")
					}
				})
				if err != nil {
					return
				}
				defer session.Close()
				<-session.Done()
			})
			return right, nil
		}), nil
	}
	_, stop := startService(t, f, rootlessBoundary{})
	defer func() {
		stop()
		cancel()
		connections.Wait()
	}()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream, err := api.DialAgentAdditionRelay(ctx, client, "add_fixture", "192.0.2.10", 2222)
	if err != nil {
		t.Fatal(err)
	}
	var original int64
	select {
	case original = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("local identity proxy did not open the native onboarding relay")
	}
	other, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Message(ctx, message.Request{
		Operation: api.AgentAdditionRelayPrefix + ".read", Target: <-tickets,
	}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another local peer borrowed an onboarding ticket on the shared owner transport", err)
	}
	eventually(t, "owner did not renew during the onboarding stream", func() bool { return clients.Load() > original })
	if _, closed := retired.Load(original); closed {
		t.Fatal("owner renewal closed an admitted onboarding transport")
	}
	if _, err := stream.Write([]byte("encrypted SSH")); err != nil {
		t.Fatal("renewal moved a ticket to another owner transport", err)
	}
	got := make([]byte, 64)
	if n, err := stream.Read(got); err != nil || string(got[:n]) != "owner-bound SSH bytes" {
		t.Fatal("renewal changed the pinned relay read", err)
	}
	if _, err := api.DialAgentAdditionRelay(ctx, client, "add_other", "192.0.2.11", 22); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("one raw proxy admitted multiple onboarding streams", err)
	}
	client.Close()
	select {
	case number := <-cleaned:
		if number != original {
			t.Fatal("proxy cleanup targeted the renewed transport instead of its original ticket")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnecting the raw proxy leaked its remote worker connection")
	}
	eventually(t, "closed onboarding proxy retained the retired owner lease", func() bool {
		_, closed := retired.Load(original)
		return closed
	})
}
