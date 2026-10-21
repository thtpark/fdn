package identityservice

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type idleTrackingTransport struct {
	http.RoundTripper
	closed atomic.Int64
}

func (t *idleTrackingTransport) CloseIdleConnections() { t.closed.Add(1) }

type idleTrackingRemote struct {
	Remote
	mu      sync.Mutex
	clients []*idleTrackingTransport
}

func (r *idleTrackingRemote) Client(worker config.Worker, certificate tls.Certificate) (*api.Client, error) {
	client, err := r.Remote.Client(worker, certificate)
	if err != nil {
		return nil, err
	}
	transport := &idleTrackingTransport{RoundTripper: client.HTTP.Transport}
	client.HTTP.Transport = transport
	r.mu.Lock()
	r.clients = append(r.clients, transport)
	r.mu.Unlock()
	return client, nil
}

func TestOwnerCertificateRenewalClosesPreviousIdleMTLSConnections(t *testing.T) {
	f := newFixture(t)
	remote := &idleTrackingRemote{Remote: f.remote}
	f.options.Remote = remote
	f.options.RenewInterval = 10 * time.Millisecond
	_, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	eventually(t, "owner certificate renewal did not replace pooled mTLS state", func() bool {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		return len(remote.clients) >= 2 && remote.clients[0].closed.Load() > 0
	})
	stop()
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for _, client := range remote.clients {
		if client.closed.Load() == 0 {
			t.Fatal("owner certificate rotation or shutdown left a cached TLS credential pooled")
		}
	}
}

func TestDeviceCertificateRenewsBeforeExpiryAndReconnectsSameEphemeralOwner(t *testing.T) {
	f := newFixture(t)
	started := time.Now()
	f.options.Now = func() time.Time { return f.now.Add(time.Since(started)) }
	f.options.DeviceRenewBefore = time.Second
	f.remote.nowFn = f.options.Now
	f.remote.deviceTTL = 2 * time.Second
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	first := <-f.remote.streams
	firstProof := <-first.sent
	var old domain.IdentityCertificate
	if err := config.Read(filepath.Join(f.config.StateDir, certificateFile), &old); err != nil {
		t.Fatal(err)
	}
	var renewed *fakeOwner
	select {
	case renewed = <-f.remote.streams:
	case <-time.After(4 * time.Second):
		t.Fatal("device certificate was not renewed before the owner channel's device expiry")
	}
	newProof := <-renewed.sent
	eventually(t, "renewed device did not restore owner readiness", func() bool { return service.Status().Ready })
	stop()
	if firstProof.OwnerKey != newProof.OwnerKey || firstProof.Nonce == newProof.Nonce || f.crypto.owners.Load() != 1 || f.crypto.generated.Load() != 1 {
		t.Fatal("device renewal replaced the device/ephemeral owner key or replayed the prior owner nonce")
	}
	var current domain.IdentityCertificate
	if err := config.Read(filepath.Join(f.config.StateDir, certificateFile), &current); err != nil || old.Certificate == current.Certificate {
		t.Fatal("renewed device certificate was not persisted", err)
	}
	f.remote.mu.Lock()
	defer f.remote.mu.Unlock()
	if len(f.remote.activations) != 2 || f.remote.activations[0].OperationID == f.remote.activations[1].OperationID ||
		f.remote.activations[0].Nonce == f.remote.activations[1].Nonce {
		t.Fatal("device renewal did not use a fresh activation command and server challenge")
	}
}

func TestExpiredOwnerCertificateDeniesRatherThanFallingBack(t *testing.T) {
	f := newFixture(t)
	service, err := New(f.config, f.options)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	service.lease = &ownerLease{context: context.Background(), expires: f.now.Add(-time.Second), client: &api.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}}}
	service.status.Ready = true
	service.ownerExpiry = service.lease.expires
	request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	request = request.WithContext(context.WithValue(request.Context(), peerContextKey{}, Peer{UID: f.config.WorkerUID, GID: f.config.WorkerGID}))
	response := httptest.NewRecorder()
	service.handler().ServeHTTP(response, request)
	var envelope api.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || response.Code != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("expired owner authority did not stop worker execution", response.Code, err)
	}
	if called || service.Status().Ready {
		t.Fatal("expired owner certificate borrowed another transport or remained ready")
	}
	service.lease = nil
	service.status.Problem = domain.Unavailable
	response = httptest.NewRecorder()
	service.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.Status().Problem != domain.Denied {
		t.Fatal("a disconnected owner forgot its certificate expiry and let offline work continue indefinitely")
	}
}
