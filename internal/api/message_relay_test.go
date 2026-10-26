package api

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func TestBinaryRelayTransferDeadlineRevocationAndCapabilityIsolation(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	requester := messageFixtureClient(t, f.server, f.founder.ID)
	target := messageFixtureClient(t, f.server, f.target.ID)
	unrelated := messageFixtureClient(t, f.server, f.delegate.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grant, err := Call[domain.AssistanceGrant](ctx, requester, "POST", "/v1/assistance", domain.AssistanceGrantRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, RequesterID: f.founder.ID, TargetID: f.target.ID,
		ProjectID: f.project.ID, Mode: "tmux", Purpose: "terminal", Reason: "isolated binary relay fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := Call[domain.Session](ctx, requester, "POST", "/v1/sessions", domain.SessionRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, GrantID: grant.ID, TargetID: f.target.ID,
		ProjectID: f.project.ID, Mode: "tmux", Purpose: "terminal", Reason: "isolated bytes", ExpiresAt: time.Now().Unix() + 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = Call[domain.Session](ctx, requester, "POST", Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: session.Revision}, Action: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	left, err := DialMessageRelay(ctx, requester, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := DialMessageRelay(ctx, target, session.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	if _, err := unrelated.Message(ctx, message.Request{Operation: "relay.read", Target: left.(*messageRelay).ticket}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another actor used a private connection ticket", err)
	}
	invalid, err := message.MarshalPayload(RelayChunk{Data: []byte("not a close command")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requester.Message(ctx, message.Request{Operation: "relay.close", Target: left.(*messageRelay).ticket, Payload: invalid}); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("invalid close mutated a relay", err)
	}
	timedOut := make(chan error, 1)
	go func() {
		_, err := right.Read(make([]byte, 1))
		timedOut <- err
	}()
	if err := right.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-timedOut:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal("an active binary read ignored its deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("binary read deadline did not interrupt the active call")
	}
	if err := right.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(strings.Repeat("private bytes\n", 10000))
	written := make(chan error, 1)
	go func() {
		n, err := left.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(right, buffer); err != nil || string(buffer) != string(payload) {
		t.Fatal("bounded chunking or deadline reset corrupted the stream", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if _, err := Call[domain.Session](ctx, requester, "POST", Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: session.Revision}, Action: "revoke",
	}); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := right.Read(make([]byte, 1))
		closed <- err
	}()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("revocation left the private relay readable")
		}
	case <-time.After(time.Second):
		t.Fatal("scoped revocation did not close the binary relay")
	}
}
