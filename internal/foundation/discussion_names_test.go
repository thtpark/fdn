package foundation

import (
	"context"
	"slices"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentNameMentionsResolveOnceAndRetainBodyGenerationFences(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	task := f.ready(t)
	body := "Please review @{Founder} @{FOUNDER} @{" + f.founder.ID + "} @{worker}."
	request := domain.EntryRequest{Command: op(), Kind: "comment", Body: body}
	entry, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, request)
	if err != nil || entry.Body != body || !slices.Equal(entry.Mentions, []string{f.founder.ID}) {
		t.Fatal("name/ID deduplication or self-exclusion failed", entry, err)
	}
	pending, err := f.s.Attention(ctx, f.founder.ID, "", 200)
	if err != nil || len(pending.Items) != 1 || pending.Items[0].EntryID != entry.ID {
		t.Fatal("name mention did not create exactly one attention item", pending, err)
	}
	f.enroll(t, "FOUNDER")
	replay, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, request)
	if err != nil || replay.ID != entry.ID || !slices.Equal(replay.Mentions, entry.Mentions) {
		t.Fatal("replay re-resolved a name instead of returning the original receipt", replay, err)
	}
	_, err = f.s.EditEntry(ctx, f.worker.ID, entry.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: entry.Revision}, Body: "Ambiguous @{Founder}",
	})
	assertCode(t, err, domain.Conflict)
	unchanged, err := get[domain.Entry](ctx, f.s.db, "entries", entry.ID)
	if err != nil || unchanged.Body != entry.Body || unchanged.Generation != entry.Generation || !slices.Equal(unchanged.Mentions, entry.Mentions) {
		t.Fatal("failed mention edit changed the original body/recipients", unchanged, err)
	}
	edited, err := f.s.EditEntry(ctx, f.worker.ID, entry.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: entry.Revision}, Body: "Please review @{OTHER}.",
	})
	if err != nil || edited.Generation != entry.Generation+1 || !slices.Equal(edited.Mentions, []string{f.other.ID}) {
		t.Fatal("name mention edit lost its recipient or generation", edited, err)
	}
	_, err = f.s.AttentionReceipt(ctx, f.founder.ID, entry.ID, domain.AttentionRequest{Command: op(), Generation: entry.Generation, State: "handled"})
	assertCode(t, err, domain.Conflict)
	pending, err = f.s.Attention(ctx, f.founder.ID, "", 200)
	if err != nil || len(pending.Items) != 0 {
		t.Fatal("edited name mention left old attention pending", pending, err)
	}
	receipt, err := f.s.AttentionReceipt(ctx, f.other.ID, entry.ID, domain.AttentionRequest{Command: op(), Generation: edited.Generation, State: "handled"})
	if err != nil || receipt.State != "handled" {
		t.Fatal(receipt, err)
	}
}

func TestAgentNameMentionsRejectUnknownAmbiguousAndInactiveNamesAtomically(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	task := f.ready(t)
	f.enroll(t, "FOUNDER")
	for _, body := range []string{"Unknown @{missing}", "Ambiguous @{founder}"} {
		before, err := f.s.Task(ctx, f.worker.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: body})
		want := domain.NotFound
		if body == "Ambiguous @{founder}" {
			want = domain.Conflict
		}
		assertCode(t, err, want)
		after, err := f.s.Task(ctx, f.worker.ID, task.ID)
		if err != nil || after.Revision != before.Revision || after.DiscussionGeneration != before.DiscussionGeneration {
			t.Fatal("failed name resolution changed the discussion", after, err)
		}
	}
	var entries int
	if err := f.s.db.QueryRow("SELECT count(*) FROM entries").Scan(&entries); err != nil || entries != 0 {
		t.Fatal("unresolved mention left a partial entry", entries, err)
	}
	_, err := f.s.Revoke(ctx, f.founder.ID, f.other.ID, domain.AgentRequest{Command: op()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "Inactive @{other}"})
	assertCode(t, err, domain.NotFound)
	entry, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "Explicit @{" + f.other.ID + "} and @{" + f.founder.ID + "}"})
	want := []string{f.other.ID, f.founder.ID}
	slices.Sort(want)
	if err != nil || !slices.Equal(entry.Mentions, want) {
		t.Fatal("ID mentions lost compatibility in the presence of duplicate/inactive names", entry, err)
	}
}

func TestAgentNameMentionsSupportSpacedNamesAndWorklogSemantics(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	agent, _ := f.enroll(t, "Agent One")
	task := f.ready(t)
	entry, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "worklog", Body: "Discussed with @{agent one}."})
	if err != nil || !slices.Equal(entry.Mentions, []string{agent.ID}) {
		t.Fatal("spaced name was not resolved", entry, err)
	}
	pending, err := f.s.Attention(ctx, agent.ID, "", 200)
	if err != nil || len(pending.Items) != 0 {
		t.Fatal("worklog mentions unexpectedly created attention", pending, err)
	}
}
