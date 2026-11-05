package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestSendMessageRejectsSelfWithoutCreatingMessagesOrReceipts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, actor := range []domain.Agent{f.founder, f.worker, f.other} {
		for _, kind := range []string{"direct", "request", "authentication", "permission", "session"} {
			req := domain.MessageRequest{Command: op(), RecipientID: actor.ID, Body: "Do not send this to myself.",
				Kind: kind, Origin: "service", ResponseRequest: kind == "direct"}
			for range 2 {
				if _, err := f.s.SendMessage(ctx, actor.ID, req); domain.AsFault(err).Code != domain.Invalid {
					t.Fatal("self message was not explicitly rejected", actor.ID, kind, err)
				}
			}
		}
	}
	var count int
	if err := f.s.db.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected sends created message records", count, err)
	}
	req := domain.MessageRequest{Command: op(), RecipientID: f.other.ID, Body: "An ordinary private message.", Kind: "direct", Origin: "human"}
	sent, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil || sent.RecipientID != f.other.ID {
		t.Fatal("sending to another agent changed", sent, err)
	}
	replayed, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil || replayed.ID != sent.ID {
		t.Fatal("ordinary exact replay changed", replayed, err)
	}
}
