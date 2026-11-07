package api

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func TestAgentAdditionRelayRequiresCurrentFounderAndLiteralEndpoint(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []domain.Agent{f.target, f.delegate} {
		client := messageFixtureClient(t, f.server, actor.ID)
		if _, err := DialAgentAdditionRelay(ctx, client, "add_fixture", host, port); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("non-Founder reached an onboarding relay", err)
		}
	}
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	valid := domain.AgentAdditionRelayOpen{OperationID: "add_fixture", Host: host, Port: port, Ticket: "relay_fixture"}
	for _, change := range []func(*domain.AgentAdditionRelayOpen){
		func(v *domain.AgentAdditionRelayOpen) { v.Host = "worker.example" },
		func(v *domain.AgentAdditionRelayOpen) { v.Host = "0.0.0.0" },
		func(v *domain.AgentAdditionRelayOpen) { v.Host = "::" },
		func(v *domain.AgentAdditionRelayOpen) { v.Host = "224.0.0.1" },
		func(v *domain.AgentAdditionRelayOpen) { v.Host = "fe80::1%lo0" },
		func(v *domain.AgentAdditionRelayOpen) { v.Port = 0 },
		func(v *domain.AgentAdditionRelayOpen) { v.Port = 65536 },
		func(v *domain.AgentAdditionRelayOpen) { v.OperationID = "" },
		func(v *domain.AgentAdditionRelayOpen) { v.Ticket = "../other" },
	} {
		input := valid
		change(&input)
		data, err := message.MarshalPayload(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := founder.Message(ctx, message.Request{Operation: AgentAdditionRelayOperation, Payload: data}); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("server accepted an invalid onboarding endpoint or binding", input, err)
		}
	}
	data, err := message.MarshalPayload(valid)
	if err != nil {
		t.Fatal(err)
	}
	spoofed := f.target
	spoofed.Founder = true
	peer := newRelayPeer(f.server, ctx, spoofed)
	if _, err := peer.call(ctx, message.Request{Operation: AgentAdditionRelayOperation, Payload: data}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("relay trusted a cached or spoofed Founder role instead of current authority", err)
	}
	for _, request := range []message.Request{
		{Operation: AgentAdditionRelayOperation, Target: "override", Payload: data},
		{Operation: AgentAdditionRelayOperation, Query: map[string]string{"actor": f.founder.ID}, Payload: data},
		{Operation: AgentAdditionRelayOperation, Payload: make([]byte, (64<<10)+1)},
	} {
		if _, err := founder.Message(ctx, request); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("onboarding relay accepted metadata or unbounded input", err)
		}
	}
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("a denied or invalid request dialed the worker")
	}
	if _, err := DialAgentAdditionRelay(ctx, &Client{}, "add_fixture", host, port); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("onboarding relay fell back to an HTTP transport", err)
	}
}

func TestAgentAdditionRelayTransfersOpaqueBytesAndKeepsConnectionTicketsPrivate(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	otherConnection := messageFixtureClient(t, f.server, f.founder.ID)
	stream, err := DialAgentAdditionRelay(ctx, founder, "add_fixture", "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	worker, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	ticket := stream.(*messageRelay).ticket
	if _, err := otherConnection.Message(ctx, message.Request{Operation: AgentAdditionRelayPrefix + ".read", Target: ticket}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another connection borrowed an onboarding ticket", err)
	}
	for _, operation := range []string{"relay.read", "relay.write", "relay.close"} {
		if _, err := founder.Message(ctx, message.Request{Operation: operation, Target: ticket}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("ordinary session relay operations accessed an onboarding ticket", operation, err)
		}
	}
	payload := bytes.Repeat([]byte{0, 1, 255, '\n'}, 30000)
	written := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		written <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(worker, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("onboarding relay changed encrypted worker-bound bytes", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := worker.Write(payload)
		written <- err
	}()
	if _, err := io.ReadFull(stream, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("onboarding relay changed encrypted Founder-bound bytes", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := worker.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Read(got[:1]); err != io.EOF {
		t.Fatal("closing the ticket did not close the worker connection", err)
	}
}

func TestAgentAdditionRelayBoundsPendingDialsAndClosesCancelledAdmissions(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := newRelayPeer(f.server, ctx, f.founder)
	defer peer.close()
	started, release := make(chan struct{}, 4), make(chan struct{})
	var dials sync.WaitGroup
	for range 4 {
		dials.Go(func() {
			call, stop := context.WithCancel(ctx)
			defer stop()
			_, err := peer.open(call, domain.NewID("relay"), &relayStream{kind: additionRelay}, func() (net.Conn, error) {
				started <- struct{}{}
				<-release
				stop()
				left, right := net.Pipe()
				right.Close()
				return left, nil
			})
			if domain.AsFault(err).Code != domain.Cancelled {
				t.Error("cancelled onboarding admission was retained", err)
			}
		})
	}
	for range 4 {
		<-started
	}
	_, err := peer.open(ctx, "relay_excess", &relayStream{kind: additionRelay}, func() (net.Conn, error) {
		t.Error("onboarding relay exceeded its pending-dial limit")
		return nil, domain.Fail(domain.Internal, "unexpected excess relay admission")
	})
	close(release)
	dials.Wait()
	if domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("pending onboarding dials did not retain bounded admission", err)
	}
	if len(peer.streams) != 0 || len(peer.openings) != 0 {
		t.Fatal("cancelled onboarding dials leaked connection state")
	}
	var tickets []string
	dial := func() (net.Conn, error) {
		left, right := net.Pipe()
		t.Cleanup(func() { right.Close() })
		return left, nil
	}
	for range 4 {
		ticket := domain.NewID("relay")
		if _, err := peer.open(ctx, ticket, &relayStream{kind: additionRelay}, dial); err != nil {
			t.Fatal("cancelled openings did not release their admission", err)
		}
		tickets = append(tickets, ticket)
	}
	if _, err := peer.open(ctx, "relay_excess_active", &relayStream{kind: additionRelay}, dial); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("onboarding relay exceeded its four active-stream bound", err)
	}
	if _, err := peer.call(ctx, message.Request{Operation: AgentAdditionRelayPrefix + ".close", Target: tickets[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.open(ctx, "relay_after_close", &relayStream{kind: additionRelay}, dial); err != nil {
		t.Fatal("closing an active onboarding relay did not release its admission", err)
	}
}
