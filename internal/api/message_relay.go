package api

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

const relayChunkBytes = 32 << 10

type RelayOpen = domain.RelayOpen
type RelayChunk = domain.RelayChunk
type RelayWritten = domain.RelayWritten

type relayPeer struct {
	server   *Server
	ctx      context.Context
	actor    domain.Agent
	mu       sync.Mutex
	streams  map[string]*relayStream
	openings map[string]bool
}

type relayStream struct {
	net.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func newRelayPeer(server *Server, ctx context.Context, actor domain.Agent) *relayPeer {
	return &relayPeer{server: server, ctx: ctx, actor: actor, streams: map[string]*relayStream{}, openings: map[string]bool{}}
}

func (p *relayPeer) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ticket, stream := range p.streams {
		_ = stream.Close()
		delete(p.streams, ticket)
	}
}

func (p *relayPeer) call(ctx context.Context, request message.Request) ([]byte, error) {
	if len(request.Query) != 0 || len(request.Payload) > 64<<10 {
		return nil, domain.Fail(domain.Invalid, "relay accepts only bounded typed messages")
	}
	if request.Operation == "relay.open" {
		if request.Target != "" {
			return nil, domain.Fail(domain.Invalid, "relay open does not accept a target override")
		}
		var input RelayOpen
		if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
			return nil, err
		}
		if !domain.ValidOperation(input.Ticket) {
			return nil, domain.Fail(domain.Invalid, "relay ticket is invalid")
		}
		p.mu.Lock()
		if len(p.streams)+len(p.openings) >= 4 || p.streams[input.Ticket] != nil || p.openings[input.Ticket] {
			p.mu.Unlock()
			return nil, domain.Fail(domain.Conflict, "relay admission is occupied or at its connection bound")
		}
		p.openings[input.Ticket] = true
		p.mu.Unlock()
		stream, err := p.server.Relay.Open(p.ctx, p.actor.ID, input.SessionID, input.Side)
		p.mu.Lock()
		delete(p.openings, input.Ticket)
		if err == nil && ctx.Err() == nil && p.ctx.Err() == nil {
			p.streams[input.Ticket] = &relayStream{Conn: stream}
		} else if stream != nil {
			_ = stream.Close()
		}
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil || p.ctx.Err() != nil {
			return nil, domain.Fail(domain.Cancelled, "relay admission was cancelled")
		}
		return message.MarshalPayload(input.Ticket)
	}
	if !domain.ValidOperation(request.Target) {
		return nil, domain.Fail(domain.Invalid, "relay ticket is invalid")
	}
	if request.Operation == "relay.close" && len(request.Payload) != 0 {
		return nil, domain.Fail(domain.Invalid, "relay close does not accept a body")
	}
	p.mu.Lock()
	stream := p.streams[request.Target]
	if request.Operation == "relay.close" {
		delete(p.streams, request.Target)
	}
	p.mu.Unlock()
	if request.Operation == "relay.close" {
		if stream != nil {
			if err := stream.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				return nil, domain.Fail(domain.Unavailable, "relay cleanup remains pending")
			}
		}
		return message.MarshalPayload(true)
	}
	if stream == nil {
		return nil, domain.Fail(domain.Denied, "relay ticket is not owned by this connection")
	}
	switch request.Operation {
	case "relay.read":
		if len(request.Payload) != 0 {
			return nil, domain.Fail(domain.Invalid, "relay read does not accept a body")
		}
		stream.readMu.Lock()
		defer stream.readMu.Unlock()
		if ctx.Err() != nil {
			return nil, domain.Fail(domain.Cancelled, "relay read was cancelled")
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = stream.SetReadDeadline(deadline)
		} else {
			_ = stream.SetReadDeadline(time.Time{})
		}
		stop := cancelRelayIO(ctx, func() { _ = stream.SetReadDeadline(time.Now()) })
		defer stop()
		data := make([]byte, relayChunkBytes)
		n, err := stream.Read(data)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, domain.Fail(domain.Unavailable, "authorized relay read ended")
		}
		return message.MarshalPayload(RelayChunk{Data: data[:n], EOF: errors.Is(err, io.EOF)})
	case "relay.write":
		var input RelayChunk
		if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
			return nil, err
		}
		if len(input.Data) > relayChunkBytes || input.EOF {
			return nil, domain.Fail(domain.Invalid, "relay write exceeds its chunk contract")
		}
		stream.writeMu.Lock()
		defer stream.writeMu.Unlock()
		if ctx.Err() != nil {
			return nil, domain.Fail(domain.Cancelled, "relay write was cancelled")
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = stream.SetWriteDeadline(deadline)
		} else {
			_ = stream.SetWriteDeadline(time.Time{})
		}
		stop := cancelRelayIO(ctx, func() { _ = stream.SetWriteDeadline(time.Now()) })
		defer stop()
		n, err := stream.Write(input.Data)
		if err != nil {
			return nil, domain.Fail(domain.Unavailable, "authorized relay write ended")
		}
		return message.MarshalPayload(RelayWritten{Written: n})
	default:
		return nil, domain.Fail(domain.Denied, "unknown relay message")
	}
}

func cancelRelayIO(ctx context.Context, cancel func()) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		cancel()
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

type messageRelay struct {
	client        *Client
	ctx           context.Context
	cancel        context.CancelFunc
	ticket        string
	mu            sync.RWMutex
	readMu        sync.Mutex
	writeMu       sync.Mutex
	pending       []byte
	eof           bool
	readDeadline  time.Time
	writeDeadline time.Time
	readIO        *relayIO
	writeIO       *relayIO
	once          sync.Once
	closeErr      error
}

