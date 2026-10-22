package identityservice

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

// Remote is the pinned SSH/TLS boundary. Implementations own and close only
// connections they create; no method accepts or returns a bearer credential.
type Remote interface {
	Challenge(context.Context, config.Worker) (domain.IdentityChallenge, error)
	Activate(context.Context, config.Worker, domain.ActivateIdentityRequest) (domain.IdentityCertificate, error)
	Owner(context.Context, config.Worker, tls.Certificate) (OwnerStream, error)
	Client(config.Worker, tls.Certificate) (*api.Client, error)
}

// OwnerStream has one reader and one writer. Receive must continue running even
// when no certificate is due, so WebSocket pings prove this owner is alive.
type OwnerStream interface {
	Challenge(context.Context) (domain.IdentityChallenge, error)
	Send(context.Context, domain.OwnerRequest) error
	Receive(context.Context) (domain.OwnerCertificate, error)
	Close() error
}

// Cryptography can be replaced in rootless tests, never by runtime configuration.
// Private material is consumed entirely inside this account's service boundary.
type Cryptography interface {
	Generate(string) (domain.PublicIdentity, []byte, []byte, error)
	ParseSigner([]byte) (crypto.Signer, error)
	SignChallenge(crypto.Signer, domain.IdentityChallenge) (string, error)
	SignOwner(crypto.Signer, domain.AgentIdentity, string) (domain.OwnerRequest, error)
	GenerateOwner() (crypto.Signer, error)
}

type nativeCryptography struct{}

func (nativeCryptography) Generate(name string) (domain.PublicIdentity, []byte, []byte, error) {
	return device.Generate(name)
}
func (nativeCryptography) ParseSigner(key []byte) (crypto.Signer, error) {
	return device.ParseSigner(key)
}
func (nativeCryptography) SignChallenge(key crypto.Signer, c domain.IdentityChallenge) (string, error) {
	return device.SignChallenge(key, c)
}
func (nativeCryptography) SignOwner(key crypto.Signer, i domain.AgentIdentity, nonce string) (domain.OwnerRequest, error) {
	return device.SignOwner(key, i, nonce)
}
func (nativeCryptography) GenerateOwner() (crypto.Signer, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	return key, err
}

type NativeRemote struct{}

func (NativeRemote) Challenge(ctx context.Context, c config.Worker) (domain.IdentityChallenge, error) {
	client, err := api.NewTunnelClient(c, tls.Certificate{}, "fdn-join")
	if err != nil {
		return domain.IdentityChallenge{}, err
	}
	defer client.Close()
	return api.Call[domain.IdentityChallenge](ctx, client, http.MethodGet, "/v1/identity/challenge", nil)
}

func (NativeRemote) Activate(ctx context.Context, c config.Worker, request domain.ActivateIdentityRequest) (domain.IdentityCertificate, error) {
	client, err := api.NewTunnelClient(c, tls.Certificate{}, "fdn-join")
	if err != nil {
		return domain.IdentityCertificate{}, err
	}
	defer client.Close()
	return api.Call[domain.IdentityCertificate](ctx, client, http.MethodPost, "/v1/identity/activate", request)
}

func (NativeRemote) Owner(ctx context.Context, c config.Worker, cert tls.Certificate) (OwnerStream, error) {
	client, err := api.NewTunnelClient(c, cert, "fdn-join")
	if err != nil {
		return nil, err
	}
	connection, response, err := websocket.Dial(ctx, "wss://"+strings.TrimPrefix(client.BaseURL, "https://")+"/v1/identity/owner", &websocket.DialOptions{
		HTTPClient: client.HTTP, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		client.Close()
		if response != nil && (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusConflict) {
			return nil, domain.Fail(domain.Denied, "identity owner was rejected")
		}
		return nil, domain.Fail(domain.Unavailable, "identity owner connection failed")
	}
	connection.SetReadLimit(domain.MaxBody)
	// Certificate renewal is slower than the gateway's idle limit. Keep the
	// authenticated owner stream alive independently of certificate renewal.
	heartbeatCtx, cancel := context.WithCancel(ctx)
	owner := &nativeOwner{connection: connection, client: client, cancel: cancel, heartbeatDone: make(chan struct{})}
	go func() {
		defer close(owner.heartbeatDone)
		if err := ownerKeepalive(heartbeatCtx, 20*time.Second, connection.Ping); err != nil && heartbeatCtx.Err() == nil {
			connection.CloseNow()
		}
	}()
	return owner, nil
}

func (NativeRemote) Client(c config.Worker, cert tls.Certificate) (*api.Client, error) {
	return api.NewTunnelClient(c, cert, "fdn-api")
}

type nativeOwner struct {
	connection    *websocket.Conn
	client        *api.Client
	cancel        context.CancelFunc
	heartbeatDone chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

func ownerKeepalive(ctx context.Context, interval time.Duration, ping func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ping(check)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (o *nativeOwner) Challenge(ctx context.Context) (domain.IdentityChallenge, error) {
	var challenge domain.IdentityChallenge
	err := wsjson.Read(ctx, o.connection, &challenge)
	return challenge, err
}
func (o *nativeOwner) Send(ctx context.Context, request domain.OwnerRequest) error {
	return wsjson.Write(ctx, o.connection, request)
}
func (o *nativeOwner) Receive(ctx context.Context) (domain.OwnerCertificate, error) {
	var cert domain.OwnerCertificate
	err := wsjson.Read(ctx, o.connection, &cert)
	if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
		err = domain.Fail(domain.Denied, "identity owner authority was withdrawn")
	}
	return cert, err
}
func (o *nativeOwner) Close() error {
	o.closeOnce.Do(func() {
		if o.cancel != nil {
			o.cancel()
		}
		o.closeErr = o.connection.CloseNow()
		if o.heartbeatDone != nil {
			<-o.heartbeatDone
		}
		o.client.Close()
	})
	return o.closeErr
}
