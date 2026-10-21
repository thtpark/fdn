package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type tunnelRunner func(context.Context, process.Spec) error

func (r tunnelRunner) Run(ctx context.Context, spec process.Spec) error { return r(ctx, spec) }

type tunnelListener struct {
	conn     net.Conn
	mu       sync.Mutex
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *tunnelListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *tunnelListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *tunnelListener) Addr() net.Addr { return l.conn.LocalAddr() }

func tunnelFixture(t *testing.T) (config.Worker, tls.Certificate, tls.Certificate, *tls.Config) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	bundle, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "private")
	if err := bundle.Write(root); err != nil {
		t.Fatal(err)
	}
	sshKey, pins := filepath.Join(root, "ssh-key"), filepath.Join(root, "known_hosts")
	if err := secure.Create(sshKey, []byte("fixture-local-ssh-key")); err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(pins, []byte("fixture-pinned-host-key")); err != nil {
		t.Fatal(err)
	}
	c := config.Worker{URL: "https://localhost:18443", CAFile: filepath.Join(root, "ca.pem"), ServerName: "localhost",
		SSH: &config.SSH{Host: "fixture.invalid", User: "agent_test", Port: 22, IdentityFile: sshKey, KnownHostsFile: pins}}
	issue := func(purpose string) tls.Certificate {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		identity := domain.CertificateIdentity{InstanceID: "instance_test", AgentID: "agent_test", Generation: 2, Purpose: purpose}
		if purpose == "owner" {
			identity.OwnerGeneration = 3
		}
		cert, err := pki.IssueClient(bundle.CA, bundle.CAKey, public, identity, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		key, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(cert, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	server, err := pki.ServerMutual(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"), c.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	return c, issue("owner"), issue("device"), server
}

func servingTunnel(t *testing.T, serverTLS *tls.Config, handler http.Handler, starts *atomic.Int64, stops chan<- struct{}) process.Runner {
	t.Helper()
	return tunnelRunner(func(ctx context.Context, spec process.Spec) error {
		starts.Add(1)
		defer func() { stops <- struct{}{} }()
		conn, ok := spec.Stdin.(net.Conn)
		if !ok || spec.Stdout != conn {
			return errors.New("tunnel did not use duplex stdin/stdout")
		}
		listener := &tunnelListener{conn: conn, done: make(chan struct{})}
		server := &http.Server{Handler: handler, TLSConfig: serverTLS, ErrorLog: log.New(io.Discard, "", 0), ReadHeaderTimeout: time.Second}
		done := make(chan error, 1)
		go func() { done <- server.Serve(tls.NewListener(listener, serverTLS)) }()
		if err := spec.Started(123); err != nil {
			listener.Close()
			server.Close()
			<-done
			return err
		}
		<-ctx.Done()
		listener.Close()
		server.Close()
		<-done
		return ctx.Err()
	})
}

func awaitTunnelStop(t *testing.T, stops <-chan struct{}) {
	t.Helper()
	select {
	case <-stops:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP connection close did not cancel its SSH process")
	}
}

func TestTunnelClientMutualTLSWithoutBearerOrTCPFallback(t *testing.T) {
	c, owner, _, serverTLS := tunnelFixture(t)
	var starts atomic.Int64
	var requests atomic.Int64
	stops := make(chan struct{}, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.TLS == nil || r.TLS.Version != tls.VersionTLS13 {
			t.Error("request did not use credential-free mutual TLS")
		}
		identity, err := pki.ParseIdentity(r.TLS.PeerCertificates[0])
		if err != nil || identity.Purpose != "owner" || identity.OwnerGeneration != 3 {
			t.Error("wrong client certificate identity", err)
		}
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		json.NewEncoder(w).Encode(Envelope{OK: true, Data: json.RawMessage(`{"connected":true}`)})
	})
	client, err := newTunnelClient(c, owner, "fdn-api", servingTunnel(t, serverTLS, handler, &starts, stops))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Credential != (config.Credential{}) || client.BaseURL != c.URL {
		t.Fatal("constructor loaded bearer credentials or changed origin")
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var result struct {
			Connected bool `json:"connected"`
		}
		err := client.Request(ctx, http.MethodGet, "/healthz", nil, &result)
		cancel()
		if err != nil || !result.Connected {
			t.Fatal("request failed", err)
		}
	}
	if starts.Load() != 1 || requests.Load() != 2 {
		t.Fatal("HTTP request cancellation killed reusable SSH connection", starts.Load(), requests.Load())
	}
	transport := client.HTTP.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "localhost" || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatal("tunnel weakened TLS or inherited a proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("transport accepted a foreign origin")
	}
	client.Close()
	awaitTunnelStop(t, stops)
}

func TestTunnelClientWebsocketUsesSamePinnedStream(t *testing.T) {
	c, _, device, serverTLS := tunnelFixture(t)
	var starts atomic.Int64
	stops := make(chan struct{}, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("websocket received bearer credentials")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.Write(ctx, kind, data); err != nil {
			t.Error(err)
		}
		conn.Close(websocket.StatusNormalClosure, "done")
	})
	client, err := newTunnelClient(c, device, "fdn-join", servingTunnel(t, serverTLS, handler, &starts, stops))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(c.URL, "https:", "wss:", 1)+"/v1/identity/owner", &websocket.DialOptions{HTTPClient: client.HTTP})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("owner renewal")); err != nil {
		t.Fatal(err)
	}
	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText || string(data) != "owner renewal" {
		t.Fatal("websocket tunnel did not preserve data", err)
	}
	conn.Close(websocket.StatusNormalClosure, "done")
	client.Close()
	awaitTunnelStop(t, stops)
	if starts.Load() != 1 {
		t.Fatal("websocket used a second transport")
	}
}

