package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/message/wire"
	"github.com/thtpark/fdn/internal/pki"
	"google.golang.org/protobuf/proto"
)

func TestMessageFleetProtobufPushFixture(t *testing.T) {
	// Functional CI uses arbitrary hardware and too few samples for fleet SLOs.
	// The explicit 1,000-identity profile retains every approved latency gate.
	runMessageFleet(t, 8, 2, false)
}

func TestMessageFleet1000ProtobufPushAcceptance(t *testing.T) {
	if os.Getenv("FDN_MESSAGE_FLEET") != "1" {
		t.Skip("set FDN_MESSAGE_FLEET=1 for the isolated 1,000-identity S7 acceptance profile")
	}
	runMessageFleet(t, 1000, 128, true)
}

type messageFleetDelivery struct {
	update domain.Update
	at     time.Time
}

type messageFleetProbe struct {
	cancel         context.CancelFunc
	ready          chan struct{}
	ack            chan struct{}
	local          chan error
	started, ended time.Time
	sawCancel      bool
	err            error
}

type messageFleetCall struct {
	operation string
	watch     bool
	probe     *messageFleetProbe
}

type messageFleetPeer struct {
	index   int
	agent   domain.Agent
	client  *Client
	dials   atomic.Int64
	mu      sync.Mutex
	calls   map[uint64]messageFleetCall
	probe   *messageFleetProbe
	cursor  string
	initial string
	pending int
	last    int64
	updates map[string]messageFleetDelivery
}

type messageFleet struct {
	t              *testing.T
	server         *Server
	ctx            context.Context
	cancel         context.CancelFunc
	watchCtx       context.Context
	stopWatches    context.CancelFunc
	listener       net.Listener
	acceptDone     chan struct{}
	peers          []*messageFleetPeer
	samples        int
	serveWG        sync.WaitGroup
	watchWG        sync.WaitGroup
	probeWG        sync.WaitGroup
	closeOnce      sync.Once
	closing        atomic.Bool
	accepted       atomic.Int64
	connected      atomic.Int64
	pending        atomic.Int64
	handlers       atomic.Int64
	deliveries     atomic.Int64
	protobufInputs atomic.Int64
	protobufOutput atomic.Int64
	mu             sync.Mutex
	connections    map[net.Conn]struct{}
	problems       []error
}

