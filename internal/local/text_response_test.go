package local

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestTextResponseJournalAndRetryFenceCommitAtomically(t *testing.T) {
	state := sessionRecoveryState(t)
	ctx := context.Background()
	hash := domain.Hash("semantic text action")
	journal := Journal{ID: "invocation_text", Kind: "scope", State: "reply-prepared", CreatedAt: 1800000000}
	if err := state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	journal.State, journal.TextActionHash = "reply-rejected", hash
	block := map[string]any{"version": 1, "journal_id": journal.ID, "action_hash": hash, "state": "blocked"}
	if err := state.SaveTextResponse(ctx, journal, hash, block); err != nil {
		t.Fatal(err)
	}
	pending, err := state.Journals(ctx, true)
	if err != nil || len(pending) != 0 {
		t.Fatal("rejected response occupied global admission", pending, err)
	}
	retained, err := state.Journal(ctx, journal.ID)
	if err != nil || retained.State != "reply-rejected" {
		t.Fatal(retained, err)
	}
	if _, err := state.db.ExecContext(ctx, `CREATE TRIGGER fixture_retry_failure BEFORE UPDATE ON journals WHEN NEW.state='abandoned' BEGIN SELECT RAISE(ABORT,'fixture retry journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	journal.State = "abandoned"
	block["state"] = "retry-authorized"
	if err := state.SaveTextResponse(ctx, journal, hash, block); err == nil {
		t.Fatal("fixture did not fail retry commit")
	}
	var actual map[string]any
	if _, err := state.Get(ctx, "text-response:"+hash, &actual); err != nil || actual["state"] != "blocked" {
		t.Fatal("failed journal write authorized another model call", actual, err)
	}
	retained, err = state.Journal(ctx, journal.ID)
	if err != nil || retained.State != "reply-rejected" {
		t.Fatal("failed retry changed retained journal", retained, err)
	}
}
