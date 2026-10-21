//go:build integration && (darwin || linux)

package integration_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/pki"
)

type identityKeys struct {
	public domain.PublicIdentity
	auth   crypto.Signer
}

func newIdentityKeys(t *testing.T, name string) identityKeys {
	t.Helper()
	public, _, private, err := device.Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := device.ParseSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	return identityKeys{public: public, auth: auth}
}

func newOwnerKey(t *testing.T) crypto.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private
}

type identityListener struct {
	server *http.Server
	done   chan error
}

// A fixture binds exactly one identity to the test process's actual kernel UID.
// Logical UIDs for other actors seed registry policy only, never OS isolation.
type identityHarness struct {
	root       string
	store      *foundation.Store
	bundle     pki.Bundle
	service    *api.Server
	listeners  []identityListener
	apiSocket  string
	joinSocket string
	founder    domain.AgentIdentity
	identity   domain.AgentIdentity
	keys       identityKeys
	join       *api.Client
	peerUID    atomic.Int64
}

func newIdentityHarness(t *testing.T, founder bool) *identityHarness {
	t.Helper()
	if os.Geteuid() <= 0 {
		t.Skip("requires an unprivileged native account; does not provision host accounts")
	}
	root, err := os.MkdirTemp(".", ".identity-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	h := &identityHarness{root: root, apiSocket: filepath.Join(root, "api.sock"), joinSocket: filepath.Join(root, "join.sock")}
	t.Cleanup(func() {
		h.stop(t)
		if h.store != nil {
			if err := h.store.Close(); err != nil {
				t.Error(err)
			}
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	h.store, err = foundation.Open(ctx, filepath.Join(root, "state", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	h.bundle, err = pki.Generate(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.bundle.Write(filepath.Join(root, "trust")); err != nil {
		t.Fatal(err)
	}
	founderKeys := newIdentityKeys(t, "Founder")
	h.founder, err = h.store.BootstrapIdentity(ctx, founderKeys.public, command().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if founder {
		h.identity, h.keys = h.founder, founderKeys
	} else {
		h.founder = h.seedActivate(t, h.founder, founderKeys, int64(os.Geteuid())+100000)
		h.keys = newIdentityKeys(t, "worker")
		h.identity, err = h.store.RegisterIdentity(ctx, h.founder.AgentID, domain.RegisterIdentityRequest{Command: command(), Identity: h.keys.public})
		if err != nil {
			t.Fatal(err)
		}
	}
	h.identity, err = h.store.BindIdentityAccount(ctx, h.identity.AgentID, h.identity.SSHUser, int64(os.Geteuid()), h.identity.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	h.start(t)
	h.join = h.client(t, h.joinSocket, tls.Certificate{})
	return h
}

func (h *identityHarness) seedActivate(t *testing.T, identity domain.AgentIdentity, keys identityKeys, uid int64) domain.AgentIdentity {
	t.Helper()
	ctx := context.Background()
	identity, err := h.store.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, uid, identity.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := h.store.ChallengeIdentity(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(keys.auth, challenge)
	if err != nil {
		t.Fatal(err)
	}
	identity, err = h.store.ActivateIdentity(ctx, uid, domain.ActivateIdentityRequest{Command: command(), Nonce: challenge.Nonce, Proof: proof})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func (h *identityHarness) start(t *testing.T) {
	t.Helper()
	var err error
	h.service, err = api.New(h.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(h.root, "trust")
	if err := h.service.ConfigureIdentity(api.IdentityOptions{CAFile: filepath.Join(trust, "ca.pem"), CAKeyFile: filepath.Join(trust, "ca-key.pem")}); err != nil {
		t.Fatal(err)
	}
	mutual, err := pki.ServerMutual(filepath.Join(trust, "server.pem"), filepath.Join(trust, "server-key.pem"), filepath.Join(trust, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	joinTLS := mutual.Clone()
	joinTLS.ClientAuth = tls.VerifyClientCertIfGiven
	joinTLS.VerifyConnection = nil
	for _, endpoint := range []struct {
		path    string
		tls     *tls.Config
		handler http.Handler
	}{{h.apiSocket, mutual, h.service.Handler()}, {h.joinSocket, joinTLS, h.service.BootstrapHandler()}} {
		listener, err := ingress.Listen(endpoint.path, -1)
		if err != nil {
			t.Fatal(err)
		}
		handler := endpoint.handler
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if uid, ok := ingress.UID(r.Context()); ok {
				h.peerUID.Store(int64(uid))
			}
			handler.ServeHTTP(w, r)
		}), ConnContext: ingress.ConnContext, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
		done := make(chan error, 1)
		go func() { done <- server.Serve(tls.NewListener(listener, endpoint.tls)) }()
		h.listeners = append(h.listeners, identityListener{server, done})
	}
}

func (h *identityHarness) stop(t *testing.T) {
	t.Helper()
	if h.service != nil {
		h.service.Close()
		h.service = nil
	}
	for _, listener := range h.listeners {
		listener.server.Close()
		if err := <-listener.done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}
	h.listeners = nil
}

func (h *identityHarness) client(t *testing.T, socket string, certificate tls.Certificate) *api.Client {
	t.Helper()
	config, err := pki.Client(filepath.Join(h.root, "trust", "ca.pem"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	config.NextProtos = []string{"http/1.1"}
	if len(certificate.Certificate) != 0 {
		config.Certificates = []tls.Certificate{certificate}
	}
	transport := &http.Transport{TLSClientConfig: config, Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	client := &api.Client{BaseURL: "https://localhost", HTTP: &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	t.Cleanup(client.Close)
	return client
}

func identityTLSCertificate(t *testing.T, certificate []byte, signer crypto.Signer) tls.Certificate {
	t.Helper()
	key, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func (h *identityHarness) issue(t *testing.T, signer crypto.Signer, identity domain.CertificateIdentity, now time.Time, ttl time.Duration) tls.Certificate {
	t.Helper()
	certificate, err := pki.IssueClient(h.bundle.CA, h.bundle.CAKey, signer.Public(), identity, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return identityTLSCertificate(t, certificate, signer)
}

func (h *identityHarness) activate(t *testing.T) tls.Certificate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	challenge, err := api.Call[domain.IdentityChallenge](ctx, h.join, "GET", "/v1/identity/challenge", nil)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(h.keys.auth, challenge)
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.Call[domain.IdentityCertificate](ctx, h.join, "POST", "/v1/identity/activate", domain.ActivateIdentityRequest{Command: command(), Nonce: challenge.Nonce, Proof: proof})
	if err != nil || result.Agent.ID != h.identity.AgentID || result.Identity.State != "active" {
		t.Fatal("key-bound activation failed", result.Identity, err)
	}
	h.identity = result.Identity
	return identityTLSCertificate(t, []byte(result.Certificate), h.keys.auth)
}

type ownerLease struct {
	conn        *websocket.Conn
	certificate domain.OwnerCertificate
	key         crypto.Signer
	client      *api.Client
	updates     chan domain.OwnerCertificate
	errors      chan error
	cancel      context.CancelFunc
}

func (h *identityHarness) ownerChallenge(t *testing.T, client *api.Client) (*websocket.Conn, domain.IdentityChallenge) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: client.HTTP})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	var challenge domain.IdentityChallenge
	if err := wsjson.Read(ctx, conn, &challenge); err != nil || challenge.Nonce == "" || challenge.AgentID != h.identity.AgentID {
		t.Fatal("owner channel did not send a fresh bound challenge", challenge, err)
	}
	return conn, challenge
}

func (h *identityHarness) openOwner(t *testing.T, deviceClient *api.Client, key crypto.Signer, read bool) *ownerLease {
	t.Helper()
	conn, challenge := h.ownerChallenge(t, deviceClient)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := device.SignOwner(key, h.identity, challenge.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, conn, req); err != nil {
		t.Fatal(err)
	}
	var certificate domain.OwnerCertificate
	if err := wsjson.Read(ctx, conn, &certificate); err != nil || certificate.Certificate == "" || certificate.Generation < 1 || certificate.Challenge.Nonce == challenge.Nonce {
		t.Fatal("valid fresh owner admission failed", err)
	}
	pair := identityTLSCertificate(t, []byte(certificate.Certificate), key)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ci, err := pki.ParseIdentity(leaf)
	if err != nil || ci.Purpose != "owner" || ci.AgentID != h.identity.AgentID || ci.OwnerGeneration != certificate.Generation ||
		leaf.NotAfter.After(time.Now().Add(15*time.Minute+time.Second)) {
		t.Fatal("owner certificate was not short-lived and generation/key-bound", ci, err)
	}
	h.identity, err = h.store.Identity(context.Background(), h.identity.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	life, stop := context.WithCancel(context.Background())
	lease := &ownerLease{conn: conn, certificate: certificate, key: key, client: h.client(t, h.apiSocket, pair),
		updates: make(chan domain.OwnerCertificate, 2), errors: make(chan error, 1), cancel: stop}
	t.Cleanup(lease.close)
	if read {
		go func() {
			for {
				var next domain.OwnerCertificate
				if err := wsjson.Read(life, conn, &next); err != nil {
					lease.errors <- err
					return
				}
				select {
				case lease.updates <- next:
				case <-life.Done():
					return
				}
			}
		}()
	}
	return lease
}

func (l *ownerLease) close() {
	l.cancel()
	l.conn.CloseNow()
}

func identityCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if err == nil || domain.AsFault(err).Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func identityMe(t *testing.T, client *api.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actor, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
	if err != nil || actor.ID != id || actor.Banned || !actor.Active {
		t.Fatal("expected the live original identity", actor, err)
	}
}

func identityEventually(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for " + description)
}
