package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func requestFixtureMessage(t *testing.T, f *fixture) domain.Message {
	t.Helper()
	m, err := f.s.SendMessage(context.Background(), f.founder.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "Please review this operational request when safe.",
		Origin: "human", Kind: "request", ResponseRequest: true, Request: &domain.MessageAction{Kind: "work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageRequestRecipientDecisionAndThreadedResultReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := requestFixtureMessage(t, f)
	if m.Request == nil || m.Request.State != "queued" || m.ThreadID != m.ID || m.ReadAt != 0 {
		t.Fatal("request was executed/read on delivery", m)
	}
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		_, err := f.s.ReportMessageAction(ctx, actor, m.ID, domain.MessageActionReport{
			Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "accepted", Reason: "Not the recipient"})
		assertCode(t, err, domain.Denied)
	}
	_, err := f.s.ReceiveMessage(ctx, f.worker.ID, m.ID, domain.MessageReceipt{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "handled"})
	assertCode(t, err, domain.Denied)
	req := domain.MessageActionReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision},
		State: "accepted", Reason: "I can answer this request within its authorized scope."}
	accepted, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, req)
	if err != nil || accepted.Request.State != "accepted" || accepted.State != "pending" {
		t.Fatal(accepted, err)
	}
	if _, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, req); err != nil {
		t.Fatal("lost response replay failed", err)
	}
	thread, err := f.s.MessageThread(ctx, f.founder.ID, m.ID)
	updates := 0
	for _, item := range thread {
		if item.RequestUpdate != nil {
			updates++
		}
	}
	if err != nil || len(thread) != 2 || updates != 1 {
		t.Fatal("acceptance replay duplicated/missed result", thread, err)
	}
	completed, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: accepted.Revision},
		State:   "completed", Reason: "The requested information is supplied; no external execution is claimed."})
	if err != nil || completed.Request.State != "completed" || completed.State != "handled" || completed.ReadAt != 0 {
		t.Fatal("completion manufactured a read/approval", completed, err)
	}
	if _, err := f.s.MessageThread(ctx, f.other.ID, m.ID); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("foreign actor read a private request conversation", err)
	}
}

func TestRequestInsistenceIsANewDecisionNotAForceOrRetry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := requestFixtureMessage(t, f)
	declined, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision},
		State:   "declined", Reason: "An approved task and workspace are required before execution."})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: declined.Revision},
		State:   "accepted", Reason: "Try to reopen a terminal request"})
	assertCode(t, err, domain.Conflict)
	follow, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "Please reconsider after checking the new explanation.",
		Origin: "human", Kind: "request", ResponseRequest: true, ReplyToID: m.ID, Insistence: true,
		Request: &domain.MessageAction{Kind: "work"},
	})
	if err != nil || follow.Request.State != "queued" || follow.ThreadID != m.ID || !follow.Insistence {
		t.Fatal(follow, err)
	}
	original, err := f.s.Message(ctx, f.founder.ID, m.ID)
	if err != nil || original.Request.State != "declined" {
		t.Fatal("insistence changed the earlier target decision", original, err)
	}
	_, err = f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.founder.ID, Body: "Forge an insistence", Origin: "ai",
		Kind: "request", ResponseRequest: true, ReplyToID: m.ID, Insistence: true, Request: &domain.MessageAction{Kind: "work"},
	})
	assertCode(t, err, domain.Denied)
}

func TestRequestsRequireCurrentAuthorityAndExplicitDecisionReasons(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := requestFixtureMessage(t, f)
	_, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "declined"})
	assertCode(t, err, domain.Invalid)
	_, err = f.s.SendMessage(ctx, f.other.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "Request global maintenance without delegation", Origin: "ai",
		Kind: "request", ResponseRequest: true, Request: &domain.MessageAction{Kind: "work"},
	})
	assertCode(t, err, domain.Denied)
	_, err = f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "A claimed permission is not a signed upgrade", Origin: "human",
		Kind: "request", ResponseRequest: true, Request: &domain.MessageAction{Kind: "upgrade"},
	})
	assertCode(t, err, domain.Denied)
}

