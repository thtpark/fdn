package foundation

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type fixture struct {
	s            *Store
	now          time.Time
	founder      domain.Agent
	worker       domain.Agent
	other        domain.Agent
	founderToken string
	workerToken  string
}

func op() domain.Command { return domain.Command{OperationID: domain.NewID("op")} }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "private", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &fixture{s: s, now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return f.now }
	token := domain.NewToken()
	if _, err := s.Bootstrap(ctx, domain.Hash(token), f.now.Unix()+900); err != nil {
		t.Fatal(err)
	}
	f.founderToken = domain.NewToken()
	f.founder, err = s.Enroll(ctx, domain.EnrollRequest{Command: op(), InvitationToken: token, Name: "founder", CredentialHash: domain.Hash(f.founderToken)})
	if err != nil {
		t.Fatal(err)
	}
	f.worker, f.workerToken = f.enroll(t, "worker")
	f.other, _ = f.enroll(t, "other")
	return f
}

func (f *fixture) enroll(t *testing.T, name string) (domain.Agent, string) {
	t.Helper()
	token, credential := domain.NewToken(), domain.NewToken()
	_, err := f.s.Invite(context.Background(), f.founder.ID, domain.InvitationRequest{Command: op(), TokenHash: domain.Hash(token), ExpiresAt: f.now.Unix() + 600})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.EnrollRequest{Command: op(), InvitationToken: token, Name: name, CredentialHash: domain.Hash(credential)}
	agent, err := f.s.Enroll(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.Enroll(context.Background(), req)
	if err != nil || replayed.ID != agent.ID {
		t.Fatal("lost enrollment acknowledgment replay", err)
	}
	req.Command = op()
	if _, err := f.s.Enroll(context.Background(), req); err == nil {
		t.Fatal("invitation replay accepted")
	}
	return agent, credential
}

func (f *fixture) change(t *testing.T, actor, id, kind string, set func(*domain.TaskChange)) domain.Task {
	t.Helper()
	task, err := f.s.Task(context.Background(), actor, id)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.TaskChange{Command: op(), Kind: kind, Reason: "test decision"}
	req.ExpectedRevision = task.Revision
	if set != nil {
		set(&req)
	}
	task, err = f.s.ChangeTask(context.Background(), actor, id, req)
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return task
}

func (f *fixture) ready(t *testing.T) domain.Task {
	t.Helper()
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "project"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.s.CreateTask(ctx, f.worker.ID, domain.TaskRequest{Command: op(), ProjectID: project.ID, Title: "write a file", Description: "Write the requested fixture file", AcceptanceCriteria: "File contains the requested text"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Revision != 1 {
		t.Fatal("creation returned an uncommitted revision", task.Revision)
	}
	task = f.change(t, f.founder.ID, task.ID, "assign", func(r *domain.TaskChange) { r.AgentID = f.worker.ID; r.Provider = "codex" })
	task = f.change(t, f.worker.ID, task.ID, "workspace", func(r *domain.TaskChange) { r.WorkspaceRef = "workspace_fixture" })
	task = f.change(t, f.worker.ID, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.Todo })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	return f.change(t, f.founder.ID, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
}

func assertCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if err == nil || domain.AsFault(err).Code != code {
		t.Fatalf("want %s; got %v", code, err)
	}
}

func TestProjectEpochAndRetainedReasons(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project, err := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	description := "changed project context"
	req := domain.ProjectChange{Command: op(), Description: &description, Reason: "new constraints require current approval"}
	req.ExpectedRevision = project.Revision
	if _, err := f.s.ChangeProject(ctx, f.worker.ID, project.ID, req); err == nil {
		t.Fatal("ordinary worker edited protected project context")
	}
	project, err = f.s.ChangeProject(ctx, f.founder.ID, project.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	if _, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim); err == nil {
		t.Fatal("old project epoch authorized execution")
	}
	history, err := f.s.Histories(ctx, f.worker.ID, project.ID)
	if err != nil || history[0].Reason != req.Reason {
		t.Fatal("change rationale not retained", err)
	}
	title := "latest content"
	task = f.change(t, f.worker.ID, task.ID, "edit", func(r *domain.TaskChange) { r.Title = &title })
	history, err = f.s.Histories(ctx, f.worker.ID, task.ID)
	if err != nil || history[0].Reason != "test decision" {
		t.Fatal("task rationale not retained", err)
	}
	var largest int
	if err := f.s.db.QueryRow("SELECT max(length(result)) FROM operations").Scan(&largest); err != nil {
		t.Fatal(err)
	}
	if largest > 512 {
		t.Fatal("idempotency receipts duplicated mutable history payloads", largest)
	}
}

func TestEnrollmentAuthorityAndRevocation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.s.Invite(ctx, f.worker.ID, domain.InvitationRequest{Command: op(), TokenHash: domain.Hash(domain.NewToken()), ExpiresAt: f.now.Unix() + 300})
	assertCode(t, err, domain.Denied)
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "delegated"})
	if err != nil {
		t.Fatal(err)
	}
	g := domain.GrantRequest{Command: op(), AgentID: f.worker.ID}
	g.ExpectedRevision = project.Revision
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, g)
	if err != nil {
		t.Fatal(err)
	}
	token := domain.NewToken()
	if _, err := f.s.Invite(ctx, f.worker.ID, domain.InvitationRequest{Command: op(), TokenHash: domain.Hash(token), ExpiresAt: f.now.Unix() + 300}); err != nil {
		t.Fatal(err)
	}
	g = domain.GrantRequest{Command: op(), AgentID: f.worker.ID, Revoke: true}
	g.ExpectedRevision = project.Revision
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, g); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Enroll(ctx, domain.EnrollRequest{Command: op(), Name: "late", InvitationToken: token, CredentialHash: domain.Hash(domain.NewToken())})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(ctx, f.workerToken)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Agents(ctx, f.worker.ID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Revoke(ctx, f.other.ID, f.founder.ID, domain.AgentRequest{Command: op()})
	assertCode(t, err, domain.Denied)
}

