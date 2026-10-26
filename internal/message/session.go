// Package message implements the versioned, full-duplex S2 protocol over an
// already admitted net.Conn. It does not authenticate or authorize its peers.
package message

import (
	"bytes"
	"context"
	"crypto/sha256"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message/wire"
	"google.golang.org/protobuf/proto"
)

type Request struct {
	Operation string
	Target    string
	Query     map[string]string
	Payload   []byte
}

type Handler func(context.Context, Request) ([]byte, error)

type reply struct {
	payload []byte
	err     error
}

type pending struct {
	id         uint64
	ping       bool
	ctx        context.Context
	reply      chan reply
	state      uint8 // queued, writing, sent
	abandoned  bool
	cancelSent bool
}

type incoming struct {
	ctx       context.Context
	cancel    context.CancelFunc
	stop      func() bool
	responded bool
}

type outbound struct {
	envelope    *wire.Envelope
	pending     *pending
	terminal    bool
	queuedBytes int
}

type Session struct {
	conn     net.Conn
	handler  Handler
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	data     chan outbound
	control  chan outbound
	terminal chan outbound

	mu            sync.Mutex
	err           error
	stopping      bool
	nextCall      uint64
	nextPing      uint64
	lastCall      uint64
	lastPing      uint64
	calls         map[uint64]*pending
	pings         map[uint64]*pending
	incoming      map[uint64]*incoming
	shutdownTimer *time.Timer
	queuedBytes   int
}

var payloadHash = sha256.Sum256([]byte(payloadContract))

// NewSession simultaneously sends and reads each handshake phase, including on
// unbuffered net.Pipe connections. ctx owns the entire connection lifetime.
// A nil handler permits calls/pings out, but rejects incoming business calls.
func NewSession(ctx context.Context, conn net.Conn, handler Handler) (*Session, error) {
	if ctx == nil || conn == nil {
		return nil, invalid("message session requires a context and connection")
	}
	handshake, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	stop := context.AfterFunc(handshake, func() { conn.Close() })
	defer func() {
		stop()
		cancel()
	}()
	fail := func(err error) (*Session, error) {
		conn.Close()
		if handshake.Err() != nil {
			return nil, handshake.Err()
		}
		return nil, err
	}
	if deadline, ok := handshake.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fail(unavailable("message handshake deadline unavailable"))
		}
	}
	hello := &wire.Envelope{Body: &wire.Envelope_Hello{Hello: &wire.Hello{
		Version: ProtocolVersion, PayloadSchemaSha256: payloadHash[:],
	}}}
	peer, err := exchange(conn, hello)
	if err != nil {
		return fail(err)
	}
	if terminal := peer.GetTerminal(); terminal != nil {
		return fail(fromFault(terminal))
	}
	if peer.GetHello() == nil || peer.GetHello().Version != ProtocolVersion ||
		!bytes.Equal(peer.GetHello().PayloadSchemaSha256, payloadHash[:]) {
		denied := domain.Fail(domain.Denied, "incompatible message protocol or payload schema")
		_ = writeEnvelope(conn, faultEnvelope(denied), TerminalWriteTimeout)
		return fail(denied)
	}
	peer, err = exchange(conn, &wire.Envelope{Body: &wire.Envelope_Ready{Ready: &wire.Ready{}}})
	if err != nil {
		return fail(err)
	}
	if terminal := peer.GetTerminal(); terminal != nil {
		return fail(fromFault(terminal))
	}
	if peer.GetReady() == nil {
		return fail(invalid("message handshake confirmation required"))
	}
	if handshake.Err() != nil {
		return fail(handshake.Err())
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(unavailable("message session deadlines unavailable"))
	}
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	s := &Session{
		conn: conn, handler: handler, ctx: sessionCtx, cancel: sessionCancel, done: make(chan struct{}),
		data: make(chan outbound, MaxQueuedFrames), control: make(chan outbound, MaxControlFrames),
		terminal: make(chan outbound, 1), calls: make(map[uint64]*pending),
		pings: make(map[uint64]*pending), incoming: make(map[uint64]*incoming),
	}
	go s.writeLoop()
	go s.readLoop()
	go func() {
		select {
		case <-sessionCtx.Done():
			s.terminate(sessionCtx.Err())
		case <-s.done:
		}
	}()
	return s, nil
}

