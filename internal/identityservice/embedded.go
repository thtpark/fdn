package identityservice

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"sync"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

// EmbeddedAttachment is an app-private capability channel, not a configurable
// replacement for kernel peers. Only connections created by this attachment
// can reach the existing identity proxy's method/path/principal checks.
type EmbeddedAttachment struct {
	settings config.IdentityService
	mu       sync.Mutex
	listened bool
	closed   bool
	queue    chan net.Conn
	done     chan struct{}
	owned    map[*embeddedPeer]struct{}
}

type embeddedPeer struct {
	net.Conn
	attachment *EmbeddedAttachment
}

func NewEmbeddedAttachment(c config.IdentityService) (*EmbeddedAttachment, error) {
	c = c.WithCurrentUser()
	if err := CheckNativeAccount(c); err != nil {
		return nil, err
	}
	return &EmbeddedAttachment{settings: c, queue: make(chan net.Conn, 16), done: make(chan struct{}), owned: make(map[*embeddedPeer]struct{})}, nil
}

func (a *EmbeddedAttachment) Listen(c config.IdentityService) (net.Listener, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c = c.WithCurrentUser()
	if a.closed || a.listened || !reflect.DeepEqual(c, a.settings) {
		return nil, domain.Fail(domain.Denied, "embedded identity attachment cannot be rebound or reopened")
	}
	a.listened = true
	return a, nil
}

func (a *EmbeddedAttachment) Peer(connection net.Conn) (Peer, error) {
	peer, ok := connection.(*embeddedPeer)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, owned := a.owned[peer]
	if !ok || peer.attachment != a || !owned || a.closed {
		return Peer{}, domain.Fail(domain.Denied, "connection does not carry this app's private identity capability")
	}
	return Peer{UID: os.Geteuid(), GID: os.Getegid()}, nil
}

func (a *EmbeddedAttachment) Dial(ctx context.Context) (net.Conn, error) {
	a.mu.Lock()
	if a.closed || !a.listened {
		a.mu.Unlock()
		return nil, domain.Fail(domain.Unavailable, "embedded identity attachment is not active")
	}
	client, server := net.Pipe()
	peer := &embeddedPeer{Conn: server, attachment: a}
	a.owned[peer] = struct{}{}
	a.mu.Unlock()
	select {
	case a.queue <- peer:
		return client, nil
	case <-ctx.Done():
		client.Close()
		peer.Close()
		return nil, ctx.Err()
	case <-a.done:
		client.Close()
		peer.Close()
		return nil, net.ErrClosed
	}
}

func (a *EmbeddedAttachment) Accept() (net.Conn, error) {
	select {
	case connection := <-a.queue:
		return connection, nil
	case <-a.done:
		return nil, net.ErrClosed
	}
}

func (a *EmbeddedAttachment) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	close(a.done)
	var err error
	for peer := range a.owned {
		err = errors.Join(err, peer.Conn.Close())
	}
	a.owned = nil
	return err
}

func (a *EmbeddedAttachment) Addr() net.Addr { return embeddedAddress{} }
func (p *embeddedPeer) Close() error {
	p.attachment.mu.Lock()
	delete(p.attachment.owned, p)
	p.attachment.mu.Unlock()
	return p.Conn.Close()
}

type embeddedAddress struct{}

func (embeddedAddress) Network() string { return "embedded" }
func (embeddedAddress) String() string  { return "private-identity-capability" }