func runMessageFleet(t *testing.T, identities, samples int, qualify bool) {
	t.Helper()
	if identities < 3*samples || samples < 1 {
		t.Fatal("fleet profile needs separate senders, recipients, and cancellation subscribers")
	}
	messageFleetResources(t, "before")
	f := newMessageFleet(t, identities, samples)
	warmStart := time.Now()
	if err := messageFleetParallel(identities, 16, func(index int) error {
		p := f.peers[index]
		me, err := Call[domain.Agent](f.ctx, p.client, "GET", "/v1/me", nil)
		if err != nil || me.ID != p.agent.ID {
			return fmt.Errorf("identity %d principal warmup: %v", index, err)
		}
		page, err := Call[domain.Page[domain.Update]](f.ctx, p.client, "GET", "/v1/updates", nil)
		if err != nil || len(page.Items) != 0 || page.NextCursor == "" {
			return fmt.Errorf("identity %d initial watermark: %v", index, err)
		}
		p.cursor, p.initial = page.NextCursor, page.NextCursor
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for start := 0; start < identities; start += 16 {
		end := min(start+16, identities)
		for _, p := range f.peers[start:end] {
			f.watchWG.Go(func() { f.watch(p) })
		}
		f.await("bounded subscription warmup", func() bool {
			return f.pending.Load() == int64(end) && f.handlers.Load() == int64(end) &&
				f.server.MessageCounts()["query.updates"] == uint64(identities+end)
		})
	}
	f.await("all admitted connections", func() bool {
		return f.connected.Load() == int64(identities) && f.accepted.Load() == int64(identities)
	})
	f.quiet(200 * time.Millisecond)
	warmCounts := f.server.MessageCounts()
	t.Logf("warmup identities=%d concurrency<=16 elapsed=%s initialized=%d pending=%d connected=%d counts=%s",
		identities, time.Since(warmStart).Round(time.Millisecond), identities, f.pending.Load(),
		f.connected.Load(), messageFleetCounts(warmCounts))
	messageFleetResources(t, "warm-idle")

	idlePings := make([]time.Duration, identities)
	if err := messageFleetParallel(identities, 32, func(index int) error {
		started := time.Now()
		err := f.peers[index].client.Ping(f.ctx)
		idlePings[index] = time.Since(started)
		return err
	}); err != nil {
		t.Fatal("idle transport ping failed:", err)
	}
	if !messageFleetEqualCounts(warmCounts, f.server.MessageCounts()) {
		t.Fatal("idle transport pings incremented business/discovery handlers")
	}
	f.assertPending(identities)
	controlLimit := time.Duration(0)
	updateP95, updateP99 := time.Duration(0), time.Duration(0)
	if qualify {
		controlLimit = time.Second
		updateP95, updateP99 = 250*time.Millisecond, time.Second
	}
	messageFleetQuantiles(t, "idle_transport_ping", idlePings, 0, controlLimit)

	if err := messageFleetParallel(samples, 16, func(index int) error {
		p := f.peers[2*samples+index]
		ctx, cancel := context.WithCancel(f.watchCtx)
		probe := &messageFleetProbe{cancel: cancel, ready: make(chan struct{}),
			ack: make(chan struct{}), local: make(chan error, 1)}
		p.mu.Lock()
		p.probe = probe
		cursor := p.cursor
		p.mu.Unlock()
		f.probeWG.Go(func() {
			_, err := Call[domain.Page[domain.Update]](ctx, p.client, "GET",
				"/v1/updates?wait=true&limit=1&since="+url.QueryEscape(cursor), nil)
			probe.local <- err
		})
		select {
		case <-probe.ready:
			return nil
		case <-f.ctx.Done():
			return fmt.Errorf("cancellation subscription %d was not admitted", index)
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.await("all cancellation handlers admitted", func() bool {
		return f.handlers.Load() == int64(identities+samples) &&
			f.server.MessageCounts()["query.updates"] == uint64(2*identities+samples)
	})
	f.assertPending(identities)
	windowCounts := f.server.MessageCounts()

	type mutation struct {
		request domain.MessageRequest
		message domain.Message
		started time.Time
		ack     time.Duration
	}
	mutations := make([]mutation, samples)
	cancellations := make([]time.Duration, samples)
	controlPings := make([]time.Duration, samples)
	workErrors := make(chan error, 3)
	start := make(chan struct{})
	var work sync.WaitGroup
	windowStart := time.Now()
	work.Go(func() {
		<-start
		workErrors <- messageFleetParallel(samples, 8, func(index int) error {
			m := &mutations[index]
			m.request = domain.MessageRequest{
				Command:     domain.Command{OperationID: domain.NewID("op")},
				RecipientID: f.peers[samples+index].agent.ID,
				Body:        fmt.Sprintf("isolated synthetic fleet payload %d", index),
				Origin:      "human", Kind: "direct", Urgent: true,
			}
			m.started = time.Now()
			sent, err := Call[domain.Message](f.ctx, f.peers[index].client, "POST", "/v1/messages", m.request)
			m.ack = time.Since(m.started)
			m.message = sent
			if err != nil {
				return fmt.Errorf("committed mutation %d: %w", index, err)
			}
			if sent.ID == "" || sent.SenderID != f.peers[index].agent.ID ||
				sent.RecipientID != m.request.RecipientID || sent.Body != m.request.Body || sent.ReadAt != 0 {
				return fmt.Errorf("mutation %d returned an incorrect private domain result", index)
			}
			return nil
		})
	})
	work.Go(func() {
		<-start
		workErrors <- messageFleetParallel(samples, 16, func(index int) error {
			p := f.peers[2*samples+index]
			p.mu.Lock()
			probe := p.probe
			probe.started = time.Now()
			p.mu.Unlock()
			probe.cancel()
			select {
			case err := <-probe.local:
				if domain.AsFault(err).Code != domain.Cancelled {
					return fmt.Errorf("local cancellation %d: %v", index, err)
				}
			case <-f.ctx.Done():
				return fmt.Errorf("local cancellation %d did not return", index)
			}
			// Local context cancellation alone is not a remote cancellation sample.
			select {
			case <-probe.ack:
				p.mu.Lock()
				defer p.mu.Unlock()
				if probe.err != nil {
					return probe.err
				}
				cancellations[index] = probe.ended.Sub(probe.started)
				return nil
			case <-f.ctx.Done():
				return fmt.Errorf("remote cancellation %d was not acknowledged", index)
			}
		})
	})
	work.Go(func() {
		<-start
		workErrors <- messageFleetParallel(samples, 16, func(index int) error {
			started := time.Now()
			err := f.peers[2*samples+index].client.Ping(f.ctx)
			controlPings[index] = time.Since(started)
			return err
		})
	})
	close(start)
	work.Wait()
	close(workErrors)
	for err := range workErrors {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	f.await("committed update delivery and immediate resubscription", func() bool {
		return f.deliveries.Load() == int64(2*samples) && f.pending.Load() == int64(identities) &&
			f.handlers.Load() == int64(identities)
	})
	elapsed := time.Since(windowStart)
	f.assertPending(identities)
	timedCounts := f.server.MessageCounts()
	for name, count := range windowCounts {
		want := count
		switch name {
		case "command.messages":
			want += uint64(samples)
		case "query.updates":
			want += uint64(2 * samples)
		}
		if timedCounts[name] != want {
			t.Errorf("unexpected timed business/discovery traffic %s: got %d want %d", name, timedCounts[name], want)
		}
	}
	delays := make([]time.Duration, 0, 2*samples)
	commandAcks := make([]time.Duration, samples)
	for index, m := range mutations {
		commandAcks[index] = m.ack
		var sequence int64
		for _, peerIndex := range []int{index, samples + index} {
			p := f.peers[peerIndex]
			p.mu.Lock()
			delivery, ok := p.updates[m.message.ID]
			n := len(p.updates)
			p.mu.Unlock()
			if !ok || n != 1 || delivery.at.Before(m.started) || delivery.update.Topic != "message" ||
				!delivery.update.Urgent || delivery.update.TaskID != "" || delivery.update.ProjectID != "" {
				t.Fatalf("identity %d did not receive exactly its committed scoped update", peerIndex)
			}
			if sequence != 0 && sequence != delivery.update.Sequence {
				t.Fatal("sender and recipient did not observe the same committed update")
			}
			sequence = delivery.update.Sequence
			delays = append(delays, delivery.at.Sub(m.started))
		}
	}
	for _, p := range f.peers[2*samples:] {
		p.mu.Lock()
		untouched := len(p.updates) == 0 && p.pending == 1
		p.mu.Unlock()
		if !untouched {
			t.Fatalf("unrelated identity %d did not remain blocked", p.index)
		}
	}
	t.Logf("steady_state identities=%d connected=%d mutations=%d update_deliveries=%d remote_cancellations=%d control_pings=%d unrelated_blocked=%d pending_after=%d window=%s errors=0",
		identities, f.connected.Load(), samples, len(delays), len(cancellations), len(controlPings),
		identities-2*samples, f.pending.Load(), elapsed.Round(time.Microsecond))
	t.Logf("timed_counts=%s (only committed commands and post-delivery resubscriptions; no periodic business discovery)",
		messageFleetCounts(messageFleetCountDelta(timedCounts, windowCounts)))
	messageFleetQuantiles(t, "submission_to_delivered_scoped_update", delays, updateP95, updateP99)
	messageFleetQuantiles(t, "submission_to_committed_command_ack", commandAcks, 0, 0)
	messageFleetQuantiles(t, "remote_subscription_cancel_ack", cancellations, 0, controlLimit)
	messageFleetQuantiles(t, "mixed_priority_transport_ping", controlPings, 0, controlLimit)
	messageFleetResources(t, "steady-state")

	first := mutations[0]
	replayed, err := Call[domain.Message](f.ctx, f.peers[0].client, "POST", "/v1/messages", first.request)
	if err != nil || replayed.ID != first.message.ID {
		t.Fatal("exact committed operation replay changed message identity:", err)
	}
	for _, index := range []int{0, samples} {
		page, err := Call[domain.Page[domain.Update]](f.ctx, f.peers[index].client, "GET",
			"/v1/updates?since="+url.QueryEscape(f.peers[index].initial), nil)
		if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != first.message.ID {
			t.Fatalf("party %d replay was missing, duplicated, or unrelated: %v", index, err)
		}
		shown, err := Call[domain.Message](f.ctx, f.peers[index].client, "GET", Path("messages", first.message.ID), nil)
		if err != nil || shown.Body != first.request.Body || shown.ReadAt != 0 {
			t.Fatalf("party %d private message/read receipt changed: %v", index, err)
		}
	}
	unrelated := f.peers[2*samples]
	if _, err := Call[domain.Message](f.ctx, unrelated.client, "GET", Path("messages", first.message.ID), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("unrelated identity could read the private committed payload:", err)
	}
	if _, err := Call[domain.Page[domain.Update]](f.ctx, unrelated.client, "GET",
		"/v1/updates?since="+url.QueryEscape(f.peers[samples].initial), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("unrelated identity could replay a different actor's cursor:", err)
	}
	f.quiet(150 * time.Millisecond)
	if f.deliveries.Load() != int64(2*samples) {
		t.Fatal("immutable command replay generated a duplicate pushed update")
	}
	f.assertPending(identities)
	if f.protobufInputs.Load() != int64(samples+1) || f.protobufOutput.Load() != int64(samples+1) {
		t.Fatal("mutations/results were not observed as registered non-JSON protobuf DTOs")
	}
	t.Logf("replay_privacy exact_operation=1 party_feed_replays=2 expected_denials=2 implicit_read_receipts=0 duplicate_deliveries=0 protobuf_commands=%d protobuf_message_results=%d total_counts=%s",
		f.protobufInputs.Load(), f.protobufOutput.Load(), messageFleetCounts(f.server.MessageCounts()))
	t.Log("boundary: test-owned TLS 1.3 loopback, distinct ephemeral mTLS certificate identities bound server-side to enrolled logical principals, real isolated SQLite and protobuf domain handlers; no HTTP, SSH processes/accounts, peer-UID/native owner authentication, provider inference, deployment, or native fleet capacity qualification")
	f.close()
	messageFleetResources(t, "owned-connections-and-callers-closed")
}

func newMessageFleet(t *testing.T, identities, samples int) *messageFleet {
	t.Helper()
	a := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	watchCtx, stopWatches := context.WithCancel(ctx)
	f := &messageFleet{t: t, server: a.server, ctx: ctx, cancel: cancel, watchCtx: watchCtx,
		stopWatches: stopWatches, samples: samples, acceptDone: make(chan struct{}), connections: make(map[net.Conn]struct{})}
	t.Cleanup(f.close)
	// Observe the unchanged domain handler's lifetime, including remote cancellation cleanup.
	updates := f.server.operations["query.updates"]
	handle := updates.handle
	updates.handle = func(r *operationRequest, actor domain.Agent, data []byte) ([]byte, error) {
		f.handlers.Add(1)
		defer f.handlers.Add(-1)
		return handle(r, actor, data)
	}
	agents := []domain.Agent{a.founder, a.delegate, a.target}
	for index := len(agents); index < identities; index++ {
		token := domain.NewToken()
		if _, err := a.store.Invite(ctx, a.founder.ID, domain.InvitationRequest{
			Command:   domain.Command{OperationID: domain.NewID("op")},
			TokenHash: domain.Hash(token), ExpiresAt: time.Now().Unix() + 600,
		}); err != nil {
			t.Fatal(err)
		}
		agent, err := a.store.Enroll(ctx, domain.EnrollRequest{
			Command: domain.Command{OperationID: domain.NewID("op")}, Name: fmt.Sprintf("isolated fleet %04d", index),
			InvitationToken: token, CredentialHash: domain.Hash(domain.NewToken()),
		})
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, agent)
	}
	now := time.Now()
	bundle, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	serverPair, err := tls.X509KeyPair(bundle.Cert, bundle.Key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle.CA) {
		t.Fatal("fixture CA could not be parsed")
	}
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, NextProtos: []string{MessageALPN}}
	certificates := make(map[string][]byte, identities)
	clientTLS := make([]*tls.Config, identities)
	for index, agent := range agents {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		certPEM, err := pki.IssueClient(bundle.CA, bundle.CAKey, public, domain.CertificateIdentity{
			InstanceID: "instance_fleet_fixture", AgentID: agent.ID, Purpose: "device", Generation: 1,
		}, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		key, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
		if err != nil {
			t.Fatal(err)
		}
		certificates[agent.ID] = pair.Certificate[0]
		clientTLS[index] = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost",
			Certificates: []tls.Certificate{pair}, NextProtos: []string{MessageALPN}}
		f.peers = append(f.peers, &messageFleetPeer{index: index, agent: agent,
			calls: make(map[uint64]messageFleetCall), updates: make(map[string]messageFleetDelivery)})
	}
	f.listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*messageFleetPeer, identities)
	for index, p := range f.peers {
		byID[p.agent.ID] = p
		dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: clientTLS[index]}
		p.client = NewMessageClient("fixture://isolated-mtls-fleet", func(ctx context.Context) (net.Conn, error) {
			if p.dials.Add(1) != 1 {
				return nil, errors.New("fleet fixture forbids reconnects from contaminating steady-state measurements")
			}
			conn, err := dialer.DialContext(ctx, "tcp4", f.listener.Addr().String())
			if err != nil {
				return nil, err
			}
			return &messageFleetReadConn{Conn: conn, observe: func(envelope *wire.Envelope) { f.result(p, envelope) }}, nil
		})
	}
	go func() {
		defer close(f.acceptDone)
		for {
			conn, err := f.listener.Accept()
			if err != nil {
				if !f.closing.Load() {
					f.problem(fmt.Errorf("owned loopback accept: %w", err))
				}
				return
			}
			f.mu.Lock()
			f.connections[conn] = struct{}{}
			f.mu.Unlock()
			f.serveWG.Go(func() {
				defer conn.Close()
				defer func() {
					f.mu.Lock()
					delete(f.connections, conn)
					f.mu.Unlock()
				}()
				secure := tls.Server(conn, serverTLS)
				handshake, stop := context.WithTimeout(ctx, 5*time.Second)
				err := secure.HandshakeContext(handshake)
				stop()
				if err != nil {
					if !f.closing.Load() {
						f.problem(fmt.Errorf("owned mutual TLS handshake: %w", err))
					}
					return
				}
				state := secure.ConnectionState()
				if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) != 1 || state.NegotiatedProtocol != MessageALPN {
					f.problem(errors.New("fixture requires verified single-leaf mTLS and message ALPN"))
					return
				}
				identity, err := pki.ParseIdentity(state.PeerCertificates[0])
				p := byID[identity.AgentID]
				if err != nil || p == nil || identity.InstanceID != "instance_fleet_fixture" || identity.Purpose != "device" ||
					identity.Generation != 1 || !bytes.Equal(certificates[identity.AgentID], state.PeerCertificates[0].Raw) {
					f.problem(errors.New("certificate was not bound to a fixture-owned logical principal"))
					return
				}
				f.accepted.Add(1)
				f.connected.Add(1)
				defer f.connected.Add(-1)
				observed := &messageFleetReadConn{Conn: secure, observe: func(envelope *wire.Envelope) { f.request(p, envelope) }}
				err = f.server.ServeTrustedMessages(ctx, observed, identity.AgentID)
				if !f.closing.Load() {
					f.problem(fmt.Errorf("identity %d session ended before cleanup: %v", p.index, err))
				}
			})
		}
	}()
	return f
}

func (f *messageFleet) watch(p *messageFleetPeer) {
	for {
		p.mu.Lock()
		cursor := p.cursor
		p.mu.Unlock()
		page, err := Call[domain.Page[domain.Update]](f.watchCtx, p.client, "GET",
			"/v1/updates?wait=true&limit=200&since="+url.QueryEscape(cursor), nil)
		delivered := time.Now()
		if err != nil {
			if f.watchCtx.Err() == nil {
				f.problem(fmt.Errorf("identity %d update subscription: %w", p.index, err))
			}
			return
		}
		if len(page.Items) == 0 || page.NextCursor == "" {
			f.problem(fmt.Errorf("identity %d caught-up subscription returned an empty page", p.index))
			return
		}
		p.mu.Lock()
		for _, update := range page.Items {
			if p.index >= 2*f.samples || update.Sequence <= p.last || update.SourceID == "" ||
				update.Topic != "message" || !update.Urgent {
				f.problem(fmt.Errorf("identity %d received an unrelated, duplicated, or invalid scoped update", p.index))
			}
			if _, exists := p.updates[update.SourceID]; exists {
				f.problem(fmt.Errorf("identity %d received a duplicate source update", p.index))
			}
			p.updates[update.SourceID] = messageFleetDelivery{update: update, at: delivered}
			p.last = update.Sequence
			f.deliveries.Add(1)
		}
		p.cursor = page.NextCursor
		p.mu.Unlock()
	}
}

func (f *messageFleet) request(p *messageFleetPeer, envelope *wire.Envelope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel := envelope.GetCancel(); cancel != nil {
		call, ok := p.calls[cancel.Id]
		if ok && call.probe != nil {
			call.probe.sawCancel = true
		}
		return
	}
	call := envelope.GetCall()
	if call == nil {
		return
	}
	record := messageFleetCall{operation: call.Operation}
	if call.Operation == "query.updates" {
		var wait, limit string
		for _, item := range call.Query {
			switch item.Key {
			case "wait":
				wait = item.Value
			case "limit":
				limit = item.Value
			}
		}
		if wait == "true" {
			if limit == "1" {
				record.probe = p.probe
				if record.probe == nil {
					f.problem(errors.New("unexpected cancellation probe"))
				} else {
					close(record.probe.ready)
				}
			} else {
				record.watch = true
				p.pending++
				f.pending.Add(1)
				if p.pending != 1 {
					f.problem(fmt.Errorf("identity %d has duplicate baseline subscriptions", p.index))
				}
			}
		}
	}
	if call.Operation == "command.messages" {
		var input domain.MessageRequest
		if json.Valid(call.Payload) || message.UnmarshalPayload(call.Payload, &input) != nil || input.Body == "" {
			f.problem(errors.New("message command body was not a real registered protobuf DTO"))
		}
		f.protobufInputs.Add(1)
	}
	p.calls[call.Id] = record
}

func (f *messageFleet) result(p *messageFleetPeer, envelope *wire.Envelope) {
	result := envelope.GetResult()
	if result == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	call, ok := p.calls[result.Id]
	if !ok {
		f.problem(errors.New("result did not match a fixture-observed protobuf call"))
		return
	}
	delete(p.calls, result.Id)
	if call.watch {
		p.pending--
		f.pending.Add(-1)
	}
	if probe := call.probe; probe != nil {
		probe.ended = time.Now()
		if probe.started.IsZero() || !probe.sawCancel || result.GetFault().GetCode() != wire.Fault_CANCELLED {
			probe.err = errors.New("subscription did not acknowledge its priority Cancel frame with a remote CANCELLED result")
		}
		close(probe.ack)
	}
	if call.operation == "command.messages" && result.GetFault() == nil {
		var output domain.Message
		if json.Valid(result.GetPayload()) || message.UnmarshalPayload(result.GetPayload(), &output) != nil || output.ID == "" {
			f.problem(errors.New("message command result was not a real registered protobuf DTO"))
		}
		f.protobufOutput.Add(1)
	}
}

// Read-side observation occurs before the real session consumes each complete
// plaintext frame, after TLS verification/decryption. Requests remain unchanged.
type messageFleetReadConn struct {
	net.Conn
	buffer  []byte
	observe func(*wire.Envelope)
}

func (c *messageFleetReadConn) Read(dst []byte) (int, error) {
	n, err := c.Conn.Read(dst)
	c.buffer = append(c.buffer, dst[:n]...)
	for len(c.buffer) >= 4 {
		size := int(binary.BigEndian.Uint32(c.buffer[:4]))
		if size <= 0 || size > message.MaxFrameBytes {
			return n, errors.New("invalid fixture-observed protobuf frame size")
		}
		if len(c.buffer) < size+4 {
			break
		}
		var envelope wire.Envelope
		if parseErr := proto.Unmarshal(c.buffer[4:4+size], &envelope); parseErr != nil {
			return n, errors.New("invalid fixture-observed protobuf envelope")
		}
		c.observe(&envelope)
		c.buffer = c.buffer[4+size:]
	}
	if len(c.buffer) == 0 {
		c.buffer = nil
	}
	return n, err
}

func (f *messageFleet) problem(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.problems) < 16 {
		f.problems = append(f.problems, err)
	}
}

