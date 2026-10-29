package api

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeMessageHistoryAndThreadPagesStayPartyPrivate(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	sender := messageFixtureClient(t, f.server, f.founder.ID)
	recipient := messageFixtureClient(t, f.server, f.target.ID)
	foreign := messageFixtureClient(t, f.server, f.delegate.ID)
	ctx := context.Background()
	message, err := Call[domain.Message](ctx, sender, "POST", "/v1/messages", domain.MessageRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, RecipientID: f.target.ID,
		Body: "Native historical fixture", Origin: "human", Kind: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Call[domain.Message](ctx, recipient, "POST", Path("messages", message.ID, "receipt"),
		domain.MessageReceipt{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: 1}, State: "handled"}); err != nil {
		t.Fatal(err)
	}
	page, err := Call[domain.Page[domain.Message]](ctx, recipient, "GET", "/v1/message-history?folder=received&state=all&limit=1", nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != message.ID || page.Items[0].ReadAt != 0 || page.Items[0].State != "handled" {
		t.Fatal("handled history was missing, implicitly read or altered", page, err)
	}
	thread, err := Call[domain.Page[domain.Message]](ctx, recipient, "GET", Path("messages", message.ID, "thread-page")+"?limit=1", nil)
	if err != nil || len(thread.Items) != 1 {
		t.Fatal(thread, err)
	}
	if _, err := Call[domain.Page[domain.Message]](ctx, foreign, "GET", Path("messages", message.ID, "thread-page"), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("foreign actor opened private history", err)
	}
	if _, err := Call[domain.Page[domain.Message]](ctx, recipient, "GET", "/v1/message-history?actor="+f.founder.ID, nil); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("caller supplied a principal override", err)
	}
}
