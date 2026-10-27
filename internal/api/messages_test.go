package api

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func messageFixtureClient(t *testing.T, server *Server, actor string) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var connections sync.WaitGroup
	client := NewMessageClient("fixture://foundation", func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		connections.Go(func() {
			defer left.Close()
			_ = server.ServeTrustedMessages(ctx, left, actor)
		})
		return right, nil
	})
	client.Credential.AgentID = actor
	t.Cleanup(func() {
		client.Close()
		cancel()
		connections.Wait()
	})
	return client
}

func TestMessageBusinessCommandsPrivacyAndExactReplay(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	sender := messageFixtureClient(t, f.server, f.founder.ID)
	recipient := messageFixtureClient(t, f.server, f.target.ID)
	unrelated := messageFixtureClient(t, f.server, f.delegate.ID)
	ctx := context.Background()
	me, err := Call[domain.Agent](ctx, recipient, "GET", "/v1/me", nil)
	if err != nil || me.ID != f.target.ID {
		t.Fatal("binary principal/query mismatch", me, err)
	}
	var raw json.RawMessage
	if err := recipient.Request(ctx, "GET", "/v1/tasks?assignee="+f.target.ID, nil, &raw); err != nil ||
		!strings.Contains(string(raw), `"items":[]`) {
		t.Fatal("binary empty page changed CLI output shape", string(raw), err)
	}
	watermark, err := Call[domain.Page[domain.Update]](ctx, recipient, "GET", "/v1/updates", nil)
	if err != nil || watermark.NextCursor == "" {
		t.Fatal("initial update watermark missing", watermark, err)
	}
	request := domain.MessageRequest{Command: domain.Command{OperationID: domain.NewID("op")},
		RecipientID: f.target.ID, Body: "PRIVATE_BINARY_MESSAGE", Origin: "human", Kind: "direct", Urgent: true}
	sent, err := Call[domain.Message](ctx, sender, "POST", "/v1/messages", request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Call[domain.Message](ctx, sender, "POST", "/v1/messages", request)
	if err != nil || replayed.ID != sent.ID {
		t.Fatal("exact message operation replay changed identity", replayed, err)
	}
	page, err := Call[domain.Page[domain.Update]](ctx, recipient, "GET", "/v1/updates?since="+watermark.NextCursor, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != sent.ID {
		t.Fatal("committed private update was missing or duplicated", page, err)
	}
	shown, err := Call[domain.Message](ctx, recipient, "GET", Path("messages", sent.ID), nil)
	if err != nil || shown.Body != request.Body || shown.ReadAt != 0 {
		t.Fatal("delivery/query implicitly read a message or changed its body", shown, err)
	}
	if _, err := Call[domain.Message](ctx, unrelated, "GET", Path("messages", sent.ID), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("binary message bypassed party privacy", err)
	}
	request.Body = "changed immutable operation"
	if _, err := Call[domain.Message](ctx, sender, "POST", "/v1/messages", request); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("different input reused an immutable operation", err)
	}
	if _, err := recipient.Message(ctx, message.Request{Operation: "command.identity.recover-founder"}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary binary ingress exposed root maintenance", err)
	}
	if _, err := recipient.Message(ctx, message.Request{Operation: "query.me", Query: map[string]string{"uid": "0"}}); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("binary principal was overridable by metadata", err)
	}
}

func TestMessageBlockedSubscriptionDoesNotBlockQueriesPingOrCancellation(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	client := messageFixtureClient(t, f.server, f.target.ID)
	ctx := context.Background()
	initial, err := Call[domain.Page[domain.Update]](ctx, client, "GET", "/v1/updates", nil)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := Call[domain.Page[domain.Update]](wait, client, "GET", "/v1/updates?wait=true&since="+initial.NextCursor, nil)
		result <- err
	}()
	check, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := client.Ping(check); err != nil {
		t.Fatal("blocked subscription starved transport liveness", err)
	}
	if _, err := Call[domain.Agent](check, client, "GET", "/v1/me", nil); err != nil {
		t.Fatal("blocked subscription serialized an unrelated query", err)
	}
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if domain.AsFault(err).Code != domain.Cancelled || time.Since(started) > time.Second {
			t.Fatal("priority subscription cancellation failed", err, time.Since(started))
		}
	case <-time.After(time.Second):
		t.Fatal("priority subscription cancellation exceeded one second")
	}
}

func TestBinaryMessageRequestDecisionsThreadPrivacyAndMaintenanceBoundary(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx := context.Background()
	sender := messageFixtureClient(t, f.server, f.founder.ID)
	recipient := messageFixtureClient(t, f.server, f.target.ID)
	unrelated := messageFixtureClient(t, f.server, f.delegate.ID)
	source, err := Call[domain.Message](ctx, sender, "POST", "/v1/messages", domain.MessageRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, RecipientID: f.target.ID,
		Body: "Please review this informational request.", Origin: "human", Kind: "request",
		ResponseRequest: true, Request: &domain.MessageAction{Kind: "work"},
	})
	if err != nil || source.Request == nil || source.Request.State != "queued" || source.ReadAt != 0 {
		t.Fatal("binary request delivery manufactured execution or a read acknowledgement", source, err)
	}
	report := domain.MessageActionReport{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: source.Revision},
		State: "declined", Reason: "The recipient cannot satisfy this request in its current scope."}
	if _, err := Call[domain.Message](ctx, sender, "POST", Path("messages", source.ID, "request"), report); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("sender decided an agent's own request", err)
	}
	decided, err := Call[domain.Message](ctx, recipient, "POST", Path("messages", source.ID, "request"), report)
	if err != nil || decided.Request.State != "declined" {
		t.Fatal("target decision failed across the binary boundary", decided, err)
	}
	if _, err := Call[domain.Message](ctx, recipient, "POST", Path("messages", source.ID, "request"), report); err != nil {
		t.Fatal("binary exact decision replay failed", err)
	}
	thread, err := Call[[]domain.Message](ctx, sender, "GET", Path("messages", source.ID, "thread"), nil)
	if err != nil || len(thread) != 2 || thread[1].RequestUpdate == nil || thread[1].RequestUpdate.State != "declined" {
		t.Fatal("binary request decision/result thread was missing or duplicated", thread, err)
	}
	if _, err := Call[[]domain.Message](ctx, unrelated, "GET", Path("messages", source.ID, "thread"), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("unrelated actor read the private request thread", err)
	}
	for _, client := range []*Client{sender, recipient} {
		for _, operation := range []string{"query.maintenance.requests", "command.maintenance.request-result"} {
			if _, err := client.Message(ctx, message.Request{Operation: operation}); domain.AsFault(err).Code != domain.Denied {
				t.Fatal("ordinary agent ingress exposed the Foundation maintenance receiver", operation, err)
			}
		}
	}
}
