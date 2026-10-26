package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

type messageClient struct {
	dial     func(context.Context) (net.Conn, error)
	mu       sync.Mutex
	session  *message.Session
	cancel   context.CancelFunc
	closed   bool
	terminal error
}

// NewMessageClient attaches to a fixed transport. The dialer is established by
// the SSH/identity or protected Unix boundary, never by a message from a caller.
func NewMessageClient(base string, dial func(context.Context) (net.Conn, error)) *Client {
	return &Client{BaseURL: base, messages: &messageClient{dial: dial}}
}

func (c *Client) IsMessageClient() bool { return c != nil && c.messages != nil }

func (c *Client) messageSession(ctx context.Context) (*message.Session, error) {
	if c == nil || c.messages == nil || c.messages.dial == nil {
		return nil, domain.Fail(domain.Prerequisite, "binary identity transport is not configured")
	}
	m := c.messages
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, domain.Fail(domain.Cancelled, "message client is closed")
	}
	if m.terminal != nil {
		return nil, m.terminal
	}
	if m.session != nil {
		select {
		case <-m.session.Done():
			err := m.session.Err()
			if m.cancel != nil {
				m.cancel()
			}
			m.session, m.cancel = nil, nil
			if err != nil && domain.AsFault(err).Code == domain.Denied {
				m.terminal = err
				return nil, err
			}
		default:
			return m.session, nil
		}
	}
	dial, stopDial := context.WithTimeout(ctx, 10*time.Second)
	conn, err := m.dial(dial)
	stopDial()
	if err != nil {
		return nil, messageTransportError(ctx, err)
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	session, err := message.NewSession(lifetime, conn, nil)
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		_ = conn.Close()
		if session != nil {
			_ = session.Close()
		}
		return nil, messageTransportError(ctx, err)
	}
	m.session, m.cancel = session, cancel
	return session, nil
}

func messageTransportError(ctx context.Context, err error) error {
	var fault *domain.Fault
	if errors.As(err, &fault) {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return domain.Fail(domain.Cancelled, "message connection was cancelled")
	}
	return domain.Fail(domain.Unavailable, "identity message connection is unavailable; reconcile operation_id before retrying writes")
}

func (c *Client) closeMessages() {
	if c.messages == nil {
		return
	}
	m := c.messages
	m.mu.Lock()
	m.closed = true
	session, cancel := m.session, m.cancel
	m.session, m.cancel = nil, nil
	m.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	if cancel != nil {
		cancel()
	}
}

// Message sends an already typed, allowlisted protocol operation. It does not
// retry mutations after uncertain delivery.
func (c *Client) Message(ctx context.Context, request message.Request) ([]byte, error) {
	session, err := c.messageSession(ctx)
	if err != nil {
		return nil, err
	}
	return session.Call(ctx, request)
}

func (c *Client) Ping(ctx context.Context) error {
	session, err := c.messageSession(ctx)
	if err != nil {
		return err
	}
	return session.Ping(ctx)
}

func (c *Client) MessageSession(ctx context.Context) (*message.Session, error) {
	return c.messageSession(ctx)
}

func (c *Client) requestMessage(ctx context.Context, method, selector string, input, output any) error {
	spec, request, err := resolveOperation(method, selector)
	if err != nil {
		if c.Local && (domain.AsFault(err).Code == domain.NotFound || domain.AsFault(err).Code == domain.Invalid) {
			return domain.Fail(domain.Denied, "operation is outside the local identity proxy capabilities")
		}
		return err
	}
	if _, deadline := ctx.Deadline(); !deadline && !blockingQuery(method, selector) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	request.Payload, err = spec.encode(input)
	if err != nil {
		return err
	}
	if len(request.Payload) > domain.MaxBody {
		return domain.Fail(domain.Invalid, "command exceeds its encoded input limit")
	}
	data, err := c.Message(ctx, request)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return domain.Fail(domain.Cancelled, "request cancelled")
		}
		return err
	}
	if output == nil {
		return nil
	}
	if raw, ok := output.(*json.RawMessage); ok {
		*raw, err = spec.resultJSON(data)
		return err
	}
	value := reflect.ValueOf(output)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return domain.Fail(domain.Invalid, "message result requires a non-nil typed destination")
	}
	return message.UnmarshalPayload(data, output)
}

func blockingQuery(method, selector string) bool {
	if method != "GET" {
		return false
	}
	u, err := url.ParseRequestURI(selector)
	return err == nil && (u.Path == "/v1/updates" || u.Path == "/v1/events") && u.Query().Get("wait") == "true"
}

func DecodeMessageInput(request message.Request) (any, error) {
	spec, ok := messageCatalog()[request.Operation]
	if !ok {
		return nil, domain.Fail(domain.NotFound, "unknown message operation")
	}
	if _, err := validateMessageRequest(request, spec); err != nil {
		return nil, err
	}
	if spec.inputType == nil {
		if len(request.Payload) != 0 {
			return nil, domain.Fail(domain.Invalid, "query does not accept a command body")
		}
		return nil, nil
	}
	input := reflect.New(spec.inputType).Interface()
	if err := message.UnmarshalPayload(request.Payload, input); err != nil {
		return nil, err
	}
	return input, nil
}

// ForwardMessage preserves a selected transport; a binary connection failure
// never falls back to HTTP. The HTTP branch is for trusted business fixtures.
func (c *Client) ForwardMessage(ctx context.Context, request message.Request) ([]byte, error) {
	if c.IsMessageClient() {
		return c.Message(ctx, request)
	}
	method, selector, err := MessageSelector(request)
	if err != nil {
		return nil, err
	}
	input, err := DecodeMessageInput(request)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := c.Request(ctx, method, selector, input, &result); err != nil {
		return nil, err
	}
	return EncodeMessageResult(request, result)
}