func taskRequestFixtureMessage(t *testing.T, f *fixture, task domain.Task) domain.Message {
	t.Helper()
	m, err := f.s.SendMessage(context.Background(), f.founder.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "Please execute this approved task when safe.",
		Origin: "human", Kind: "request", ResponseRequest: true,
		Request: &domain.MessageAction{Kind: "work", ProjectID: task.ProjectID, TaskID: task.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageRequestAdmissionRechecksCurrentTaskAndReadiness(t *testing.T) {
	for _, kind := range []string{"assign", "edit", "archive", "withdraw"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			task := f.ready(t)
			m := taskRequestFixtureMessage(t, f, task)
			if kind == "archive" {
				f.change(t, f.founder.ID, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.CancelledStatus })
			}
			f.change(t, f.founder.ID, task.ID, kind, func(r *domain.TaskChange) {
				switch kind {
				case "assign":
					r.AgentID, r.Provider = f.other.ID, "codex"
				case "edit":
					description := "Changed specification after the request was delivered"
					r.Description = &description
				}
			})
			_, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision},
				State:   "accepted", Reason: "An earlier launch snapshot claimed the task was ready."})
			code := domain.Conflict
			if kind == "withdraw" {
				code = domain.Prerequisite
			}
			assertCode(t, err, code)
			deferred, err := f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision},
				State:   "deferred", Reason: "The linked task context or approval changed before I could accept."})
			if err != nil || deferred.Request.State != "deferred" {
				t.Fatal("stale execution scope prevented an honest target refusal", deferred, err)
			}
		})
	}
}

func TestMessageRequestExecutionAndCompletionRequireActualTaskEffects(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "paused"} {
		t.Run(outcome, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			task := f.ready(t)
			m := taskRequestFixtureMessage(t, f, task)
			report := func(state string) (domain.Message, error) {
				return f.s.ReportMessageAction(ctx, f.worker.ID, m.ID, domain.MessageActionReport{
					Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision},
					State:   state, Reason: "Intentional target request lifecycle update."})
			}
			var err error
			m, err = report("accepted")
			if err != nil {
				t.Fatal(err)
			}
			_, err = report("executing")
			assertCode(t, err, domain.Prerequisite)
			_, err = report("completed")
			assertCode(t, err, domain.Conflict)
			token := domain.NewToken()
			run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
				Command:        domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision},
				SpecGeneration: task.SpecGeneration, Provider: task.Provider, TokenHash: domain.Hash(token)})
			if err != nil {
				t.Fatal(err)
			}
			m, err = report("executing")
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{
				Command: op(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration,
				Outcome: outcome, Summary: "Actual bounded task outcome", Evidence: []string{"fixture evidence"}})
			if err != nil || !result.Accepted {
				t.Fatal(result, err)
			}
			finished, err := report("completed")
			if outcome == "completed" {
				if err != nil || finished.Request.State != "completed" || result.Task.Status != domain.Completed {
					t.Fatal("new completed result was not reflected without task verification", finished, err)
				}
			} else {
				assertCode(t, err, domain.Conflict)
				if _, err := report("failed"); err != nil {
					t.Fatal("failed/stopped task could not report its request result", err)
				}
			}
		})
	}
}