func (f *messageFleet) check() {
	f.t.Helper()
	f.mu.Lock()
	err := errors.Join(f.problems...)
	f.mu.Unlock()
	if err != nil {
		f.t.Fatal("fleet errors:", err)
	}
}

func (f *messageFleet) await(label string, ready func() bool) {
	f.t.Helper()
	until := time.NewTimer(10 * time.Second)
	defer until.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		f.check()
		if ready() {
			return
		}
		select {
		case <-until.C:
			f.t.Fatalf("%s timed out: connected=%d pending=%d active_update_handlers=%d deliveries=%d counts=%s",
				label, f.connected.Load(), f.pending.Load(), f.handlers.Load(), f.deliveries.Load(),
				messageFleetCounts(f.server.MessageCounts()))
		case <-f.ctx.Done():
			f.t.Fatal(label, f.ctx.Err())
		case <-tick.C:
		}
	}
}

func (f *messageFleet) quiet(duration time.Duration) {
	f.t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		f.t.Fatal(f.ctx.Err())
	}
	f.check()
}

func (f *messageFleet) assertPending(identities int) {
	f.t.Helper()
	f.check()
	if f.connected.Load() != int64(identities) || f.accepted.Load() != int64(identities) ||
		f.pending.Load() != int64(identities) {
		f.t.Fatalf("full fleet was not continuously admitted/subscribed: connected=%d accepted=%d pending=%d",
			f.connected.Load(), f.accepted.Load(), f.pending.Load())
	}
	for _, p := range f.peers {
		p.mu.Lock()
		pending := p.pending
		p.mu.Unlock()
		if p.dials.Load() != 1 || pending != 1 {
			f.t.Fatalf("identity %d reconnected or lost its baseline watch: dials=%d pending=%d", p.index, p.dials.Load(), pending)
		}
	}
}

