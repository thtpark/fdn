package message

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message/wire"
	"google.golang.org/protobuf/proto"
)

type newResult struct {
	session *Session
	err     error
}

func sessionPair(t *testing.T, left, right Handler) (*Session, *Session) {
	t.Helper()
	a, b := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	result := make(chan newResult, 1)
	go func() {
		session, err := NewSession(ctx, a, left)
		result <- newResult{session, err}
	}()
	rightSession, err := NewSession(ctx, b, right)
	if err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil {
		rightSession.Close()
		t.Fatal(r.err)
	}
	t.Cleanup(func() { r.session.Close(); rightSession.Close() })
	return r.session, rightSession
}

func rawSession(t *testing.T, handler Handler) (*Session, net.Conn) {
	t.Helper()
	a, peer := net.Pipe()
	t.Cleanup(func() { a.Close(); peer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	result := make(chan newResult, 1)
	go func() {
		session, err := NewSession(ctx, a, handler)
		result <- newResult{session, err}
	}()
	hello, err := readEnvelope(peer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvelope(peer, hello, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	ready, err := readEnvelope(peer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvelope(peer, ready, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { r.session.Close() })
	return r.session, peer
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !predicate() {
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}

func waitDone(t *testing.T, session *Session) {
	t.Helper()
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not close promptly")
	}
	if session.Err() == nil {
		t.Fatal("closed session has no explicit error")
	}
}

func TestSessionConcurrentDuplex(t *testing.T) {
	var handled atomic.Int64
	handler := func(ctx context.Context, req Request) ([]byte, error) {
		var input domain.TaskChange
		if err := UnmarshalPayload(req.Payload, &input); err != nil {
			return nil, err
		}
		if _, ok := ctx.Deadline(); !ok || req.Target != "task_1" || req.Query["scope"] != "own" ||
			input.Title == nil || *input.Title != "" || input.Priority == nil || *input.Priority != 0 ||
			input.ExpectedRevision != math.MaxInt64 {
			return nil, domain.Fail(domain.Invalid, "lost request metadata")
		}
		handled.Add(1)
		return MarshalPayload(input)
	}
	a, b := sessionPair(t, handler, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	title, priority := "", 0
	payload := mustPayload(t, domain.TaskChange{
		Command: domain.Command{OperationID: "duplex", ExpectedRevision: math.MaxInt64},
		Title:   &title, Priority: &priority,
	})
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for _, session := range []*Session{a, b} {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					result, err := session.Call(ctx, Request{
						Operation: "task.edit", Target: "task_1", Query: map[string]string{"scope": "own"}, Payload: payload,
					})
					if err != nil || !bytes.Equal(result, payload) {
						failures <- fmt.Errorf("duplex call: %v; matching payload=%v", err, bytes.Equal(result, payload))
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if got := handled.Load(); got != 160 {
		t.Fatalf("handled %d calls, want 160", got)
	}
	if a.Err() != nil || b.Err() != nil {
		t.Fatalf("sessions failed: %v, %v", a.Err(), b.Err())
	}
}

func TestSessionIdlePingDoesNotInvokeHandler(t *testing.T) {
	var calls atomic.Int64
	handler := func(context.Context, Request) ([]byte, error) { calls.Add(1); return nil, nil }
	a, b := sessionPair(t, handler, handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < 8; i++ {
		if err := a.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		if err := b.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("liveness invoked a business handler")
	}
}

func TestSessionDottedOperationsWithEmptyQueryPayload(t *testing.T) {
	handler := func(_ context.Context, req Request) ([]byte, error) {
		if req.Target != "" || len(req.Query) != 0 || len(req.Payload) != 0 {
			return nil, domain.Fail(domain.Invalid, "unexpected query metadata")
		}
		return MarshalPayload(domain.LocalStatus{Agent: domain.Agent{ID: req.Operation}, Connected: true})
	}
	client, _ := sessionPair(t, nil, handler)
	for _, operation := range []string{"query.local.status", "query.me", "command.tasks.by_id.change", "identity.owner.challenge"} {
		t.Run(operation, func(t *testing.T) {
			data, err := client.Call(context.Background(), Request{
				Operation: operation, Target: "", Query: map[string]string{}, Payload: nil,
			})
			if err != nil {
				t.Fatal(err)
			}
			var result domain.LocalStatus
			if err := UnmarshalPayload(data, &result); err != nil || result.Agent.ID != operation || !result.Connected {
				t.Fatalf("dotted operation did not reach handler intact: %#v, %v", result, err)
			}
		})
	}
	if client.Err() != nil {
		t.Fatal(client.Err())
	}
}

func TestSessionUnregisteredStatusEncodingFailureIsNotOperationRejection(t *testing.T) {
	type localStatus struct {
		Agent           domain.Agent         `json:"agent"`
		Identity        domain.AgentIdentity `json:"identity"`
		Connected       bool                 `json:"connected"`
		Ready           bool                 `json:"ready"`
		OwnerGeneration int64                `json:"owner_generation,omitempty"`
		Problem         domain.Code          `json:"problem,omitempty"`
	}
	encodingError := make(chan error, 1)
	client, _ := sessionPair(t, nil, func(_ context.Context, req Request) ([]byte, error) {
		if req.Operation != "query.local.status" || req.Target != "" || len(req.Query) != 0 || len(req.Payload) != 0 {
			return nil, domain.Fail(domain.Denied, "unexpected status query")
		}
		data, err := MarshalPayload(localStatus{Agent: domain.Agent{ID: "agent_local"}, Connected: true})
		encodingError <- err
		return data, err
	})
	_, err := client.Call(context.Background(), Request{
		Operation: "query.local.status", Query: map[string]string{},
	})
	requireCode(t, err, domain.Invalid)
	select {
	case localErr := <-encodingError:
		if localErr == nil || localErr.Error() != "invalid: unsupported protobuf payload type" {
			t.Fatalf("unexpected local payload error: %v", localErr)
		}
	case <-time.After(time.Second):
		t.Fatal("status query did not reach the handler")
	}
	if client.Err() != nil {
		t.Fatalf("an operation-validation failure would close the session: %v", client.Err())
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionErrorPropagationIsRedacted(t *testing.T) {
	secret := "private/path/provider-token"
	handler := func(_ context.Context, req Request) ([]byte, error) {
		switch req.Operation {
		case "plain":
			return nil, errors.New(secret)
		case "panic":
			panic(secret)
		case "context":
			return nil, context.Canceled
		default:
			return nil, &domain.Fault{Code: domain.Code(req.Operation), Message: secret, Retryable: true}
		}
	}
	client, _ := sessionPair(t, nil, handler)
	for _, code := range []domain.Code{
		domain.Invalid, domain.NotFound, domain.Denied, domain.Conflict, domain.Prerequisite,
		domain.Unavailable, domain.Provider, domain.Internal, domain.Cancelled,
	} {
		_, err := client.Call(context.Background(), Request{Operation: string(code)})
		requireCode(t, err, code)
		if strings.Contains(err.Error(), secret) || !domain.AsFault(err).Retryable {
			t.Fatalf("unsafe/lossy fault: %v", err)
		}
	}
	for _, operation := range []string{"plain", "panic", "unknown-code", "context"} {
		_, err := client.Call(context.Background(), Request{Operation: operation})
		code := domain.Internal
		if operation == "context" {
			code = domain.Cancelled
		}
		requireCode(t, err, code)
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("sensitive detail crossed the wire: %v", err)
		}
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPublicErrorPreservesApprovedMessageOnly(t *testing.T) {
	handler := func(_ context.Context, req Request) ([]byte, error) {
		fault := &domain.Fault{Code: domain.Denied, Message: "fixture server refusal", Retryable: true}
		switch req.Operation {
		case "public":
			return nil, PublicError(fault)
		case "wrapped":
			return nil, fmt.Errorf("private backend diagnostic: %w", PublicError(fault))
		case "arbitrary":
			return nil, PublicError(errors.New("private backend secret"))
		default:
			fault.Message = "private domain detail"
			return nil, fault
		}
	}
	client, _ := sessionPair(t, nil, handler)
	for _, operation := range []string{"public", "wrapped", "arbitrary", "private"} {
		t.Run(operation, func(t *testing.T) {
			_, err := client.Call(context.Background(), Request{Operation: operation})
			code := domain.Denied
			if operation == "arbitrary" {
				code = domain.Internal
			}
			requireCode(t, err, code)
			switch operation {
			case "public", "wrapped":
				if err.Error() != "permission-denied: fixture server refusal" || !domain.AsFault(err).Retryable {
					t.Fatalf("public fault changed: %v", err)
				}
			default:
				if strings.Contains(err.Error(), "private") {
					t.Fatalf("default redaction leaked backend/domain text: %v", err)
				}
			}
		})
	}
}

func TestSessionPublicErrorTerminalMessage(t *testing.T) {
	client, server := sessionPair(t, nil, nil)
	if err := server.Shutdown(PublicError(domain.Fail(domain.Denied, "public admission refusal"))); err != nil {
		t.Fatal(err)
	}
	waitDone(t, client)
	if client.Err().Error() != "permission-denied: public admission refusal" {
		t.Fatalf("public terminal message changed: %v", client.Err())
	}
}

func TestSessionBlockedCallCancellationAndPing(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	handler := func(ctx context.Context, _ Request) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	}
	client, server := sessionPair(t, nil, handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Call(ctx, Request{Operation: "block"}); result <- err }()
	<-started
	pingCtx, stopPing := context.WithTimeout(context.Background(), time.Second)
	defer stopPing()
	if err := client.Ping(pingCtx); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("local cancellation: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("remote handler cancellation was blocked")
	}
	if elapsed := time.Since(begin); elapsed >= time.Second {
		t.Fatalf("priority cancellation took %v", elapsed)
	}
	if err := client.Ping(pingCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.calls) == 0
	})
	server.mu.Lock()
	running := len(server.incoming)
	server.mu.Unlock()
	if running != 1 {
		t.Fatal("cancellation released a still-running handler's admission slot")
	}
	releaseOnce.Do(func() { close(release) })
}

func TestSessionDeadlineAndMalformedPayload(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	handler := func(ctx context.Context, req Request) ([]byte, error) {
		if req.Operation == "wait" {
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil, ctx.Err()
		}
		var input domain.AgentStatusRequest
		if err := UnmarshalPayload(req.Payload, &input); err != nil {
			return nil, err
		}
		return MarshalPayload(input)
	}
	client, _ := sessionPair(t, nil, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Call(ctx, Request{Operation: "wait"})
	if !errors.Is(err, context.DeadlineExceeded) && (err == nil || domain.AsFault(err).Code != domain.Cancelled) {
		t.Fatalf("deadline did not cancel: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("deadline was not propagated")
	}
	data := append(mustPayload(t, domain.AgentStatusRequest{}), rawBytes(999, nil)...)
	_, err = client.Call(context.Background(), Request{Operation: "status", Payload: data})
	requireCode(t, err, domain.Invalid)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCancellationNeverOvertakesItsCall(t *testing.T) {
	session, peer := rawSession(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := session.Call(ctx, Request{Operation: "queued"}); result <- err }()
	waitFor(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		for _, call := range session.calls {
			return call.state == 1
		}
		return false
	})
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	call, err := readEnvelope(peer)
	if err != nil || call.GetCall() == nil {
		t.Fatalf("first frame must be the call: %v, %v", call, err)
	}
	control, err := readEnvelope(peer)
	if err != nil || control.GetCancel() == nil || control.GetCancel().Id != call.GetCall().Id {
		t.Fatalf("second frame must cancel the same call: %v, %v", control, err)
	}
	if session.Err() != nil {
		t.Fatal(session.Err())
	}
}

func TestSessionPendingFailureOnClose(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			started := make(chan struct{})
			client, server := sessionPair(t, nil, func(ctx context.Context, _ Request) ([]byte, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			result := make(chan error, 1)
			go func() { _, err := client.Call(context.Background(), Request{Operation: "wait"}); result <- err }()
			<-started
			if remote {
				server.Close()
			} else {
				client.Close()
			}
			waitDone(t, client)
			requireCode(t, <-result, domain.Unavailable)
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionShutdownDeniedWithBlockedHandler(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	client, server := sessionPair(t, nil, func(ctx context.Context, _ Request) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	result := make(chan error, 1)
	go func() { _, err := client.Call(context.Background(), Request{Operation: "wait"}); result <- err }()
	<-started
	begin := time.Now()
	if err := server.Shutdown(domain.Fail(domain.Denied, "sensitive identity ban details")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, client)
	requireCode(t, client.Err(), domain.Denied)
	requireCode(t, <-result, domain.Denied)
	if strings.Contains(client.Err().Error(), "sensitive") {
		t.Fatal("terminal fault leaked sensitive detail")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the blocked handler")
	}
	if time.Since(begin) >= time.Second {
		t.Fatal("terminal shutdown was not prompt")
	}
}

func TestSessionHandlerCanShutdownDenied(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	shutdownReturned := make(chan error, 1)
	var server *Session
	handler := func(ctx context.Context, req Request) ([]byte, error) {
		if req.Operation == "deny" {
			err := server.Shutdown(domain.Fail(domain.Denied, "private admission detail"))
			shutdownReturned <- err
			return nil, domain.Fail(domain.Denied, "private handler detail")
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	var client *Session
	client, server = sessionPair(t, nil, handler)
	blockResult := make(chan error, 1)
	go func() { _, err := client.Call(context.Background(), Request{Operation: "block"}); blockResult <- err }()
	<-started
	denyResult := make(chan error, 1)
	go func() { _, err := client.Call(context.Background(), Request{Operation: "deny"}); denyResult <- err }()
	select {
	case err := <-shutdownReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown deadlocked inside its own session handler")
	}
	waitDone(t, client)
	waitDone(t, server)
	requireCode(t, client.Err(), domain.Denied)
	requireCode(t, <-denyResult, domain.Denied)
	requireCode(t, <-blockResult, domain.Denied)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler-initiated shutdown did not cancel other handlers")
	}
}

func TestSessionHandlerShutdownSendsDeniedBeforeCancellationOrEOF(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	shutdownReturned := make(chan error, 1)
	var session *Session
	handler := func(ctx context.Context, req Request) ([]byte, error) {
		if req.Operation == "deny" {
			shutdownReturned <- session.Shutdown(domain.Fail(domain.Denied, "private admission detail"))
			return nil, ctx.Err()
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	var peer net.Conn
	session, peer = rawSession(t, handler)
	if err := writeEnvelope(peer, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{Id: 1, Operation: "block"}}}, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := writeEnvelope(peer, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{Id: 2, Operation: "deny"}}}, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	terminal, err := readEnvelope(peer)
	if err != nil || terminal.GetTerminal().GetCode() != wire.Fault_DENIED {
		t.Fatalf("terminal Denied must precede cancellation results or EOF: %v, %v", terminal, err)
	}
	select {
	case err := <-shutdownReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler-initiated Shutdown did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the blocked handler")
	}
	if frame, err := readEnvelope(peer); err == nil {
		t.Fatalf("unexpected frame after terminal fault: %v", frame)
	}
	waitDone(t, session)
	requireCode(t, session.Err(), domain.Denied)
}

func TestSessionVersionAndSchemaDenial(t *testing.T) {
	for _, version := range []bool{true, false} {
		t.Run(fmt.Sprintf("version=%t", version), func(t *testing.T) {
			a, peer := net.Pipe()
			defer a.Close()
			defer peer.Close()
			result := make(chan error, 1)
			go func() { _, err := NewSession(context.Background(), a, nil); result <- err }()
			hello, err := readEnvelope(peer)
			if err != nil {
				t.Fatal(err)
			}
			if version {
				hello.GetHello().Version++
			} else {
				hello.GetHello().PayloadSchemaSha256 = bytes.Repeat([]byte{0}, 32)
			}
			if err := writeEnvelope(peer, hello, WriteTimeout); err != nil {
				t.Fatal(err)
			}
			terminal, err := readEnvelope(peer)
			if err != nil || terminal.GetTerminal().GetCode() != wire.Fault_DENIED {
				t.Fatalf("missing typed version denial: %v, %v", terminal, err)
			}
			requireCode(t, <-result, domain.Denied)
		})
	}
}

func TestSessionContextClosesHandshakeAndEstablishedSession(t *testing.T) {
	a, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSession(ctx, a, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a, peer = net.Pipe()
	defer peer.Close()
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan newResult, 1)
	go func() { s, err := NewSession(ctx, a, nil); result <- newResult{s, err} }()
	hello, err := readEnvelope(peer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvelope(peer, hello, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	ready, err := readEnvelope(peer)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvelope(peer, ready, WriteTimeout); err != nil {
		t.Fatal(err)
	}
	established := <-result
	if established.err != nil {
		t.Fatal(established.err)
	}
	cancel()
	waitDone(t, established.session)
	if !errors.Is(established.session.Err(), context.Canceled) {
		t.Fatal(established.session.Err())
	}
}

func TestSessionHandlerAdmissionBound(t *testing.T) {
	started := make(chan struct{}, MaxConcurrentHandlers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var active, peak atomic.Int64
	handler := func(_ context.Context, req Request) ([]byte, error) {
		n := active.Add(1)
		for old := peak.Load(); old < n && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer active.Add(-1)
		if req.Operation == "block" {
			started <- struct{}{}
			<-release
		}
		return nil, nil
	}
	client, server := sessionPair(t, nil, handler)
	var cancellations []context.CancelFunc
	results := make(chan error, MaxConcurrentHandlers)
	for i := 0; i < MaxConcurrentHandlers; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancellations = append(cancellations, cancel)
		go func() { _, err := client.Call(ctx, Request{Operation: "block"}); results <- err }()
		<-started
	}
	_, err := client.Call(context.Background(), Request{Operation: "excess"})
	requireCode(t, err, domain.Unavailable)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, cancel := range cancellations {
		cancel()
	}
	for range cancellations {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	_, err = client.Call(context.Background(), Request{Operation: "still-running"})
	requireCode(t, err, domain.Unavailable)
	if peak.Load() != MaxConcurrentHandlers {
		t.Fatalf("handler concurrency peaked at %d", peak.Load())
	}
	releaseOnce.Do(func() { close(release) })
	waitFor(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return len(server.incoming) == 0
	})
	if _, err := client.Call(context.Background(), Request{Operation: "after-release"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPendingAdmissionBound(t *testing.T) {
	session, peer := rawSession(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := make(chan uint64, MaxPendingCalls)
	go func() {
		for {
			envelope, err := readEnvelope(peer)
			if err != nil {
				return
			}
			if call := envelope.GetCall(); call != nil {
				read <- call.Id
			}
		}
	}()
	results := make(chan error, MaxPendingCalls)
	for i := 0; i < MaxPendingCalls; i++ {
		go func() { _, err := session.Call(ctx, Request{Operation: "pending"}); results <- err }()
		<-read
	}
	_, err := session.Call(ctx, Request{Operation: "excess"})
	requireCode(t, err, domain.Unavailable)
	session.Close()
	for i := 0; i < MaxPendingCalls; i++ {
		requireCode(t, <-results, domain.Unavailable)
	}
}

func TestSessionWriterQueueAndStallAreBounded(t *testing.T) {
	session, _ := rawSession(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, MaxQueuedFrames+2)
	go func() { _, err := session.Call(ctx, Request{Operation: "blocked-write"}); results <- err }()
	waitFor(t, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		for _, call := range session.calls {
			return call.state == 1
		}
		return false
	})
	for i := 0; i < MaxQueuedFrames; i++ {
		go func() { _, err := session.Call(ctx, Request{Operation: "queued"}); results <- err }()
		want := i + 1
		waitFor(t, func() bool { return len(session.data) == want })
	}
	begin := time.Now()
	_, err := session.Call(context.Background(), Request{Operation: "saturated"})
	requireCode(t, err, domain.Unavailable)
	cancel()
	for i := 0; i <= MaxQueuedFrames; i++ {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation: %v", err)
		}
	}
	waitDone(t, session)
	if time.Since(begin) >= time.Second {
		t.Fatal("stalled writer outlived the control SLO bound")
	}
}

func sendRaw(conn net.Conn, data []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

func encoded(t *testing.T, envelope *wire.Envelope) []byte {
	t.Helper()
	data, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSessionMalformedFramesAndRequests(t *testing.T) {
	call := &wire.Call{Id: 1, Operation: "valid"}
	baseCall, err := proto.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"unknown envelope":                rawBytes(99, nil),
		"invalid protobuf":                {0xff},
		"unknown protected request field": rawBytes(3, append(bytes.Clone(baseCall), rawBytes(99, nil)...)),
		"duplicate call field":            rawBytes(3, append(bytes.Clone(baseCall), rawVarint(1, 1)...)),
		"multiple variants": append(
			encoded(t, &wire.Envelope{Body: &wire.Envelope_Ping{Ping: &wire.Ping{Id: 1}}}),
			encoded(t, &wire.Envelope{Body: &wire.Envelope_Cancel{Cancel: &wire.Cancel{Id: 1}}})...),
		"HTTP operation":     encoded(t, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{Id: 1, Operation: "/v1/tasks"}}}),
		"HTTP target":        encoded(t, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{Id: 1, Operation: "task.get", Target: "/v1/tasks"}}}),
		"missing outcome":    encoded(t, &wire.Envelope{Body: &wire.Envelope_Result{Result: &wire.Result{Id: 1}}}),
		"unknown result":     encoded(t, &wire.Envelope{Body: &wire.Envelope_Result{Result: &wire.Result{Id: 1, Outcome: &wire.Result_Payload{}}}}),
		"future cancel":      encoded(t, &wire.Envelope{Body: &wire.Envelope_Cancel{Cancel: &wire.Cancel{Id: 1}}}),
		"unknown pong":       encoded(t, &wire.Envelope{Body: &wire.Envelope_Pong{Pong: &wire.Pong{Id: 1}}}),
		"unspecified fault":  encoded(t, &wire.Envelope{Body: &wire.Envelope_Terminal{Terminal: &wire.Fault{}}}),
		"unknown fault enum": rawBytes(8, rawVarint(1, 99)),
		"duplicate query": encoded(t, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{
			Id: 1, Operation: "query", Query: []*wire.Metadata{{Key: "key"}, {Key: "key"}},
		}}}),
		"request too large": encoded(t, &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{
			Id: 1, Operation: "large", Payload: make([]byte, MaxRequestBytes+1),
		}}}),
	} {
		t.Run(name, func(t *testing.T) {
			session, peer := rawSession(t, nil)
			if err := sendRaw(peer, data); err != nil {
				t.Fatal(err)
			}
			if name != "unspecified fault" {
				terminal, err := readEnvelope(peer)
				if err != nil || terminal.GetTerminal().GetCode() != wire.Fault_INVALID {
					t.Fatalf("malformed input did not receive explicit invalid fault: %v, %v", terminal, err)
				}
			}
			waitDone(t, session)
			requireCode(t, session.Err(), domain.Invalid)
		})
	}
}

func TestSessionInvalidLengthsAndTruncation(t *testing.T) {
	for name, parts := range map[string][][]byte{
		"empty":           {{0, 0, 0, 0}},
		"oversized":       {{2, 0, 64, 1}},
		"partial header":  {{0, 1}},
		"partial payload": {{0, 0, 0, 10}, {1, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			session, peer := rawSession(t, nil)
			for _, part := range parts {
				if _, err := peer.Write(part); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(name, "partial") {
				peer.Close()
			} else {
				terminal, err := readEnvelope(peer)
				if err != nil || terminal.GetTerminal().GetCode() != wire.Fault_INVALID {
					t.Fatalf("length denial: %v, %v", terminal, err)
				}
			}
			waitDone(t, session)
			requireCode(t, session.Err(), domain.Invalid)
		})
	}
}

func TestSessionDuplicateCorrelationIDs(t *testing.T) {
	for _, ping := range []bool{false, true} {
		t.Run(fmt.Sprintf("ping=%t", ping), func(t *testing.T) {
			session, peer := rawSession(t, nil)
			envelope := &wire.Envelope{Body: &wire.Envelope_Call{Call: &wire.Call{Id: 5, Operation: "no-handler"}}}
			if ping {
				envelope = &wire.Envelope{Body: &wire.Envelope_Ping{Ping: &wire.Ping{Id: 5}}}
			}
			for i := 0; i < 2; i++ {
				if err := writeEnvelope(peer, envelope, WriteTimeout); err != nil {
					t.Fatal(err)
				}
				result, err := readEnvelope(peer)
				if err != nil {
					t.Fatal(err)
				}
				if i == 1 && result.GetTerminal().GetCode() != wire.Fault_INVALID {
					t.Fatalf("duplicate ID accepted: %v", result)
				}
			}
			waitDone(t, session)
			requireCode(t, session.Err(), domain.Invalid)
		})
	}
}

func TestSessionResponseBoundExceedsRequestBound(t *testing.T) {
	payload := mustPayload(t, bytes.Repeat([]byte{7}, 2*MaxRequestBytes))
	client, _ := sessionPair(t, nil, func(_ context.Context, req Request) ([]byte, error) {
		if req.Operation == "oversized" {
			return make([]byte, MaxPayloadBytes+1), nil
		}
		return payload, nil
	})
	result, err := client.Call(context.Background(), Request{Operation: "large-result"})
	if err != nil || !bytes.Equal(result, payload) {
		t.Fatalf("bounded large response: %v", err)
	}
	_, err = client.Call(context.Background(), Request{Operation: "oversized"})
	requireCode(t, err, domain.Invalid)
	_, err = client.Call(context.Background(), Request{Operation: "large-request", Payload: make([]byte, MaxRequestBytes+1)})
	requireCode(t, err, domain.Invalid)
}
