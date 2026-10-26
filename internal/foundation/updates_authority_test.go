package foundation

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestUpdatesSessionsGrantsAndRetirementStayPartyPrivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	workerCursor := updateWatermark(t, f.s, f.worker.ID)
	founderCursor := updateWatermark(t, f.s, f.founder.ID)
	otherCursor := updateWatermark(t, f.s, f.other.ID)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Persistent isolated fixture authorization"})
	if err != nil {
		t.Fatal(err)
	}
	assertParties := func(topic, source string) {
		t.Helper()
		for actor, since := range map[string]string{f.worker.ID: workerCursor, f.founder.ID: founderCursor} {
			page := updateReplay(t, f.s, actor, since)
			if len(page.Items) != 1 || !updateContains(page, topic, source) || !page.Items[0].Urgent {
				t.Fatal("private session/grant change omitted its exact parties", actor, page)
			}
			if actor == f.worker.ID {
				workerCursor = page.NextCursor
			} else {
				founderCursor = page.NextCursor
			}
		}
		if unrelated := updateReplay(t, f.s, f.other.ID, otherCursor); len(unrelated.Items) != 0 {
			t.Fatal("session/grant ID leaked outside its parties", unrelated)
		}
		var shared int
		if err := f.s.db.QueryRow("SELECT count(*) FROM events WHERE source_id=? OR task_id=? OR project_id=?", source, source, source).Scan(&shared); err != nil || shared != 0 {
			t.Fatal("private session/grant ID leaked into shared events", shared, err)
		}
		assertUpdateChangesDrained(t, f.s)
	}
	assertParties("assistance", grant.ID)
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Private isolated session", ExpiresAt: f.now.Unix() + 120})
	if err != nil || session.State != "consented" {
		t.Fatal(session, err)
	}
	assertParties("session", session.ID)
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	assertParties("session", session.ID)
	session, err = f.s.ChangeSession(ctx, f.worker.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "active"})
	if err != nil {
		t.Fatal(err)
	}
	assertParties("session", session.ID)
	if _, err := f.s.RevokeAssistance(ctx, f.founder.ID, grant.ID, domain.AssistanceRevoke{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: grant.Revision}, Reason: "Withdraw this fixture authorization"}); err != nil {
		t.Fatal(err)
	}
	assertParties("assistance", grant.ID)
	if _, err := f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target"); err == nil {
		t.Fatal("hint delivery changed revoked session authority")
	}
	result, _ := startUpdateWait(t, f.s, ctx, f.worker.ID, workerCursor)
	updateSubscription(t, f.s, f.worker.ID, 1)
	// Restart retirement is not a mutate/transact operation.
	if err := f.s.RetireSessions(ctx); err != nil {
		t.Fatal(err)
	}
	retired := awaitUpdateWait(t, result)
	if retired.err != nil || !updateContains(retired.page, "session", session.ID) {
		t.Fatal("maintenance commit failed to wake its affected target", retired.page, retired.err)
	}
	assertParties("session", session.ID)
}

func TestUpdatesProjectArchiveNotifiesAssigneesAndSessionParties(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "archive scope"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.s.CreateTask(ctx, f.founder.ID, domain.TaskRequest{Command: op(), ProjectID: project.ID, Title: "archive fixture"})
	if err != nil {
		t.Fatal(err)
	}
	f.change(t, f.founder.ID, task.ID, "assign", func(req *domain.TaskChange) { req.AgentID = f.worker.ID })
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.other.ID, ProjectID: project.ID, Mode: "tmux", Purpose: "terminal", Reason: "Project-scoped fixture authorization"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, ProjectID: project.ID, TargetID: f.other.ID, Mode: "tmux", Purpose: "terminal", Reason: "Project-scoped session", ExpiresAt: f.now.Unix() + 120})
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string]string{
		f.worker.ID: updateWatermark(t, f.s, f.worker.ID),
		f.other.ID:  updateWatermark(t, f.s, f.other.ID),
	}
	archived := true
	if _, err := f.s.ChangeProject(ctx, f.founder.ID, project.ID, domain.ProjectChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, Archived: &archived, Reason: "Withdraw the complete project scope"}); err != nil {
		t.Fatal(err)
	}
	for actor, since := range cursors {
		page := updateReplay(t, f.s, actor, since)
		if len(page.Items) != 1 || !updateContains(page, "project", project.ID) || !page.Items[0].Urgent {
			t.Fatal("archive failed to notify task/session dependent", actor, page)
		}
		for _, update := range page.Items {
			if update.SourceID == session.ID || update.SourceID == grant.ID {
				t.Fatal("public project hint included another party's private source")
			}
		}
	}
	if _, err := f.s.SessionAccess(ctx, f.other.ID, session.ID, "target"); err == nil {
		t.Fatal("archive notification altered current scope enforcement")
	}
}

func TestUpdatesModeAndRunResultChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	since := updateWatermark(t, f.s, f.worker.ID)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	page := updateReplay(t, f.s, f.worker.ID, since)
	if !updateContains(page, "run", run.ID) || !updateContains(page, "task", task.ID) {
		t.Fatal("execution claim omitted task/run changes", page)
	}
	report, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token, SpecGeneration: run.SpecGeneration, Outcome: "completed", Summary: "Result fixture", Evidence: []string{"fixture checks"}})
	if err != nil {
		t.Fatal(err)
	}
	page = updateReplay(t, f.s, f.worker.ID, page.NextCursor)
	if !updateContains(page, "run", run.ID) || !updateContains(page, "task", task.ID) || report.Task.Status != domain.Completed {
		t.Fatal("completed report omitted result/run hints or accepted unverified work", page)
	}
	since = page.NextCursor
	mode, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Priority pause fixture"})
	if err != nil {
		t.Fatal(err)
	}
	page = updateReplay(t, f.s, f.worker.ID, since)
	if len(page.Items) != 1 || !updateContains(page, "agent-mode", mode.ID) || !page.Items[0].Urgent {
		t.Fatal("Founder mode change omitted urgent self notification", page)
	}
}

func TestUpdatesIdentityGenerationFencesOwnerChangesAndRevocation(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	worker, _, _ := f.register(t, "feed worker", 4251)
	other, _, _ := f.register(t, "unrelated feed worker", 4252)
	workerCursor := updateWatermark(t, f.s, worker.AgentID)
	otherCursor := updateWatermark(t, f.s, other.AgentID)
	result, _ := startUpdateWait(t, f.s, ctx, worker.AgentID, workerCursor)
	otherResult, cancelOther := startUpdateWait(t, f.s, ctx, other.AgentID, otherCursor)
	updateSubscription(t, f.s, worker.AgentID, 1)
	otherWake := updateSubscription(t, f.s, other.AgentID, 1)
	owner := securityPublic(t, "isolated owner")
	worker, err := f.s.SetIdentityOwner(ctx, worker.AgentID, owner.public.AuthKey, worker.Generation)
	if err != nil {
		t.Fatal(err)
	}
	delivered := awaitUpdateWait(t, result)
	if delivered.err != nil || !updateContains(delivered.page, "agent", worker.AgentID) || !delivered.page.Items[0].Urgent {
		t.Fatal("owner commit outside mutate failed to notify the current identity", delivered.page, delivered.err)
	}
	assertNotWoken(t, otherWake)
	oldOwnerCtx := WithIdentityFence(ctx, securityCertificate(worker, "owner"))
	oldOwnerCursor := delivered.page.NextCursor
	oldOwnerWait, _ := startUpdateWait(t, f.s, oldOwnerCtx, worker.AgentID, oldOwnerCursor)
	updateSubscription(t, f.s, worker.AgentID, 1)
	newOwner := securityPublic(t, "new isolated owner")
	worker, err = f.s.SetIdentityOwner(ctx, worker.AgentID, newOwner.public.AuthKey, worker.Generation)
	if err != nil {
		t.Fatal(err)
	}
	oldOwnerResult := awaitUpdateWait(t, oldOwnerWait)
	assertCode(t, oldOwnerResult.err, domain.Denied)
	if page := updateReplay(t, f.s, worker.AgentID, oldOwnerCursor); !updateContains(page, "agent", worker.AgentID) {
		t.Fatal("owner generation incorrectly retired the device key-generation cursor", page)
	}
	currentCursor := updateWatermark(t, f.s, worker.AgentID)
	banned, _ := startUpdateWait(t, f.s, ctx, worker.AgentID, currentCursor)
	updateSubscription(t, f.s, worker.AgentID, 1)
	// A ban is another security transaction that does not use mutate/transact.
	if err := f.s.BanIdentity(ctx, worker.AgentID, "isolated fixture ban"); err != nil {
		t.Fatal(err)
	}
	bannedResult := awaitUpdateWait(t, banned)
	assertCode(t, bannedResult.err, domain.Denied)
	assertNotWoken(t, otherWake)
	fresh := securityPublic(t, "fresh recovered feed worker")
	recovered, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, domain.RecoverIdentityRequest{Command: op(), Identity: fresh.public, Reason: "Explicit isolated fresh-key recovery"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, worker.UID)
	_, err = f.s.Updates(ctx, recovered.AgentID, currentCursor, 200)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.Updates(oldOwnerCtx, recovered.AgentID, "", 200)
	assertCode(t, err, domain.Denied)
	updateWatermark(t, f.s, recovered.AgentID)
	cancelOther()
	awaitUpdateWait(t, otherResult)
	assertUpdateChangesDrained(t, f.s)
}