func (f *messageFleet) close() {
	f.closeOnce.Do(func() {
		f.closing.Store(true)
		f.stopWatches()
		for _, p := range f.peers {
			p.mu.Lock()
			probe := p.probe
			p.mu.Unlock()
			if probe != nil {
				probe.cancel()
			}
		}
		callers := make(chan struct{})
		go func() {
			f.watchWG.Wait()
			f.probeWG.Wait()
			close(callers)
		}()
		select {
		case <-callers:
		case <-time.After(5 * time.Second):
			f.t.Error("owned fleet callers did not stop after cancellation")
		}
		deadline := time.Now().Add(5 * time.Second)
		for (f.pending.Load() != 0 || f.handlers.Load() != 0) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		f.cancel()
		if f.listener != nil {
			_ = f.listener.Close()
			<-f.acceptDone
		}
		f.mu.Lock()
		connections := make([]net.Conn, 0, len(f.connections))
		for conn := range f.connections {
			connections = append(connections, conn)
		}
		f.mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		for _, p := range f.peers {
			if p.client != nil {
				p.client.Close()
			}
		}
		servers := make(chan struct{})
		go func() {
			f.serveWG.Wait()
			close(servers)
		}()
		select {
		case <-servers:
		case <-time.After(5 * time.Second):
			f.t.Error("owned fleet server sessions did not close")
		}
		select {
		case <-callers:
		case <-time.After(time.Second):
			f.t.Error("owned fleet callers remained after forced socket close")
		}
		if f.handlers.Load() != 0 || f.connected.Load() != 0 {
			f.t.Errorf("owned fleet leaked active handlers/connections: handlers=%d connected=%d",
				f.handlers.Load(), f.connected.Load())
		}
		f.t.Logf("cleanup owned_connections=%d active_update_handlers=%d pending_wire_watches=%d",
			f.connected.Load(), f.handlers.Load(), f.pending.Load())
	})
}

