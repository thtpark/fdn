package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
)

type slot struct {
	target    net.Conn
	requester net.Conn
	done      chan struct{}
	once      sync.Once
}

type endpoint struct {
	net.Conn
	close func()
	once  sync.Once
}

func (c *endpoint) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.close)
	return err
}

type Broker struct {
	store  *foundation.Store
	mu     sync.Mutex
	slots  map[string]*slot
	closed bool
}

func New(store *foundation.Store) *Broker {
	return &Broker{store: store, slots: map[string]*slot{}}
}

func (b *Broker) Open(ctx context.Context, actor, id, side string) (net.Conn, error) {
	watermark, err := b.store.Updates(ctx, actor, "", 1)
	if err != nil {
		return nil, err
	}
	session, err := b.store.SessionAccess(ctx, actor, id, side)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, domain.Fail(domain.Unavailable, "relay is stopping")
	}
	s := b.slots[id]
	if s == nil {
		s = &slot{done: make(chan struct{})}
		b.slots[id] = s
	}
	if (side == "target" && s.target != nil) || (side == "requester" && s.requester != nil) {
		b.mu.Unlock()
		return nil, domain.Fail(domain.Conflict, "relay side is already connected")
	}
	local, peer := net.Pipe()
	if side == "target" {
		s.target = peer
	} else {
		s.requester = peer
	}
	if s.target != nil && s.requester != nil {
		go b.bridge(id, s, s.target, s.requester)
	}
	b.mu.Unlock()
	go b.guard(ctx, actor, id, side, watermark.NextCursor, session.ExpiresAt, s)
	return &endpoint{Conn: local, close: func() { b.retire(id, s) }}, nil
}

func (b *Broker) guard(ctx context.Context, actor, id, side, cursor string, expires int64, s *slot) {
	ctx, cancel := context.WithDeadline(ctx, time.Unix(expires, 0))
	defer cancel()
	stop := context.AfterFunc(ctx, func() { b.retire(id, s) })
	defer stop()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		page, err := b.store.WaitUpdates(ctx, actor, cursor, 200)
		if err != nil {
			b.retire(id, s)
			return
		}
		cursor = page.NextCursor
		if len(page.Items) > 0 {
			if _, err := b.store.SessionAccess(ctx, actor, id, side); err != nil {
				b.retire(id, s)
				return
			}
		}
	}
}

// Serve is the browser/business-fixture edge. Native relay traffic uses Open
// and bounded binary messages, not a WebSocket on Foundation's SSH ingress.
func (b *Broker) Serve(w http.ResponseWriter, r *http.Request, actor, id, side string) {
	stream, err := b.Open(r.Context(), actor, id, side)
	if err != nil {
		http.Error(w, "relay authority unavailable", domain.HTTPStatus(err))
		return
	}
	defer stream.Close()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)
	ws := websocket.NetConn(r.Context(), conn, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, ws); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ws, stream); done <- struct{}{} }()
	<-done
	_ = ws.Close()
	_ = stream.Close()
	<-done
}

func (b *Broker) bridge(id string, s *slot, target, requester net.Conn) {
	done := make(chan error, 2)
	go func() { _, err := io.Copy(target, requester); done <- err }()
	go func() { _, err := io.Copy(requester, target); done <- err }()
	first := <-done
	b.retire(id, s)
	second := <-done
	for _, err := range []error{first, second} {
		if err != nil && !IsClosed(err) {
			slog.Warn("relay stream ended", "code", domain.AsFault(err).Code)
		}
	}
}

func (b *Broker) retire(id string, s *slot) {
	s.once.Do(func() {
		b.mu.Lock()
		if b.slots[id] == s {
			delete(b.slots, id)
		}
		target, requester := s.target, s.requester
		close(s.done)
		b.mu.Unlock()
		if target != nil {
			_ = target.Close()
		}
		if requester != nil {
			_ = requester.Close()
		}
	})
}

func (b *Broker) Close() error {
	b.mu.Lock()
	b.closed = true
	slots := make(map[string]*slot, len(b.slots))
	for id, s := range b.slots {
		slots[id] = s
	}
	b.mu.Unlock()
	for id, s := range slots {
		b.retire(id, s)
	}
	return nil
}

func IsClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}