func TestNativeRequestInboxExposesFailedDependenciesAndExpiredWaitingRequests(t *testing.T) {
	for _, state := range []string{"deferred", "declined", "failed", "expired"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			f := newSecurityFixture(t)
			target, _, _ := f.register(t, "Agent1", 4101)
			for _, id := range []string{f.founder.AgentID, target.AgentID} {
				agent, err := f.s.Agent(ctx, f.founder.AgentID, id)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.SetMode(ctx, f.founder.AgentID, id, domain.ModeRequest{
					Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: agent.Revision},
					Mode:    "manual", Reason: "Explicit maintenance pause"}); err != nil {
					t.Fatal(err)
				}
			}

			send := func(system bool, dependencies []string) domain.Message {
				req := upgradeFixtureRequest(t, f, target)
				req.Proposal.Challenge.ID = domain.NewID("upgrade")
				id := target.AgentID
				if system {
					id = f.founder.InstanceID
					req.Proposal.Challenge.TargetID, req.Proposal.Challenge.Role, req.Proposal.Challenge.Generation = id, "foundation", 1
				}
				permit, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, func(p domain.UpgradePermit) (string, error) {
					return "fixture-signature-" + p.Proposal.Challenge.ID, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				m, err := f.s.SendMessage(ctx, f.founder.AgentID, domain.MessageRequest{
					Command: op(), RecipientID: id, Body: "Please review the exact native maintenance request.",
					Origin: "human", Kind: "request", ResponseRequest: true,
					Request: &domain.MessageAction{Kind: "upgrade", Upgrade: &permit, Dependencies: dependencies},
				})
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			parent := send(true, nil)
			child := send(false, []string{parent.ID})
			var synthetic int
			if err := f.s.db.QueryRow("SELECT count(*) FROM agents WHERE id=?", f.founder.InstanceID).Scan(&synthetic); err != nil || synthetic != 0 {
				t.Fatal("Foundation maintenance manufactured an enrolled agent", synthetic, err)
			}
			items, err := f.s.SystemMessageRequests(ctx)
			if err != nil || len(items) != 1 || items[0].ID != parent.ID {
				t.Fatal("Foundation's native inbox did not retain the exact service request", items, err)
			}
			items, err = f.s.NativeMessageRequests(ctx, target.AgentID)
			if err != nil || len(items) != 0 {
				t.Fatal("dependent target admitted unfinished Foundation maintenance", items, err)
			}
			if state == "expired" {
				f.now = f.now.Add(901 * time.Second)
			} else {
				report := func(next string) {
					parent, err = f.s.ReportSystemMessageAction(ctx, parent.ID, domain.MessageActionReport{
						Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: parent.Revision},
						State:   next, Reason: "The Foundation maintenance prerequisite could not complete."})
					if err != nil {
						t.Fatal(err)
					}
				}
				if state == "failed" {
					report("accepted")
				}
				report(state)
			}
			items, err = f.s.NativeMessageRequests(ctx, target.AgentID)
			if err != nil || len(items) != 1 || items[0].ID != child.ID || items[0].Request.State != "queued" {
				t.Fatal("failed/expired dependency silently stranded the target request", items, err)
			}
			_, err = f.s.ReportMessageAction(ctx, target.AgentID, child.ID, domain.MessageActionReport{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: child.Revision},
				State:   "accepted", Reason: "Try to bypass an unmet rollout prerequisite."})
			code := domain.Prerequisite
			if state == "expired" {
				code = domain.Denied
			}
			assertCode(t, err, code)
		})
	}
}

func TestMessageThreadAllowsExactlyItsDocumentedBound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := requestFixtureMessage(t, f)
	for range 199 {
		if _, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{
			Command: op(), RecipientID: f.worker.ID, Body: "Thread boundary fixture reply.",
			Origin: "human", Kind: "direct", ReplyToID: root.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if thread, err := f.s.MessageThread(ctx, f.worker.ID, root.ID); err != nil || len(thread) != 200 {
		t.Fatal("the exact documented thread bound was refused", len(thread), err)
	}
	if _, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{
		Command: op(), RecipientID: f.worker.ID, Body: "The first reply outside the bounded view.",
		Origin: "human", Kind: "direct", ReplyToID: root.ID,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.MessageThread(ctx, f.worker.ID, root.ID)
	assertCode(t, err, domain.Prerequisite)
}