func exchange(conn net.Conn, own *wire.Envelope) (*wire.Envelope, error) {
	written := make(chan error, 1)
	go func() { written <- writeEnvelope(conn, own, WriteTimeout) }()
	peer, err := readEnvelope(conn)
	if err != nil {
		conn.Close()
		<-written
		return nil, err
	}
	if err := <-written; err != nil {
		return nil, err
	}
	return peer, nil
}

func validToken(value string, max int, empty bool) bool {
	if len(value) == 0 {
		return empty
	}
	if len(value) > max {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-:@", r)) {
			return false
		}
	}
	return true
}

func validateRequest(req Request) error {
	if !domain.ValidOperation(req.Operation) || !validToken(req.Target, MaxTargetBytes, true) ||
		len(req.Payload) > MaxRequestBytes || len(req.Query) > MaxQueryEntries {
		return invalid("invalid message request or request limit exceeded")
	}
	size := 0
	for key, value := range req.Query {
		if !validToken(key, MaxQueryKeyBytes, false) || len(value) > MaxQueryValueBytes ||
			!utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return invalid("invalid message query")
		}
		size += len(key) + len(value)
	}
	if size > MaxQueryBytes {
		return invalid("message query limit exceeded")
	}
	return nil
}

func (s *Session) Call(ctx context.Context, req Request) ([]byte, error) {
	if ctx == nil {
		return nil, invalid("message call requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	call := &wire.Call{Operation: req.Operation, Target: req.Target, Payload: bytes.Clone(req.Payload)}
	keys := make([]string, 0, len(req.Query))
	for key := range req.Query {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		call.Query = append(call.Query, &wire.Metadata{Key: key, Value: req.Query[key]})
	}
	if deadline, ok := ctx.Deadline(); ok {
		nanos := deadline.UnixNano()
		if nanos <= 0 || !time.Unix(0, nanos).Equal(deadline) {
			return nil, invalid("message deadline is out of range")
		}
		call.DeadlineUnixNano = nanos
	}
	p := &pending{ctx: ctx, reply: make(chan reply, 1)}
	s.mu.Lock()
	if s.stopping {
		err := s.err
		s.mu.Unlock()
		return nil, err
	}
	if len(s.calls) >= MaxPendingCalls || s.nextCall == math.MaxUint64 {
		s.mu.Unlock()
		return nil, unavailable("message call admission limit exceeded")
	}
	s.nextCall++
	p.id, call.Id = s.nextCall, s.nextCall
	s.calls[p.id] = p
	frame := outbound{envelope: &wire.Envelope{Body: &wire.Envelope_Call{Call: call}}, pending: p}
	frame.queuedBytes = proto.Size(frame.envelope)
	if s.queuedBytes+frame.queuedBytes > MaxQueuedBytes {
		delete(s.calls, p.id)
		s.mu.Unlock()
		return nil, unavailable("message write byte limit exceeded")
	}
	select {
	case s.data <- frame:
		s.queuedBytes += frame.queuedBytes
		s.mu.Unlock()
	default:
		delete(s.calls, p.id)
		s.mu.Unlock()
		return nil, unavailable("message write queue saturated")
	}
	return s.wait(ctx, p)
}

func (s *Session) Ping(ctx context.Context) error {
	if ctx == nil {
		return invalid("message ping requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p := &pending{ctx: ctx, ping: true, reply: make(chan reply, 1)}
	s.mu.Lock()
	if s.stopping {
		err := s.err
		s.mu.Unlock()
		return err
	}
	if len(s.pings) >= MaxPendingPings || s.nextPing == math.MaxUint64 {
		s.mu.Unlock()
		return unavailable("message ping admission limit exceeded")
	}
	s.nextPing++
	p.id = s.nextPing
	s.pings[p.id] = p
	select {
	case s.control <- outbound{envelope: &wire.Envelope{Body: &wire.Envelope_Ping{Ping: &wire.Ping{Id: p.id}}}, pending: p}:
		s.mu.Unlock()
	default:
		delete(s.pings, p.id)
		s.mu.Unlock()
		return unavailable("message control queue saturated")
	}
	_, err := s.wait(ctx, p)
	return err
}

func (s *Session) wait(ctx context.Context, p *pending) ([]byte, error) {
	select {
	case result := <-p.reply:
		return result.payload, result.err
	case <-ctx.Done():
		s.abandon(p)
		return nil, ctx.Err()
	case <-s.done:
		select {
		case result := <-p.reply:
			return result.payload, result.err
		default:
			return nil, s.Err()
		}
	}
}

func (s *Session) pendingMap(p *pending) map[uint64]*pending {
	if p.ping {
		return s.pings
	}
	return s.calls
}

func (s *Session) abandon(p *pending) {
	sendCancel := false
	s.mu.Lock()
	table := s.pendingMap(p)
	if table[p.id] == p {
		p.abandoned = true
		if p.state == 0 {
			delete(table, p.id)
		} else if !p.ping && p.state == 2 && !p.cancelSent {
			p.cancelSent, sendCancel = true, true
		}
	}
	s.mu.Unlock()
	if sendCancel {
		s.sendCancel(p.id)
	}
}

func (s *Session) sendCancel(id uint64) {
	s.enqueue(outbound{envelope: &wire.Envelope{Body: &wire.Envelope_Cancel{Cancel: &wire.Cancel{Id: id}}}}, true)
}

func (s *Session) enqueue(frame outbound, priority bool) {
	if !priority {
		frame.queuedBytes = proto.Size(frame.envelope)
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	queue := s.data
	if priority {
		queue = s.control
	}
	if s.queuedBytes+frame.queuedBytes > MaxQueuedBytes {
		s.mu.Unlock()
		s.initiateShutdown(unavailable("message response byte limit exceeded"))
		return
	}
	select {
	case queue <- frame:
		s.queuedBytes += frame.queuedBytes
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		s.initiateShutdown(unavailable("message response queue saturated"))
	}
}

func (s *Session) writeLoop() {
	controlBurst := 0
	for {
		var frame outbound
		select {
		case <-s.done:
			return
		default:
		}
		select {
		case frame = <-s.terminal:
		default:
			if controlBurst >= 8 {
				select {
				case frame = <-s.data:
					controlBurst = 0
				default:
				}
			}
			if frame.envelope == nil {
				select {
				case frame = <-s.control:
					controlBurst++
				default:
					select {
					case <-s.done:
						return
					case frame = <-s.terminal:
					case frame = <-s.control:
						controlBurst++
					case frame = <-s.data:
						controlBurst = 0
					}
				}
			}
		}
		if frame.queuedBytes != 0 {
			s.mu.Lock()
			s.queuedBytes -= frame.queuedBytes
			s.mu.Unlock()
		}
		if frame.pending != nil {
			s.mu.Lock()
			p := frame.pending
			table := s.pendingMap(p)
			if s.stopping || table[p.id] != p || p.ctx.Err() != nil {
				delete(table, p.id)
				s.mu.Unlock()
				continue
			}
			p.state = 1
			s.mu.Unlock()
		}
		timeout := WriteTimeout
		if frame.terminal {
			timeout = TerminalWriteTimeout
		}
		if err := writeEnvelope(s.conn, frame.envelope, timeout); err != nil {
			s.terminate(err)
			return
		}
		if frame.terminal {
			s.terminate(s.Err())
			return
		}
		if frame.pending != nil {
			sendCancel := false
			s.mu.Lock()
			p := frame.pending
			if s.pendingMap(p)[p.id] == p {
				p.state = 2
				if p.ctx.Err() != nil {
					p.abandoned = true
				}
				if p.abandoned && !p.ping && !p.cancelSent {
					p.cancelSent, sendCancel = true, true
				}
			}
			s.mu.Unlock()
			if sendCancel {
				s.sendCancel(frame.pending.id)
			}
		}
	}
}

func (s *Session) readLoop() {
	for {
		envelope, err := readEnvelope(s.conn)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.initiateShutdown(err)
			return
		}
		switch body := envelope.Body.(type) {
		case *wire.Envelope_Call:
			err = s.receiveCall(body.Call)
		case *wire.Envelope_Result:
			err = s.receiveResult(body.Result)
		case *wire.Envelope_Ping:
			s.mu.Lock()
			if body.Ping.Id == 0 || body.Ping.Id <= s.lastPing {
				err = invalid("duplicate or invalid message ping ID")
			} else {
				s.lastPing = body.Ping.Id
			}
			s.mu.Unlock()
			if err == nil {
				s.enqueue(outbound{envelope: &wire.Envelope{Body: &wire.Envelope_Pong{Pong: &wire.Pong{Id: body.Ping.Id}}}}, true)
			}
		case *wire.Envelope_Pong:
			s.mu.Lock()
			p := s.pings[body.Pong.Id]
			if p == nil || p.state == 0 {
				err = invalid("duplicate or unknown message pong ID")
			} else {
				delete(s.pings, p.id)
				if !p.abandoned {
					p.reply <- reply{}
				}
			}
			s.mu.Unlock()
		case *wire.Envelope_Cancel:
			s.mu.Lock()
			valid := body.Cancel.Id != 0 && body.Cancel.Id <= s.lastCall
			s.mu.Unlock()
			if !valid {
				err = invalid("unknown message cancellation ID")
			} else {
				s.cancelIncoming(body.Cancel.Id, context.Canceled)
			}
		case *wire.Envelope_Terminal:
			s.terminate(fromFault(body.Terminal))
			return
		default:
			err = invalid("message variant is not valid after negotiation")
		}
		if err != nil {
			s.initiateShutdown(err)
			return
		}
	}
}

func (s *Session) receiveCall(call *wire.Call) error {
	req := Request{Operation: call.Operation, Target: call.Target, Payload: call.Payload}
	if len(call.Query) > MaxQueryEntries || call.DeadlineUnixNano < 0 {
		return invalid("invalid message request metadata")
	}
	if len(call.Query) != 0 {
		req.Query = make(map[string]string, len(call.Query))
		for _, item := range call.Query {
			if _, exists := req.Query[item.Key]; exists {
				return invalid("duplicate message query key")
			}
			req.Query[item.Key] = item.Value
		}
	}
	if err := validateRequest(req); err != nil {
		return err
	}
	s.mu.Lock()
	if call.Id == 0 || call.Id <= s.lastCall {
		s.mu.Unlock()
		return invalid("duplicate or invalid message call ID")
	}
	s.lastCall = call.Id
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	if s.handler == nil || len(s.incoming) >= MaxConcurrentHandlers {
		err := unavailable("message handler admission limit exceeded")
		if s.handler == nil {
			err = domain.Fail(domain.NotFound, "message handler unavailable")
		}
		s.mu.Unlock()
		s.sendResult(call.Id, nil, err, true)
		return nil
	}
	var callCtx context.Context
	var cancel context.CancelFunc
	if call.DeadlineUnixNano != 0 {
		callCtx, cancel = context.WithDeadline(s.ctx, time.Unix(0, call.DeadlineUnixNano))
	} else {
		callCtx, cancel = context.WithCancel(s.ctx)
	}
	in := &incoming{ctx: callCtx, cancel: cancel}
	s.incoming[call.Id] = in
	s.mu.Unlock()
	stop := context.AfterFunc(callCtx, func() { s.cancelIncoming(call.Id, callCtx.Err()) })
	s.mu.Lock()
	if s.incoming[call.Id] == in {
		in.stop = stop
	} else {
		stop()
	}
	s.mu.Unlock()
	go s.runHandler(call.Id, in, req)
	return nil
}

func (s *Session) runHandler(id uint64, in *incoming, req Request) {
	var payload []byte
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = domain.Fail(domain.Internal, "message handler failed")
			}
		}()
		if err = in.ctx.Err(); err == nil {
			payload, err = s.handler(in.ctx, req)
		}
	}()
	if in.ctx.Err() != nil {
		err = in.ctx.Err()
	}
	if err == nil && len(payload) > MaxPayloadBytes {
		err = invalid("message result too large")
	}
	s.mu.Lock()
	if s.incoming[id] != in {
		s.mu.Unlock()
		return
	}
	delete(s.incoming, id)
	respond := !in.responded
	stop := in.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	in.cancel()
	if respond {
		s.sendResult(id, payload, err, err != nil)
	}
}

func (s *Session) cancelIncoming(id uint64, err error) {
	s.mu.Lock()
	in := s.incoming[id]
	respond := in != nil && !in.responded
	if in != nil {
		in.responded = true
	}
	s.mu.Unlock()
	if in != nil {
		in.cancel()
	}
	if respond {
		s.sendResult(id, nil, err, true)
	}
}

func (s *Session) sendResult(id uint64, payload []byte, err error, priority bool) {
	result := &wire.Result{Id: id}
	if err != nil {
		result.Outcome = &wire.Result_Fault{Fault: safeFault(err)}
	} else {
		result.Outcome = &wire.Result_Payload{Payload: bytes.Clone(payload)}
	}
	s.enqueue(outbound{envelope: &wire.Envelope{Body: &wire.Envelope_Result{Result: result}}}, priority)
}

func (s *Session) receiveResult(result *wire.Result) error {
	var response reply
	switch body := result.Outcome.(type) {
	case *wire.Result_Payload:
		if len(body.Payload) > MaxPayloadBytes {
			return invalid("message result too large")
		}
		response.payload = body.Payload
	case *wire.Result_Fault:
		if body.Fault == nil || body.Fault.Code == wire.Fault_UNSPECIFIED {
			return invalid("invalid message result fault")
		}
		response.err = fromFault(body.Fault)
	default:
		return invalid("message result outcome required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.calls[result.Id]
	if p == nil || p.state == 0 {
		return invalid("duplicate or unknown message result ID")
	}
	delete(s.calls, result.Id)
	if !p.abandoned {
		p.reply <- response
	}
	return nil
}

func faultEnvelope(err error) *wire.Envelope {
	return &wire.Envelope{Body: &wire.Envelope_Terminal{Terminal: safeFault(err)}}
}

func (s *Session) initiateShutdown(err error) {
	if err == nil {
		err = unavailable("message session shut down")
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping, s.err = true, fromFault(safeFault(err))
	s.terminal <- outbound{envelope: faultEnvelope(err), terminal: true}
	s.shutdownTimer = time.AfterFunc(shutdownTimeout, func() { s.terminate(s.Err()) })
	s.mu.Unlock()
}

// Shutdown attempts one bounded, priority terminal fault before closing. A
// responsive peer observes the typed fault (including Denied), never raw detail.
// A broken/stalled connection can only be closed; terminal delivery is not an ack.
func (s *Session) Shutdown(err error) error {
	s.initiateShutdown(err)
	<-s.done
	return nil
}

func (s *Session) Close() error {
	s.terminate(unavailable("message session closed"))
	return nil
}

func (s *Session) terminate(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		if s.err == nil {
			if err == nil {
				err = unavailable("message session closed")
			}
			s.err = err
		}
		s.stopping = true
		timer := s.shutdownTimer
		incoming := s.incoming
		s.incoming = nil
		s.calls, s.pings = nil, nil
		close(s.done)
		s.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		s.cancel()
		s.conn.Close()
		for _, in := range incoming {
			if in.stop != nil {
				in.stop()
			}
			in.cancel()
		}
	})
}

func (s *Session) Done() <-chan struct{} { return s.done }

// Err is nil until closure/terminal shutdown has begun.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