func messageFleetParallel(total, concurrency int, fn func(int) error) error {
	jobs := make(chan int, total)
	for index := range total {
		jobs <- index
	}
	close(jobs)
	problems := make(chan error, total)
	var workers sync.WaitGroup
	for range min(total, concurrency) {
		workers.Go(func() {
			for index := range jobs {
				if err := fn(index); err != nil {
					problems <- err
				}
			}
		})
	}
	workers.Wait()
	close(problems)
	var all []error
	for err := range problems {
		all = append(all, err)
	}
	return errors.Join(all...)
}

func messageFleetQuantiles(t *testing.T, label string, values []time.Duration, p95Limit, p99Limit time.Duration) {
	t.Helper()
	if len(values) == 0 {
		t.Fatal(label, "has no latency samples")
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	if sorted[0] <= 0 {
		t.Fatal(label, "contains an absent or nonpositive latency sample")
	}
	quantile := func(percent int) time.Duration { return sorted[(len(sorted)*percent+99)/100-1] }
	p50, p95, p99 := quantile(50), quantile(95), quantile(99)
	t.Logf("%s n=%d p50=%s p95=%s p99=%s max=%s", label, len(sorted),
		p50.Round(time.Microsecond), p95.Round(time.Microsecond), p99.Round(time.Microsecond),
		sorted[len(sorted)-1].Round(time.Microsecond))
	if p95Limit != 0 && p95 > p95Limit {
		t.Errorf("%s p95 %s exceeds approved %s", label, p95, p95Limit)
	}
	if p99Limit != 0 && p99 > p99Limit {
		t.Errorf("%s p99 %s exceeds approved %s", label, p99, p99Limit)
	}
}

func messageFleetEqualCounts(left, right map[string]uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func messageFleetCountDelta(after, before map[string]uint64) map[string]uint64 {
	delta := make(map[string]uint64)
	for name, value := range after {
		delta[name] = value - before[name]
	}
	return delta
}

func messageFleetCounts(counts map[string]uint64) string {
	var names []string
	for name, value := range counts {
		if value != 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for index, name := range names {
		names[index] = name + "=" + strconv.FormatUint(counts[name], 10)
	}
	return strings.Join(names, ",")
}

func messageFleetResources(t *testing.T, phase string) {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var limit syscall.Rlimit
	fdLimit := "unavailable"
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit) == nil {
		fdLimit = strconv.FormatUint(limit.Cur, 10)
	}
	fdCount := "unavailable"
	if tool, err := exec.LookPath("lsof"); err == nil {
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		output, err := exec.CommandContext(ctx, tool, "-a", "-p", strconv.Itoa(os.Getpid()), "-Ff").Output()
		stop()
		if err == nil {
			count := 0
			for _, line := range strings.Split(string(output), "\n") {
				if len(line) > 1 && line[0] == 'f' && line[1] >= '0' && line[1] <= '9' {
					count++
				}
			}
			fdCount = strconv.Itoa(count)
		}
	}
	rss := "unavailable"
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	if output, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output(); err == nil {
		rss = strings.TrimSpace(string(output))
	}
	stop()
	race := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" {
				race = setting.Value
			}
		}
	}
	t.Logf("resources phase=%s go=%s os=%s arch=%s cpu=%d gomaxprocs=%d race=%s goroutines=%d heap_alloc_bytes=%d heap_sys_bytes=%d go_sys_bytes=%d gc=%d rss_kib=%s fd_open=%s fd_soft_limit=%s",
		phase, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0),
		race, runtime.NumGoroutine(), memory.HeapAlloc, memory.HeapSys, memory.Sys, memory.NumGC, rss, fdCount, fdLimit)
}