func TestTunnelClientRejectsUnpinnedOrWrongPurposeConnections(t *testing.T) {
	c, owner, device, _ := tunnelFixture(t)
	for name, test := range map[string]struct {
		config      config.Worker
		certificate tls.Certificate
		mode        string
	}{
		"no-ssh":         {config: config.Worker{URL: c.URL, CAFile: c.CAFile}, certificate: owner, mode: "fdn-api"},
		"no-owner":       {config: c, mode: "fdn-api"},
		"device-for-api": {config: c, certificate: device, mode: "fdn-api"},
		"owner-for-join": {config: c, certificate: owner, mode: "fdn-join"},
		"generic-mode":   {config: c, certificate: owner, mode: "shell"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTunnelClient(test.config, test.certificate, test.mode); err == nil {
				t.Fatal("unsafe tunnel configuration accepted")
			}
		})
	}
	if client, err := NewTunnelClient(c, tls.Certificate{}, "fdn-join"); err != nil {
		t.Fatal("initial join without a certificate was rejected", err)
	} else {
		client.Close()
	}
	for _, key := range []any{device.PrivateKey, ed25519.PrivateKey(nil), ed25519.PrivateKey{1, 2}} {
		wrong := owner
		wrong.PrivateKey = key
		if _, err := NewTunnelClient(c, wrong, "fdn-api"); err == nil {
			t.Fatal("mismatched or malformed certificate/private key accepted")
		}
	}
	for _, base := range []string{"https://remote.example", "http://localhost", c.URL + "/extra", c.URL + "?", c.URL + "/%2F"} {
		changed := c
		changed.URL = base
		if _, err := NewTunnelClient(changed, owner, "fdn-api"); err == nil {
			t.Fatal("non-loopback or malformed origin accepted")
		}
	}
}

func TestTunnelClientFailsClosedOnSSHOrTLSPinningFailure(t *testing.T) {
	c, owner, _, serverTLS := tunnelFixture(t)
	failed := tunnelRunner(func(context.Context, process.Spec) error { return errors.New("fixture failure") })
	client, err := newTunnelClient(c, owner, "fdn-api", failed)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Request(context.Background(), http.MethodGet, "/healthz", nil, nil); err == nil {
		t.Fatal("failed SSH became successful loopback access")
	}
	c.ServerName = "wrong.invalid"
	var starts atomic.Int64
	stops := make(chan struct{}, 4)
	var requests atomic.Int64
	runner := servingTunnel(t, serverTLS, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }), &starts, stops)
	client, err = newTunnelClient(c, owner, "fdn-api", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Request(ctx, http.MethodGet, "/healthz", nil, nil); err == nil || requests.Load() != 0 {
		t.Fatal("wrong pinned TLS server name accepted", err)
	}
	client.Close()
	awaitTunnelStop(t, stops)
}
