package foundation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestReplyLoopWarnsOnceAtOneHundredRoundTripsWithoutStopping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	parent := ""
	var last domain.Message
	warnings := func() []domain.Message {
		t.Helper()
		rows, err := f.s.db.QueryContext(ctx, "SELECT data FROM messages WHERE id LIKE 'message_loop_%'")
		if err != nil {
			t.Fatal(err)
		}
		items, err := scanJSON[domain.Message](rows)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	for i := range 204 {
		sender, recipient := f.worker.ID, f.other.ID
		if i%2 != 0 {
			sender, recipient = recipient, sender
		}
		request := domain.MessageRequest{Command: op(), RecipientID: recipient, ReplyToID: parent,
			Origin: "ai", Kind: "direct", Body: "PRIVATE_LOOP_CONTENT", ResponseRequest: true}
		var err error
		last, err = f.s.SendMessage(ctx, sender, request)
		if err != nil {
			t.Fatal("a warning must not interrupt replies", i, err)
		}
		replayed, err := f.s.SendMessage(ctx, sender, request)
		if err != nil || replayed.ID != last.ID {
			t.Fatal("exact replay changed a loop message", replayed, err)
		}
		if i == 198 && len(warnings()) != 0 {
			t.Fatal("warning was sent before 100 complete round trips")
		}
		if i == 198 {
			var sequence int
			var name, path string
			if err := f.s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			f.s.now = func() time.Time { return f.now }
			t.Cleanup(func() {
				if err := f.s.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		if i >= 199 && len(warnings()) != 1 {
			t.Fatal("threshold/replay produced a missing or duplicate warning", i, warnings())
		}
		parent = last.ID
	}
	warning := warnings()[0]
	if warning.RecipientID != f.founder.ID || warning.ResponseRequest || warning.Origin != "service" ||
		strings.Contains(warning.Body, "PRIVATE_LOOP_CONTENT") || !strings.Contains(warning.Body, last.ThreadID) {
		t.Fatal("loop warning leaked content or requested inference", warning)
	}
	for _, actor := range []domain.Agent{f.worker, f.other, f.founder} {
		current, err := f.s.Me(ctx, actor.ID)
		if err != nil || current.Mode != actor.Mode {
			t.Fatal("warning changed mode", current, err)
		}
	}
}

func TestAdhocTasksSelfAssignWithoutGrantingProjectOrVerificationAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	request := domain.TaskRequest{Command: op(), ProjectID: domain.AdhocProjectID,
		Title: "Adhoc action", Description: "Perform the authorized standalone action.", AcceptanceCriteria: "Report actual checks and results."}
	task, err := f.s.CreateTask(ctx, f.worker.ID, request)
	if err != nil || task.AssigneeID != f.worker.ID || task.Status != domain.Todo || task.Readiness != nil {
		t.Fatal("Adhoc task was not an immediate self-assigned record", task, err)
	}
	replayed, err := f.s.CreateTask(ctx, f.worker.ID, request)
	if err != nil || replayed.ID != task.ID {
		t.Fatal("Adhoc creation was duplicated after replay", replayed, err)
	}
	project, err := f.s.Project(ctx, f.worker.ID, domain.AdhocProjectID)
	if err != nil || project.Name != "Adhoc" || project.OwnerID != f.founder.ID || len(project.Delegates) != 0 {
		t.Fatal("special project incorrectly granted delegation", project, err)
	}
	if domain.EligibleApprover(f.worker, project, task, true) || domain.EligibleApprover(f.other, project, task, false) {
		t.Fatal("Adhoc standing readiness granted verification or another task's authority")
	}
	task = f.change(t, f.worker.ID, task.ID, "workspace", func(q *domain.TaskChange) { q.WorkspaceRef = "workspace_granted" })
	task = f.change(t, f.worker.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	if _, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash("owned-test-token"),
	}); err != nil {
		t.Fatal("standing self-readiness did not admit an authorized Adhoc task", err)
	}
}

func TestAssignedCommentsHaveGenerationBoundReadOnlyAttentionWithoutMentions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	comment, err := f.s.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "Please investigate the current result."})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Attention(ctx, f.worker.ID, "", 200)
	if err != nil || len(before.Items) != 1 || before.Items[0].EntryID != comment.ID {
		t.Fatal("assigned agent missed an unmentioned comment", before, err)
	}
	if relevant, err := healthActionRelevant(ctx, f.s.db, f.worker, "attention", comment.ID, true); err != nil || !relevant {
		t.Fatal("assigned comment progress was incorrectly redacted from health", relevant, err)
	}
	var persisted int
	if err := f.s.db.QueryRowContext(ctx, "SELECT count(*) FROM attention WHERE entry_id=?", comment.ID).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatal("reading assigned attention mutated stored receipts", persisted, err)
	}
	request := domain.AttentionRequest{Command: op(), Generation: comment.Generation, State: "handled"}
	if _, err := f.s.AttentionReceipt(ctx, f.worker.ID, comment.ID, request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AttentionReceipt(ctx, f.worker.ID, comment.ID, request); err != nil {
		t.Fatal("exact receipt replay failed", err)
	}
	updated, err := f.s.EditEntry(ctx, f.founder.ID, comment.ID, domain.EntryRequest{
		Command: domain.Command{OperationID: domain.NewID("edit"), ExpectedRevision: comment.Revision}, Body: "Please investigate the changed result.",
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.s.Attention(ctx, f.worker.ID, "", 200)
	if err != nil || len(after.Items) != 1 || after.Items[0].Generation != updated.Generation {
		t.Fatal("comment edit reused an older handled generation", after, err)
	}
	if _, err := f.s.AttentionReceipt(ctx, f.other.ID, comment.ID, domain.AttentionRequest{Command: op(), Generation: updated.Generation, State: "handled"}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("unassigned and unmentioned actor acknowledged a comment", err)
	}
}

func TestMessageLoopMetadataFormat(t *testing.T) {
	state := replyLoop{Version: 1, LastID: "message_prior", Messages: 199, WarningPending: true}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var retained replyLoop
	if err := json.Unmarshal(data, &retained); err != nil || retained != state {
		t.Fatal("restart would lose loop count or pending warning", retained, err)
	}
}