type relayIO struct {
	cancel   context.CancelCauseFunc
	timer    *time.Timer
	deadline time.Time
	finished bool
}

func DialMessageRelay(ctx context.Context, client *Client, sessionID, side string) (net.Conn, error) {
	if side != "requester" && side != "target" {
		return nil, domain.Fail(domain.Invalid, "relay side is invalid")
	}
	ctx, cancel := context.WithCancel(ctx)
	ticket := domain.NewID("relay")
	data, err := message.MarshalPayload(RelayOpen{SessionID: sessionID, Side: side, Ticket: ticket})
	if err == nil {
		_, err = client.Message(ctx, message.Request{Operation: "relay.open", Payload: data})
	}
	if err != nil {
		cancel()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		_, closeErr := client.Message(cleanup, message.Request{Operation: "relay.close", Target: ticket})
		stop()
		err = errors.Join(err, closeErr)
		return nil, err
	}
	return &messageRelay{client: client, ctx: ctx, cancel: cancel, ticket: ticket}, nil
}

func (c *messageRelay) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(c.pending) > 0 {
		n := copy(output, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.eof {
		return 0, io.EOF
	}
	ctx, finish := c.beginIO(true)
	defer finish()
	data, err := c.client.Message(ctx, message.Request{Operation: "relay.read", Target: c.ticket})
	if err != nil {
		if errors.Is(context.Cause(ctx), os.ErrDeadlineExceeded) {
			return 0, os.ErrDeadlineExceeded
		}
		return 0, err
	}
	var chunk RelayChunk
	if err := message.UnmarshalPayload(data, &chunk); err != nil {
		return 0, err
	}
	if len(chunk.Data) > relayChunkBytes {
		return 0, domain.Fail(domain.Unavailable, "relay returned an oversized chunk")
	}
	c.eof = chunk.EOF
	n := copy(output, chunk.Data)
	c.pending = chunk.Data[n:]
	if n == 0 && c.eof {
		return 0, io.EOF
	}
	return n, nil
}

func (c *messageRelay) Write(input []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, finish := c.beginIO(false)
	defer finish()
	written := 0
	for len(input) > 0 {
		chunk := input[:min(len(input), relayChunkBytes)]
		data, err := message.MarshalPayload(RelayChunk{Data: chunk})
		if err != nil {
			return written, err
		}
		data, err = c.client.Message(ctx, message.Request{Operation: "relay.write", Target: c.ticket, Payload: data})
		if err != nil {
			if errors.Is(context.Cause(ctx), os.ErrDeadlineExceeded) {
				return written, os.ErrDeadlineExceeded
			}
			return written, err
		}
		var result RelayWritten
		if err := message.UnmarshalPayload(data, &result); err != nil {
			return written, err
		}
		if result.Written < 1 || result.Written > len(chunk) {
			return written, domain.Fail(domain.Unavailable, "relay returned an invalid write receipt")
		}
		written += result.Written
		input = input[result.Written:]
	}
	return written, nil
}

func (c *messageRelay) beginIO(read bool) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(c.ctx)
	operation := &relayIO{cancel: cancel}
	c.mu.Lock()
	if read {
		c.readIO = operation
		c.setDeadline(operation, c.readDeadline)
	} else {
		c.writeIO = operation
		c.setDeadline(operation, c.writeDeadline)
	}
	c.mu.Unlock()
	return ctx, func() {
		c.mu.Lock()
		operation.finished = true
		if operation.timer != nil {
			operation.timer.Stop()
		}
		if read && c.readIO == operation {
			c.readIO = nil
		} else if !read && c.writeIO == operation {
			c.writeIO = nil
		}
		c.mu.Unlock()
		cancel(nil)
	}
}

func (c *messageRelay) setDeadline(operation *relayIO, deadline time.Time) {
	if operation == nil {
		return
	}
	operation.deadline = deadline
	if operation.timer != nil {
		operation.timer.Stop()
		operation.timer = nil
	}
	if deadline.IsZero() {
		return
	}
	operation.timer = time.AfterFunc(max(time.Until(deadline), 0), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !operation.finished && !operation.deadline.IsZero() && !operation.deadline.After(time.Now()) {
			operation.cancel(os.ErrDeadlineExceeded)
		}
	})
}

func (c *messageRelay) Close() error {
	c.once.Do(func() {
		c.cancel()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), time.Second)
		_, c.closeErr = c.client.Message(ctx, message.Request{Operation: "relay.close", Target: c.ticket})
		cancel()
	})
	return c.closeErr
}

type relayAddress string

func (a relayAddress) Network() string { return "ssh-message" }
func (a relayAddress) String() string  { return string(a) }
func (c *messageRelay) LocalAddr() net.Addr {
	return relayAddress("local")
}
func (c *messageRelay) RemoteAddr() net.Addr {
	return relayAddress("foundation")
}
func (c *messageRelay) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline, c.writeDeadline = deadline, deadline
	c.setDeadline(c.readIO, deadline)
	c.setDeadline(c.writeIO, deadline)
	c.mu.Unlock()
	return nil
}
func (c *messageRelay) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.setDeadline(c.readIO, deadline)
	c.mu.Unlock()
	return nil
}
func (c *messageRelay) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.setDeadline(c.writeIO, deadline)
	c.mu.Unlock()
	return nil
}
