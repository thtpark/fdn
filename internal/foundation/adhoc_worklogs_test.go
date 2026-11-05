package foundation

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func adhocEntries(t *testing.T, f *fixture, task string) []domain.Entry {
	t.Helper()
	rows, err := f.s.db.QueryContext(context.Background(), "SELECT data FROM entries WHERE task_id=? ORDER BY rowid", task)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := scanJSON[domain.Entry](rows)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestAdhocLifecycleWorklogsAreDetailedAtomicAndReplaySafe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := domain.TaskRequest{Command: op(), ProjectID: domain.AdhocProjectID,
		Title:              "Check the generated dependency report",
		Description:        "Inspect the generated dependency report and identify outdated entries, without installing anything. Literal @{not-an-agent} is data.",
		AcceptanceCriteria: "Compare each reported version against the retained inventory and report actual discrepancies."}
	task, err := f.s.CreateTask(ctx, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateTask(ctx, f.worker.ID, req); err != nil {
		t.Fatal(err)
	}
	entries := adhocEntries(t, f, task.ID)
	if len(entries) != 1 || !strings.Contains(entries[0].Body, req.Description) || !strings.Contains(entries[0].Body, req.AcceptanceCriteria) ||
		entries[0].Kind != "worklog" || len(entries[0].Mentions) != 0 || task.DiscussionGeneration != 0 {
		t.Fatal("creation was generic, duplicated or changed discussion/mentions", entries, task)
	}
	task = f.change(t, f.worker.ID, task.ID, "workspace", func(q *domain.TaskChange) { q.WorkspaceRef = "workspace_granted" })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.worker.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(token)}
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim); err != nil {
		t.Fatal("claim replay failed", err)
	}
	report := domain.ReportRequest{Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
		Outcome: "completed", Summary: "Compared all 12 entries; two retained versions need follow-up.",
		Evidence: []string{"12 inventory entries checked", "No packages or configuration changed."}}
	result, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || !result.Accepted || result.Task.Status != domain.Completed {
		t.Fatal(result, err)
	}
	beforeReplay := adhocEntries(t, f, task.ID)
	if _, err := f.s.Report(ctx, f.worker.ID, task.ID, report); err != nil {
		t.Fatal("report replay failed", err)
	}
	entries = adhocEntries(t, f, task.ID)
	if len(entries) != 3 || !reflect.DeepEqual(entries, beforeReplay) {
		t.Fatal("lifecycle worklogs were lost or duplicated on exact replay", entries)
	}
	if !strings.Contains(entries[1].Body, run.ID) || !strings.Contains(entries[1].Body, "not yet confirmed") ||
		!strings.Contains(entries[2].Body, report.Summary) || !strings.Contains(entries[2].Body, report.Evidence[0]) ||
		!strings.Contains(entries[2].Body, "awaits independent verification") || result.Task.DiscussionGeneration != task.DiscussionGeneration {
		t.Fatal("lifecycle log misreported claim/completion/verification or invalidated clarity", entries)
	}
}

func TestAdhocWorklogsPreserveFullDescriptionAndEvidenceAtByteLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, err := f.s.CreateTask(ctx, f.worker.ID, domain.TaskRequest{Command: op(), ProjectID: domain.AdhocProjectID,
		Title: "Inspect the complete bounded report", Description: strings.Repeat("d", 65536),
		AcceptanceCriteria: strings.Repeat("c", 32768)})
	if err != nil {
		t.Fatal("a valid long description could not be logged", err)
	}
	evidence := make([]string, 64)
	for i := range evidence {
		evidence[i] = strings.Repeat("e", 8192)
	}
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := f.s.adhocRunWorklog(ctx, tx, task, domain.Run{ID: "run_bounded", Kind: domain.ExecutionRun,
		AgentID: f.worker.ID, State: "completed", Provider: "codex", Summary: strings.Repeat("s", 32768), Evidence: evidence}); err != nil {
		t.Fatal("valid bounded evidence was truncated or rejected", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	entries := adhocEntries(t, f, task.ID)
	joined := ""
	for _, entry := range entries {
		if len(entry.Body) > 65536 {
			t.Fatal("worklog exceeded the entry byte limit")
		}
		joined += entry.Body
	}
	if !strings.Contains(joined, strings.Repeat("d", 65536)) || !strings.Contains(joined, strings.Repeat("c", 32768)) ||
		!strings.Contains(joined, strings.Repeat("s", 32768)) || strings.Count(joined, strings.Repeat("e", 8192)) != 64 {
		t.Fatal("full description, summary or evidence was lost")
	}
}