func TestTaskHistoryConcurrencyAndIdempotency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	originalSpec := task.SpecGeneration
	title := "revised"
	req := domain.TaskChange{Command: op(), Kind: "edit", Reason: "clarification", Title: &title}
	req.ExpectedRevision = task.Revision
	next, err := f.s.ChangeTask(ctx, f.other.ID, task.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if next.SpecGeneration != originalSpec+1 || next.Readiness != nil || next.Clarity != nil || next.Revision != task.Revision+1 {
		t.Fatal("approval/revision invalidation", next)
	}
	replayed, err := f.s.ChangeTask(ctx, f.other.ID, task.ID, req)
	if err != nil || replayed.Revision != next.Revision {
		t.Fatal("idempotency", err)
	}
	req.Reason = "different"
	_, err = f.s.ChangeTask(ctx, f.other.ID, task.ID, req)
	assertCode(t, err, domain.Conflict)
	req.Command = op()
	req.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.other.ID, task.ID, req)
	assertCode(t, err, domain.Conflict)
	history, err := f.s.Histories(ctx, f.worker.ID, task.ID)
	if err != nil || len(history) != 3 {
		t.Fatal("history retention", len(history), err)
	}
	seq := history[2].Sequence
	next = f.change(t, f.worker.ID, task.ID, "revert", func(r *domain.TaskChange) { r.HistorySequence = seq })
	if next.Readiness != nil || next.SpecGeneration != originalSpec+2 {
		t.Fatal("revert resurrected authority")
	}
	_, err = f.s.Histories(ctx, f.worker.ID, "session_private")
	assertCode(t, err, domain.Denied)
}