func TestUpdatesRevocationAndHealthMaintenanceWakeAffectedSubscribers(t *testing.T) {
	t.Run("revocation", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		since := updateWatermark(t, f.s, f.worker.ID)
		result, _ := startUpdateWait(t, f.s, ctx, f.worker.ID, since)
		updateSubscription(t, f.s, f.worker.ID, 1)
		if _, err := f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()}); err != nil {
			t.Fatal(err)
		}
		assertCode(t, awaitUpdateWait(t, result).err, domain.Denied)
	})
	t.Run("health-delivery", func(t *testing.T) {
		f := newFixture(t)
		ctx := context.Background()
		h := observedHealth(f.now.Unix())
		h.Phase, h.ActionKind, h.ActionID = "invoking", "onboarding", "fixture"
		h.PhaseStartedAt, h.LastProgressAt, h.DeadlineAt = h.ObservedAt-400, h.ObservedAt-400, h.ObservedAt-35
		publishHealth(t, f.s, f.worker.ID, h)
		since := updateWatermark(t, f.s, f.founder.ID)
		otherCursor := updateWatermark(t, f.s, f.other.ID)
		result, _ := startUpdateWait(t, f.s, ctx, f.founder.ID, since)
		updateSubscription(t, f.s, f.founder.ID, 1)
		if err := f.s.SweepHealth(ctx); err != nil {
			t.Fatal(err)
		}
		delivered := awaitUpdateWait(t, result)
		if delivered.err != nil || len(delivered.page.Items) != 1 || delivered.page.Items[0].Topic != "message" {
			t.Fatal("health maintenance omitted its durable private delivery", delivered.page, delivered.err)
		}
		if page := updateReplay(t, f.s, f.other.ID, otherCursor); len(page.Items) != 0 {
			t.Fatal("health maintenance woke an unrelated identity", page)
		}
		count := updateCount(t, f.s)
		f.now = f.now.Add(time.Second)
		if err := f.s.SweepHealth(ctx); err != nil || updateCount(t, f.s) != count {
			t.Fatal("unchanged health sweep produced business churn", err)
		}
	})
}

func TestUpdatesReadOnlyControlSnapshotDoesNotAcquireWriterLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	operation, _ := requestDiagnosticOperation(t, f)
	message := sendUpdateMessage(t, f, f.worker.ID)
	writer, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := f.s.GetAgentOperation(readCtx, f.founder.ID, operation.ID); err != nil {
		t.Fatal("bodyless operation GET acquired an immediate writer transaction", err)
	}
	page, err := f.s.ListAgentOperations(readCtx, f.worker.ID, "", 200)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != operation.ID {
		t.Fatal("control-list GET failed to retain a consistent read-only snapshot", page, err)
	}
	if _, err := f.s.Updates(readCtx, f.worker.ID, "", 200); err != nil {
		t.Fatal("update snapshot acquired an immediate writer transaction", err)
	}
	if _, err := f.s.AgentDiagnostics(readCtx, f.founder.ID, f.worker.ID); err != nil {
		t.Fatal("agent diagnostics acquired an immediate writer transaction", err)
	}
	if _, err := f.s.MessageDiagnostics(readCtx, f.founder.ID, message.ID); err != nil {
		t.Fatal("message diagnostics acquired an immediate writer transaction", err)
	}
}

