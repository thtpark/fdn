package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMessageHistoryPagesRetainPrivacyReceiptsAndInsertionCeiling(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var messages []domain.Message
	for range 7 {
		messages = append(messages, readFixtureMessage(t, f, false))
	}
	if _, err := f.s.ReceiveMessage(ctx, f.worker.ID, messages[0].ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "handled"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.MessageHistory(ctx, f.worker.ID, "received", "all", "", "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	seen := map[string]bool{}
	for _, m := range page.Items {
		seen[m.ID] = true
		if m.ReadAt != 0 {
			t.Fatal("browsing marked a message read")
		}
	}
	// Even a backdated insert must not enter this cursor's established snapshot.
	f.now = f.now.Add(-time.Hour)
	later := readFixtureMessage(t, f, false)
	for page.NextCursor != "" {
		page, err = f.s.MessageHistory(ctx, f.worker.ID, "received", "all", "", page.NextCursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if seen[m.ID] || m.ID == later.ID || m.ReadAt != 0 {
				t.Fatal("unstable history pagination or implicit read", m)
			}
			seen[m.ID] = true
		}
	}
	if len(seen) != 7 {
		t.Fatal("old records were omitted", seen)
	}
	sent, err := f.s.MessageHistory(ctx, f.founder.ID, "sent", "all", "", "", 3)
	if err != nil || len(sent.Items) != 3 {
		t.Fatal(sent, err)
	}
	if _, err := f.s.MessageHistory(ctx, f.other.ID, "sent", "all", "", sent.NextCursor, 3); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("cross-actor cursor accepted", err)
	}
	foreign, err := f.s.MessageHistory(ctx, f.other.ID, "all", "all", "", "", 10)
	if err != nil || len(foreign.Items) != 0 {
		t.Fatal("private history leaked", foreign, err)
	}
}

func TestMessageThreadPageExceedsLegacyBoundWithoutReadingOrExpiryRenewal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := readFixtureMessage(t, f, false)
	for range 205 {
		_, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID,
			Body: "reply", Origin: "human", Kind: "direct", ReplyToID: first.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	cursor := ""
	for {
		page, err := f.s.MessageThreadPage(ctx, f.worker.ID, first.ID, cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if seen[m.ID] || m.ReadAt != 0 {
				t.Fatal("duplicate/implicitly read thread message")
			}
			seen[m.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 206 {
		t.Fatal("thread page omitted history", len(seen))
	}
	if _, err := f.s.MessageThreadPage(ctx, f.other.ID, first.ID, "", 50); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("third party read thread", err)
	}
}
