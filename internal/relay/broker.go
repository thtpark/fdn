package relay

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/foundation"
)

type slot struct {
	target            *websocket.Conn
	requester         *websocket.Conn
	targetReserved    bool
	requesterReserved bool
	done              chan struct{}
	once              sync.Once
}

type Broker struct {
	store  *foundation.Store
	mu     sync.Mutex
	slots  map[string]*slot
	closed bool
}

func New(store *foundation.Store) *Broker { return &Broker{store: store, slots: map[string]*slot{}} }

func (b *Broker) Serve(w http.ResponseWriter, r *http.Request, actor, id, side string) {
	session, err := b.store.SessionAccess(r.Context(), actor, id, side)
	if err != nil {
		http.Error(w, "relay authority changed", http.StatusForbidden)
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	s := b.slots[id]
	if s == nil {
		s = &slot{done: make(chan struct{})}
		b.slots[id] = s
	}
	busy := (side == "target" && s.targetReserved) || (side == "requester" && s.requesterReserved)
	if busy {
		b.mu.Unlock()
		http.Error(w, "relay side already connected", http.StatusConflict)
		return
	}
	if side == "target" {
		s.targetReserved = true
	} else {
		s.requesterReserved = true
	}
	b.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		b.retire(id, s)
		return
	}
	conn.SetReadLimit(64 << 10)
	b.mu.Lock()
	select {
	case <-s.done:
		b.mu.Unlock()
		conn.CloseNow()
		return
	default:
	}
	if side == "target" {
		s.target = conn
	} else {
		s.requester = conn
	}
	if s.target != nil && s.requester != nil {
		go b.bridge(id, s, s.target, s.requester)
	}
	b.mu.Unlock()
	defer b.retire(id, s)
	expiry := time.NewTimer(max(time.Until(time.Unix(session.ExpiresAt, 0)), 0))
	defer expiry.Stop()
	check := time.NewTicker(250 * time.Millisecond)
	defer check.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-r.Context().Done():
			return
		case <-expiry.C:
			return
		case <-check.C:
			if _, err := b.store.SessionAccess(r.Context(), actor, id, side); err != nil {
				return
			}
		}
	}
}

func (b *Broker) bridge(id string, s *slot, target, requester *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	left := websocket.NetConn(ctx, target, websocket.MessageBinary)
	right := websocket.NetConn(ctx, requester, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(left, right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(right, left); done <- struct{}{} }()
	<-done
	cancel()
	b.retire(id, s)
	<-done
}

func (b *Broker) retire(id string, s *slot) {
	s.once.Do(func() {
		b.mu.Lock()
		delete(b.slots, id)
		target, requester := s.target, s.requester
		close(s.done)
		b.mu.Unlock()
		if target != nil {
			target.CloseNow()
		}
		if requester != nil {
			requester.CloseNow()
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
