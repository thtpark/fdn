package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestDiscussionEntryReadPreservesParentMetadataAndAttention(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	task := f.ready(t)
	root, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{
		Command: op(), Kind: "comment", Body: "Original question for @{" + f.founder.ID + "}.",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{
		Command: op(), Kind: "comment", ParentID: root.ID, Body: "Reply retaining its original parent.",
	})
	if err != nil {
		t.Fatal(err)
	}
	worklog, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{
		Command: op(), Kind: "worklog", Body: "Intentional fixture checkpoint.",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	attention, err := f.s.Attention(ctx, f.founder.ID, "", 200)
	if err != nil || len(attention.Items) != 1 {
		t.Fatal(attention, err)
	}
	for _, entry := range []domain.Entry{root, reply, worklog} {
		got, err := f.s.Entry(ctx, f.founder.ID, entry.ID)
		if err != nil || !reflect.DeepEqual(got, entry) {
			t.Fatal("entry read changed parent/body/generation metadata", got, entry, err)
		}
	}
	after, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("entry lookup changed its task", before, after, err)
	}
	remaining, err := f.s.Attention(ctx, f.founder.ID, "", 200)
	if err != nil || !reflect.DeepEqual(attention, remaining) {
		t.Fatal("entry lookup acknowledged a mention", attention, remaining, err)
	}
	_, err = f.s.Entry(ctx, f.founder.ID, domain.NewID("entry"))
	assertCode(t, err, domain.NotFound)
	if _, err := f.s.Entry(ctx, domain.NewID("agent"), root.ID); err == nil {
		t.Fatal("entry lookup accepted an unenrolled actor")
	}
}