func TestApprovalAndSingleClaimAndVerification(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	req := domain.TaskChange{Command: op(), Kind: "approve", Reason: "forged", SpecGeneration: task.SpecGeneration}
	req.ExpectedRevision = task.Revision
	_, err := f.s.ChangeTask(ctx, f.worker.ID, task.ID, req)
	assertCode(t, err, domain.Denied)
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winner domain.Run
	success := 0
	for range 8 {
		wg.Go(func() {
			local := claim
			local.Command = op()
			local.ExpectedRevision = task.Revision
			run, err := f.s.Claim(ctx, f.worker.ID, task.ID, local)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				winner = run
			} else if domain.AsFault(err).Code != domain.Conflict {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success != 1 {
		t.Fatal("claims admitted", success)
	}
	report := domain.ReportRequest{Command: op(), RunID: winner.ID, Token: token, SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "fixture written", Evidence: []string{"fixture test passed"}}
	out, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || !out.Accepted || out.Task.Status != domain.Completed {
		t.Fatal(out, err)
	}
	replay, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || replay.Task.ResultGeneration != 1 {
		t.Fatal("report duplicated", err)
	}
	task = out.Task
	req = domain.TaskChange{Command: op(), Kind: "verify", Reason: "checks passed", SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration, Decision: "accept"}
	req.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.worker.ID, task.ID, req)
	assertCode(t, err, domain.Denied)
	task, err = f.s.ChangeTask(ctx, f.founder.ID, task.ID, req)
	if err != nil || task.Status != domain.Done {
		t.Fatal(task, err)
	}
}

func TestDiscussionMentionsAndBlockingAnswers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	q, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "Which value? @{" + f.founder.ID + "} @{" + f.founder.ID + "}"})
	if err != nil || len(q.Mentions) != 1 {
		t.Fatal(q, err)
	}
	attention, err := f.s.Attention(ctx, f.founder.ID, "", 1)
	if err != nil || len(attention.Items) != 1 {
		t.Fatal(attention, err)
	}
	task = f.change(t, f.worker.ID, task.ID, "waiting", func(r *domain.TaskChange) {
		r.SpecGeneration = task.SpecGeneration
		r.Waiting = &domain.Waiting{Kind: "clarification", Reason: "missing value", RequestedIDs: []string{f.founder.ID}, QuestionIDs: []string{q.ID}}
	})
	req := domain.TaskChange{Command: op(), Kind: "clarity", Reason: "guess", SpecGeneration: task.SpecGeneration}
	req.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.worker.ID, task.ID, req)
	assertCode(t, err, domain.Prerequisite)
	answer, err := f.s.CreateEntry(ctx, f.founder.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", ParentID: q.ID, Body: "Use the documented fixture value."})
	if err != nil {
		t.Fatal(err)
	}
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration; r.AnswerIDs = []string{answer.ID} })
	if task.Waiting != nil || task.Clarity == nil {
		t.Fatal("clarity not resolved")
	}
	edit := domain.EntryRequest{Command: op(), Body: "No mention anymore"}
	edit.ExpectedRevision = q.Revision
	_, err = f.s.EditEntry(ctx, f.founder.ID, q.ID, edit)
	assertCode(t, err, domain.Denied)
	if _, err = f.s.EditEntry(ctx, f.worker.ID, q.ID, edit); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.AttentionReceipt(ctx, f.founder.ID, q.ID, domain.AttentionRequest{Command: op(), Generation: q.Generation, State: "handled"})
	assertCode(t, err, domain.Conflict)
}

func TestUnsafeRetryAndStaleReports(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.ReportRequest{Command: op(), RunID: run.ID, Token: token, SpecGeneration: run.SpecGeneration, Outcome: "interrupted", Summary: "uncertain process exit"}
	out, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	task = out.Task
	req := domain.TaskChange{Command: op(), Kind: "clarity", Reason: "retry", SpecGeneration: task.SpecGeneration}
	req.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.worker.ID, task.ID, req)
	assertCode(t, err, domain.Prerequisite)
	f.now = f.now.Add(time.Minute)
	task = f.change(t, f.founder.ID, task.ID, "retry-approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration; r.RunID = run.ID; r.Attempt = 2 })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	claim.Command = op()
	claim.ExpectedRevision = task.Revision
	run, err = f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil || run.Attempt != 2 {
		t.Fatal(run, err)
	}
	desc := "changed after launch"
	task = f.change(t, f.founder.ID, task.ID, "edit", func(r *domain.TaskChange) { r.Description = &desc })
	report.Command = op()
	report.RunID = run.ID
	report.Outcome = "completed"
	out, err = f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || !out.Stale || out.Accepted || out.Task.Status == domain.Completed || out.Task.ActiveRun != nil {
		t.Fatal(out, err)
	}
}

func TestPrivateMessagesSessionConsentAndExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	message, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "private", Origin: "human", Kind: "direct", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Message(ctx, f.other.ID, message.ID)
	assertCode(t, err, domain.Denied)
	task := f.ready(t)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "authorize once for this requester/target"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, TaskID: task.ID, Mode: "tmux", Purpose: "terminal", Reason: "help", ExpiresAt: f.now.Unix() + 90})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target")
	assertCode(t, err, domain.Prerequisite)
	change := domain.SessionChange{Command: op(), Action: "open"}
	change.ExpectedRevision = session.Revision
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target"); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RetireSessions(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target")
	assertCode(t, err, domain.Prerequisite)
}
