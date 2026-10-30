package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestClarityConsumesOnlyExplicitlyReviewedCommentGenerations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	answer, err := f.s.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "An answer incorporated into the current task."})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "A distinct pending follow-up."})
	if err != nil {
		t.Fatal(err)
	}
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) {
		q.SpecGeneration, q.AnswerIDs = task.SpecGeneration, []string{answer.ID}
	})
	pending, err := f.s.Attention(ctx, f.worker.ID, "", 200)
	if err != nil || len(pending.Items) != 1 || pending.Items[0].EntryID != other.ID {
		t.Fatal("clarity either repeated incorporated answers or swallowed unrelated comments", pending, err)
	}
	changed, err := f.s.EditEntry(ctx, f.founder.ID, answer.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: domain.NewID("edit"), ExpectedRevision: answer.Revision}, Body: "A new body generation needs another review.",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err = f.s.Attention(ctx, f.worker.ID, "", 200)
	if err != nil || len(pending.Items) != 2 {
		t.Fatal("previous clarity swallowed a later comment generation", pending, err)
	}
	found := false
	for _, item := range pending.Items {
		if item.EntryID == changed.ID && item.Generation == changed.Generation {
			found = true
		}
	}
	if !found {
		t.Fatal("changed incorporated answer did not become pending again")
	}
}