func TestUpdatesWriterAdmissionLeavesReplayCapacity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	workerCursor := updateWatermark(t, f.s, f.worker.ID)
	otherCursor := updateWatermark(t, f.s, f.other.ID)
	const subscribers, writers = 24, 8
	idle := make([]<-chan updateWaitResult, subscribers)
	cancels := make([]context.CancelFunc, subscribers)
	for i := range idle {
		idle[i], cancels[i] = startUpdateWait(t, f.s, ctx, f.other.ID, otherCursor)
	}
	otherWake := updateSubscription(t, f.s, f.other.ID, subscribers)
	deliveries, _ := startUpdateWait(t, f.s, ctx, f.worker.ID, workerCursor)
	updateSubscription(t, f.s, f.worker.ID, 1)

	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	held := make(chan error, 1)
	go func() {
		_, err := mutate(ctx, f.s, f.founder.ID, "fixture.hold-writer", op(), nil, func(*sql.Tx, domain.Agent) (domain.Message, error) {
			close(entered)
			<-release
			return domain.Message{}, domain.Fail(domain.Conflict, "release fixture writer without a business change")
		})
		held <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture writer did not acquire its transaction")
	}
	started := make(chan struct{}, writers)
	results := make(chan error, writers)
	for range writers {
		go func() {
			started <- struct{}{}
			_, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Kind: "direct", Origin: "human", Body: "Competing writer fixture"})
			results <- err
		}()
	}
	for range writers {
		<-started
	}
	time.Sleep(50 * time.Millisecond)
	before := f.s.db.Stats()
	readCtx, cancelRead := context.WithTimeout(ctx, time.Second)
	page, readErr := f.s.Updates(readCtx, f.worker.ID, workerCursor, 200)
	cancelRead()
	t.Logf("queued_writers=%d idle_subscribers=%d pool_limit=%d in_use=%d pool_waits=%d replay_read_error=%v",
		writers, subscribers, before.MaxOpenConnections, before.InUse, before.WaitCount, readErr)
	releaseWriter()
	assertCode(t, <-held, domain.Conflict)
	for range writers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("competing writer did not finish")
		}
	}
	delivered := awaitUpdateWait(t, deliveries)
	if delivered.err != nil || len(delivered.page.Items) == 0 {
		t.Fatal("accepted writes did not wake their affected subscriber", delivered.page, delivered.err)
	}
	if replay := updateReplay(t, f.s, f.worker.ID, workerCursor); len(replay.Items) != writers {
		t.Fatal("writer admission lost or duplicated durable changes", replay)
	}
	assertNotWoken(t, otherWake)
	for i, result := range idle {
		cancels[i]()
		if canceled := awaitUpdateWait(t, result); !errors.Is(canceled.err, context.Canceled) {
			t.Fatal("unrelated idle subscriber did not remain cancellable", canceled.err)
		}
	}
	if readErr != nil || len(page.Items) != 0 || before.InUse != 1 {
		t.Fatal("queued SQLite writers monopolized connections needed by replay readers", before.InUse, page, readErr)
	}
	assertUpdateChangesDrained(t, f.s)
}

func TestUpdatesWriterAdmissionCancellationAndClose(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, finish, err := f.s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	canceledCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	request := domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Kind: "direct", Origin: "human", Body: "Canceled before writer admission"}
	if _, err := f.s.SendMessage(canceledCtx, f.founder.ID, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued writer ignored context cancellation", err)
	}
	if stats := f.s.db.Stats(); stats.InUse != 1 {
		t.Fatal("canceled admission leased an extra connection", stats)
	}
	var receipts int
	if err := f.s.db.QueryRow("SELECT count(*) FROM operations WHERE operation_id=?", request.OperationID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("canceled admission changed operation state", receipts, err)
	}
	queued := make(chan error, 1)
	go func() {
		_, err := f.s.SendMessage(ctx, f.founder.ID, request)
		queued <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- f.s.Close() }()
	select {
	case err := <-queued:
		assertCode(t, err, domain.Unavailable)
	case <-time.After(time.Second):
		t.Fatal("Store.Close did not cancel queued writer admission")
	}
	finish()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Store.Close did not finish after releasing its transaction")
	}
	if len(f.s.writer) != 0 {
		t.Fatal("finished transaction retained writer admission")
	}
	_, err = f.s.SendMessage(ctx, f.founder.ID, request)
	assertCode(t, err, domain.Unavailable)
}

func TestUpdatesPrivateRoutingDoesNotLoadFleetAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	tx, finish, err := f.s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	router := updateRouter{ctx: ctx, tx: tx, projects: make(map[string]domain.Project), tasks: make(map[string]domain.Task), updateKeys: make(map[updateRouteKey]int)}
	for _, private := range []struct {
		table, id string
		data      any
	}{
		{"messages", "message_fixture", domain.Message{SenderID: f.worker.ID, RecipientID: f.other.ID}},
		{"sessions", "session_fixture", domain.Session{RequesterID: f.worker.ID, TargetID: f.other.ID, ProjectID: task.ProjectID}},
		{"assistance_grants", "grant_fixture", domain.AssistanceGrant{RequesterID: f.worker.ID, TargetID: f.other.ID, CreatedBy: f.founder.ID, ProjectID: task.ProjectID}},
		{"attention", "attention_fixture", map[string]any{"agent_id": f.other.ID, "entry_id": "comment_fixture", "task_id": task.ID}},
	} {
		data, err := encode(private.data)
		if err != nil {
			t.Fatal(err)
		}
		if err := router.route(updateChange{table: private.table, id: private.id, after: data}); err != nil {
			t.Fatal(err)
		}
		if router.foundersLoaded {
			t.Fatal("party-private routing scanned fleet-wide Founder authority", private.table)
		}
	}
	publicParties := make(updateParties)
	if err := router.project(task.ProjectID, publicParties); err != nil {
		t.Fatal(err)
	}
	if _, ok := publicParties[f.founder.ID]; !ok || !router.foundersLoaded {
		t.Fatal("lazy routing failed to check current public authority", publicParties)
	}
}
