package identityservice

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
)

func TestOwnerRenewalMovesNativeWatchesBeforeOldCertificateExpiry(t *testing.T) {
	for _, operation := range []string{"query.updates", "query.events"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			f.remote.nowFn, f.options.Now = time.Now, time.Now
			f.remote.ownerTTL = 3 * time.Second
			f.options.RenewInterval = 200 * time.Millisecond
			trust := filepath.Join(f.root, "tls")
			if err := f.remote.ca.Write(trust); err != nil {
				t.Fatal(err)
			}
			serverTLS, err := pki.ServerMutual(
				filepath.Join(trust, "server.pem"), filepath.Join(trust, "server-key.pem"), filepath.Join(trust, "ca.pem"))
			if err != nil {
				t.Fatal(err)
			}
			serverTLS.NextProtos = []string{api.MessageALPN}
			baseTLS, err := pki.Client(filepath.Join(trust, "ca.pem"), "localhost")
			if err != nil {
				t.Fatal(err)
			}
			baseTLS.NextProtos = []string{api.MessageALPN}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var clients atomic.Int64
			var children sync.WaitGroup
			var callsMu sync.Mutex
			var calls []message.Request
			release := make(chan struct{})
			firstExpiry := make(chan time.Time, 1)
			f.remote.client = func(c config.Worker, certificate tls.Certificate) (*api.Client, error) {
				number := clients.Add(1)
				if number == 1 {
					leaf, err := x509.ParseCertificate(certificate.Certificate[0])
					if err != nil {
						return nil, err
					}
					firstExpiry <- leaf.NotAfter
				}
				clientTLS := baseTLS.Clone()
				clientTLS.Certificates = []tls.Certificate{certificate}
				return api.NewMessageClient(c.URL, func(call context.Context) (net.Conn, error) {
					left, right := net.Pipe()
					children.Add(1)
					go func() {
						defer children.Done()
						defer left.Close()
						secured := tls.Server(left, serverTLS.Clone())
						if err := secured.HandshakeContext(ctx); err != nil {
							return
						}
						expiry := secured.ConnectionState().PeerCertificates[0].NotAfter
						lifetime, stop := context.WithDeadline(ctx, expiry)
						defer stop()
						session, err := message.NewSession(lifetime, secured,
							func(request context.Context, value message.Request) ([]byte, error) {
								if value.Operation == operation {
									callsMu.Lock()
									calls = append(calls, value)
									callsMu.Unlock()
									select {
									case <-release:
										return message.MarshalPayload(domain.Page[domain.Update]{
											Items: []domain.Update{}, NextCursor: "server_cursor_after_renewal"})
									case <-request.Done():
										return nil, request.Err()
									}
								}
								return message.MarshalPayload(domain.Agent{
									ID: f.profile.Identity.AgentID, Active: true, Mode: "ai"})
							})
						if err != nil {
							return
						}
						defer session.Close()
						<-session.Done()
					}()
					secured := tls.Client(right, clientTLS)
					if err := secured.HandshakeContext(call); err != nil {
						right.Close()
						return nil, err
					}
					return secured, nil
				}), nil
			}
			service, stop := startService(t, f, rootlessBoundary{})
			defer func() {
				stop()
				cancel()
				children.Wait()
			}()
			client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			original := message.Request{Operation: operation, Query: map[string]string{
				"since": "original_cursor", "wait": "true", "limit": "7"}}
			type answer struct {
				payload []byte
				err     error
			}
			result := make(chan answer, 1)
			go func() {
				payload, err := client.Message(ctx, original)
				result <- answer{payload, err}
			}()
			eventually(t, "native watch did not enter its original transport", func() bool {
				callsMu.Lock()
				defer callsMu.Unlock()
				return len(calls) != 0
			})
			eventually(t, "identity service did not publish renewed authority", func() bool {
				return clients.Load() >= 3 && service.Status().Ready
			})
			expiry := <-firstExpiry
			timer := time.NewTimer(max(time.Until(expiry)+150*time.Millisecond, time.Millisecond))
			defer timer.Stop()
			select {
			case result := <-result:
				t.Fatalf("routine renewal ended the logical watch: %v", result.err)
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("test deadline preceded certificate expiry")
			}
			close(release)
			select {
			case result := <-result:
				if result.err != nil {
					t.Fatal("logical watch failed after old certificate expired", result.err)
				}
				var page domain.Page[domain.Update]
				if err := message.UnmarshalPayload(result.payload, &page); err != nil || page.NextCursor != "server_cursor_after_renewal" {
					t.Fatal("watch did not return the real server reply", err)
				}
			case <-ctx.Done():
				t.Fatal("renewed watch did not return its response")
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			if len(calls) < 2 {
				t.Fatal("watch never moved off its original transport")
			}
			for _, request := range calls {
				if !reflect.DeepEqual(request, original) {
					t.Fatal("renewal changed the exact watch request or cursor")
				}
			}
		})
	}
}
