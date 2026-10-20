package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestPrivateKnowledgeInventoryAndPersistence(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	state, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.BindIdentity(ctx, "agent_one"); err != nil {
		t.Fatal(err)
	}
	if err := state.BindIdentity(ctx, "agent_two"); err == nil {
		t.Fatal("identity borrowing")
	}
	for revision := int64(0); revision < 5; revision++ {
		if _, err := state.PutKnowledge(ctx, KnowledgeChange{Kind: "memory", Name: "lesson", Content: "private notes", ExpectedRevision: revision, Selected: true}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := state.KnowledgeHistory(ctx, "memory:lesson")
	if err != nil || len(history) != 3 {
		t.Fatal(history, err)
	}
	knowledge, err := state.Knowledge(ctx, "memory:lesson")
	if err != nil || knowledge.Revision != 5 || knowledge.Content != "private notes" {
		t.Fatal(knowledge, err)
	}
	selected, err := state.SelectedKnowledge(ctx)
	if err != nil || len(selected) != 1 {
		t.Fatal(selected, err)
	}
	if _, err := state.PutKnowledge(ctx, KnowledgeChange{Kind: "memory", Name: "lesson", Content: "stale", ExpectedRevision: 1}); err == nil {
		t.Fatal("stale knowledge revision")
	}
	inv, err := state.RefreshInventory(ctx)
	if err != nil || inv.CPUs < 1 || len(inv.Summary()) != 10 {
		t.Fatal(inv, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	recovered, err := state.Inventory(ctx)
	if err != nil || recovered.Hash() != inv.Hash() {
		t.Fatal(recovered, err)
	}
}

func TestWorkspaceIdentityAndOutbox(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	work := filepath.Join(root, "workspace")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	binding, err := state.BindWorkspace(ctx, work, true, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Workspace(ctx, binding.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := state.BindWorkspace(ctx, root, true, 60, nil); err == nil {
		t.Fatal("bound parent containing private state")
	}
	if err := state.EnableWorkspace(ctx, binding.Ref, false); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Workspace(ctx, binding.Ref); err == nil {
		t.Fatal("disabled binding usable")
	}
	if _, err := state.BindWorkspace(ctx, work, true, 60, nil); err == nil {
		t.Fatal("bypassed disabled binding")
	}
	if err := state.EnableWorkspace(ctx, binding.Ref, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(work, work+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Workspace(ctx, binding.Ref); err == nil {
		t.Fatal("replaced inode accepted")
	}
	body, _ := json.Marshal(domain.Command{OperationID: "outbox-one"})
	item := Outbox{OperationID: "outbox-one", Method: "POST", Path: "/v1/example", Body: body}
	if err := state.Enqueue(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := state.Enqueue(ctx, item); err != nil {
		t.Fatal(err)
	}
	item.Path = "/v1/changed"
	if err := state.Enqueue(ctx, item); err == nil {
		t.Fatal("outbox proposal overwritten")
	}
	items, err := state.Outbox(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if err := state.Acknowledge(ctx, "outbox-one"); err != nil {
		t.Fatal(err)
	}
	items, err = state.Outbox(ctx)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	j := Journal{ID: "journal", State: "invoking", Kind: "execution"}
	if err := state.SaveJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	journals, err := state.Journals(ctx, true)
	if err != nil || len(journals) != 1 {
		t.Fatal(journals, err)
	}
	j.State = "done"
	if err := state.SaveJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	journals, err = state.Journals(ctx, true)
	if err != nil || len(journals) != 0 {
		t.Fatal(journals, err)
	}
}
