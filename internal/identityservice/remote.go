package identityservice

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
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
	return api.Call[domain.IdentityChallenge](ctx, client, "GET", "/v1/identity/challenge", nil)
}

func (NativeRemote) Activate(ctx context.Context, c config.Worker, request domain.ActivateIdentityRequest) (domain.IdentityCertificate, error) {
	client, err := api.NewTunnelClient(c, tls.Certificate{}, "fdn-join")
	if err != nil {
		return domain.IdentityCertificate{}, err
	}
	defer client.Close()
	return api.Call[domain.IdentityCertificate](ctx, client, "POST", "/v1/identity/activate", request)
}

func (NativeRemote) Owner(ctx context.Context, c config.Worker, cert tls.Certificate) (OwnerStream, error) {
	client, err := api.NewTunnelClient(c, cert, "fdn-join")
	if err != nil {
		return nil, err
	}
	connection, err := client.MessageSession(ctx)
	if err != nil {
		client.Close()
		return nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	owner := &nativeOwner{connection: connection, client: client, cancel: cancel,
		responses: make(chan domain.OwnerCertificate, 1), heartbeatDone: make(chan struct{})}
	go func() {
		defer close(owner.heartbeatDone)
		if err := ownerKeepalive(heartbeatCtx, 20*time.Second, connection.Ping); err != nil && heartbeatCtx.Err() == nil {
			_ = connection.Shutdown(domain.Fail(domain.Unavailable, "owner keepalive failed"))
		}
	}()
	return owner, nil
}

func (NativeRemote) Client(c config.Worker, cert tls.Certificate) (*api.Client, error) {
	return api.NewTunnelClient(c, cert, "fdn-api")
}

type nativeOwner struct {
	connection    *message.Session
	client        *api.Client
	cancel        context.CancelFunc
	responses     chan domain.OwnerCertificate
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
	data, err := o.client.Message(ctx, message.Request{Operation: api.OwnerChallengeOperation})
	if err != nil {
		return challenge, err
	}
	err = message.UnmarshalPayload(data, &challenge)
	return challenge, err
}
func (o *nativeOwner) Send(ctx context.Context, request domain.OwnerRequest) error {
	data, err := message.MarshalPayload(request)
	if err != nil {
		return err
	}
	data, err = o.client.Message(ctx, message.Request{Operation: api.OwnerProveOperation, Payload: data})
	if err != nil {
		return err
	}
	var certificate domain.OwnerCertificate
	if err := message.UnmarshalPayload(data, &certificate); err != nil {
		return err
	}
	select {
	case o.responses <- certificate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-o.connection.Done():
		return o.connection.Err()
	}
}
func (o *nativeOwner) Receive(ctx context.Context) (domain.OwnerCertificate, error) {
	select {
	case certificate := <-o.responses:
		return certificate, nil
	case <-ctx.Done():
		return domain.OwnerCertificate{}, ctx.Err()
	case <-o.connection.Done():
		return domain.OwnerCertificate{}, o.connection.Err()
	}
}
func (o *nativeOwner) Close() error {
	o.closeOnce.Do(func() {
		if o.cancel != nil {
			o.cancel()
		}
		o.client.Close()
		if o.heartbeatDone != nil {
			<-o.heartbeatDone
		}
	})
	return o.closeErr
}
