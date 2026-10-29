package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeDiscussionEntryReadRetainsOriginalParentAndKind(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx := context.Background()
	task, err := f.store.CreateTask(ctx, f.founder.ID, domain.TaskRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, ProjectID: f.project.ID,
		Title: "Thread lookup fixture", Description: "Synthetic task context.",
		AcceptanceCriteria: "Read original comments without changing attention.",
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.store.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, Kind: "comment", Body: "Original comment.",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.store.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, Kind: "comment",
		ParentID: root.ID, Body: "Reply to the original comment.",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := messageFixtureClient(t, f.server, f.target.ID)
	for _, entry := range []domain.Entry{root, reply} {
		got, err := Call[domain.Entry](ctx, client, "GET", Path("entries", entry.ID), nil)
		if err != nil || !reflect.DeepEqual(got, entry) {
			t.Fatal("native entry lookup changed metadata", got, entry, err)
		}
	}
	if _, err := Call[domain.Entry](ctx, client, "GET", Path("entries", domain.NewID("entry")), nil); domain.AsFault(err).Code != domain.NotFound {
		t.Fatal("missing entry did not fail explicitly", err)
	}
}
