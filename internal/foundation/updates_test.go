package foundation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func updateWatermark(t *testing.T, s *Store, actor string) string {
	t.Helper()
	page, err := s.WaitUpdates(context.Background(), actor, "", 200)
	if err != nil || len(page.Items) != 0 || page.NextCursor == "" {
		t.Fatal("initial watermark must return immediately without historical hints", page, err)
	}
	return page.NextCursor
}

func seedUpdateIdentity(t *testing.T, db *sql.DB, agent domain.Agent) {
	t.Helper()
	keys := securityPublic(t, "fixture")
	sshFingerprint, authFingerprint, _, err := validateIdentityKeys(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.AgentIdentity{AgentID: agent.ID, InstanceID: "instance_retained", RequestID: keys.public.RequestID,
		SSHUser: "fixture", UID: 4100, Generation: 1, State: "active", Assurance: "software",
		SSHKey: keys.public.SSHKey, AuthKey: keys.public.AuthKey, SSHFingerprint: sshFingerprint, AuthFingerprint: authFingerprint}
	data, err := encode(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO agent_identities(id,data,ssh_user,uid) VALUES(?,?,?,?)", agent.ID, data, identity.SSHUser, identity.UID); err != nil {
		t.Fatal(err)
	}
}

func updateReplay(t *testing.T, s *Store, actor, since string) domain.Page[domain.Update] {
	t.Helper()
	page, err := s.Updates(context.Background(), actor, since, 200)
	if err != nil || page.NextCursor == "" {
		t.Fatal("replay failed or omitted its continuation watermark", page, err)
	}
	return page
}

func updateContains(page domain.Page[domain.Update], topic, source string) bool {
	for _, update := range page.Items {
		if update.Topic == topic && update.SourceID == source {
			return true
		}
	}
	return false
}

func updateCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM update_feed").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertUpdateChangesDrained(t *testing.T, s *Store) {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM update_changes").Scan(&n); err != nil || n != 0 {
		t.Fatal("committed transaction retained its private change set", n, err)
	}
}

func sendUpdateMessage(t *testing.T, f *fixture, recipient string) domain.Message {
	t.Helper()
	message, err := f.s.SendMessage(context.Background(), f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: recipient, Kind: "direct", Origin: "human", Body: "Private fixture text, never a shared event or update body"})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

type updateWaitResult struct {
	page domain.Page[domain.Update]
	err  error
}

func startUpdateWait(t *testing.T, s *Store, ctx context.Context, actor, since string) (<-chan updateWaitResult, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan updateWaitResult, 1)
	go func() {
		page, err := s.WaitUpdates(ctx, actor, since, 200)
		result <- updateWaitResult{page, err}
	}()
	return result, cancel
}

func awaitUpdateWait(t *testing.T, result <-chan updateWaitResult) updateWaitResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("update waiter did not unblock")
		return updateWaitResult{}
	}
}

func updateSubscription(t *testing.T, s *Store, actor string, refs int) <-chan struct{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.updates.mu.Lock()
		waiter := s.updates.waiters[actor]
		if waiter != nil && waiter.refs == refs {
			wake := waiter.wake
			s.updates.mu.Unlock()
			return wake
		}
		s.updates.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("update subscription did not reach expected reference count", refs)
	return nil
}

func assertNotWoken(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
		t.Fatal("unaffected subscriber was woken")
	default:
	}
}

func TestUpdatesInitialWatermarkScopedDeliveryAndReconnect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	workerCursor := updateWatermark(t, f.s, f.worker.ID)
	founderCursor := updateWatermark(t, f.s, f.founder.ID)
	otherCursor := updateWatermark(t, f.s, f.other.ID)
	message := sendUpdateMessage(t, f, f.worker.ID)
	worker := updateReplay(t, f.s, f.worker.ID, workerCursor)
	founder := updateReplay(t, f.s, f.founder.ID, founderCursor)
	other := updateReplay(t, f.s, f.other.ID, otherCursor)
	if len(worker.Items) != 1 || !updateContains(worker, "message", message.ID) || !reflect.DeepEqual(worker.Items, founder.Items) || len(other.Items) != 0 {
		t.Fatal("private message was not confined to its parties", worker, founder, other)
	}
	if updateContains(worker, "task", task.ID) {
		t.Fatal("initial watermark replayed old work")
	}
	var path string
	if err := f.s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed := updateReplay(t, reopened, f.worker.ID, workerCursor)
	if !reflect.DeepEqual(worker, replayed) {
		t.Fatal("reconnect lost durable replay or changed the cursor identity", worker, replayed)
	}
	if page := updateReplay(t, reopened, f.worker.ID, worker.NextCursor); len(page.Items) != 0 {
		t.Fatal("acknowledged watermark replayed a duplicate", page)
	}
	assertUpdateChangesDrained(t, reopened)
}

func TestUpdatesWaitScopedWithoutHeartbeatChurnOrIdleSQL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	workerCursor := updateWatermark(t, f.s, f.worker.ID)
	otherCursor := updateWatermark(t, f.s, f.other.ID)
	worker, _ := startUpdateWait(t, f.s, ctx, f.worker.ID, workerCursor)
	other, cancelOther := startUpdateWait(t, f.s, ctx, f.other.ID, otherCursor)
	workerWake := updateSubscription(t, f.s, f.worker.ID, 1)
	otherWake := updateSubscription(t, f.s, f.other.ID, 1)
	count := updateCount(t, f.s)
	h := observedHealth(f.now.Unix())
	for range 3 {
		if _, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil {
			t.Fatal(err)
		}
		publishHealth(t, f.s, f.worker.ID, h)
	}
	publishStatus(t, f.s, f.worker.ID, "Waiting for authorized work")
	publishStatus(t, f.s, f.worker.ID, "Waiting for authorized work")
	if updateCount(t, f.s) != count {
		t.Fatal("ordinary liveness or unchanged status produced business churn")
	}
	assertNotWoken(t, workerWake)
	assertNotWoken(t, otherWake)
	// One pool connection is sufficient even with idle long-lived waiters.
	f.s.db.SetMaxOpenConns(1)
	pingCtx, cancelPing := context.WithTimeout(ctx, time.Second)
	defer cancelPing()
	if err := f.s.Ping(pingCtx); err != nil {
		t.Fatal("idle update wait retained a SQL connection", err)
	}
	message := sendUpdateMessage(t, f, f.worker.ID)
	delivered := awaitUpdateWait(t, worker)
	if delivered.err != nil || !updateContains(delivered.page, "message", message.ID) {
		t.Fatal("affected waiter missed its committed change", delivered)
	}
	assertNotWoken(t, otherWake)
	select {
	case value := <-other:
		t.Fatal("unrelated actor's blocking request returned", value)
	default:
	}
	cancelOther()
	if result := awaitUpdateWait(t, other); !errors.Is(result.err, context.Canceled) {
		t.Fatal("cancellation did not unblock an unrelated idle subscriber", result)
	}
	assertUpdateChangesDrained(t, f.s)
}

func TestUpdatesAssignmentWithdrawalIncludesOldAndNewParties(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "assignment withdrawal"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.s.CreateTask(ctx, f.founder.ID, domain.TaskRequest{Command: op(), ProjectID: project.ID, Title: "Founder-created task"})
	if err != nil {
		t.Fatal(err)
	}
	task = f.change(t, f.founder.ID, task.ID, "assign", func(req *domain.TaskChange) { req.AgentID = f.worker.ID })
	oldCursor := updateWatermark(t, f.s, f.worker.ID)
	newCursor := updateWatermark(t, f.s, f.other.ID)
	task = f.change(t, f.founder.ID, task.ID, "assign", func(req *domain.TaskChange) { req.AgentID = f.other.ID })
	for actor, since := range map[string]string{f.worker.ID: oldCursor, f.other.ID: newCursor} {
		if page := updateReplay(t, f.s, actor, since); !updateContains(page, "task", task.ID) {
			t.Fatal("assignment withdrawal omitted a former/current assignee", actor, page)
		}
	}
}

func TestUpdatesDelegateWithdrawalAndFormerActiveVerifier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token, SpecGeneration: run.SpecGeneration, Outcome: "completed", Summary: "completed fixture", Evidence: []string{"isolated fixture check"}})
	if err != nil {
		t.Fatal(err)
	}
	task = report.Task
	project, err := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := f.s.Claim(ctx, f.other.ID, task.ID, domain.ClaimRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision},
		Kind: domain.VerificationRun, SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration, ProjectEpoch: project.Epoch, TokenHash: domain.Hash(domain.NewToken()), Provider: "copilot"})
	if err != nil {
		t.Fatal(err)
	}
	since := updateWatermark(t, f.s, f.other.ID)
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true})
	if err != nil {
		t.Fatal(err)
	}
	page := updateReplay(t, f.s, f.other.ID, since)
	if len(page.Items) != 1 || !updateContains(page, "project", project.ID) || !page.Items[0].Urgent {
		t.Fatal("former delegate did not receive urgent authority withdrawal", page)
	}
	since = page.NextCursor
	title := "new specification after delegation withdrawal"
	task = f.change(t, f.founder.ID, task.ID, "edit", func(req *domain.TaskChange) { req.Title = &title })
	if task.ActiveRun == nil || task.ActiveRun.ID != verification.ID || !task.ActiveRun.StopRequested {
		t.Fatal("fixture did not retain its active verification fence", task)
	}
	page = updateReplay(t, f.s, f.other.ID, since)
	if len(page.Items) != 1 || !updateContains(page, "task", task.ID) || !page.Items[0].Urgent {
		t.Fatal("current active verifier was omitted after losing delegation", page)
	}
}

func TestUpdatesPrivateReadDispositionAuthenticationAndOperations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	message := sendUpdateMessage(t, f, f.worker.ID)
	founderCursor := updateWatermark(t, f.s, f.founder.ID)
	workerCursor := updateWatermark(t, f.s, f.worker.ID)
	otherCursor := updateWatermark(t, f.s, f.other.ID)
	for _, change := range []string{"read", "disposition", "authentication", "operation-queued", "operation-checking", "operation-result"} {
		t.Run(change, func(t *testing.T) {
			var source, topic string
			switch change {
			case "read":
				read, err := f.s.ReadMessage(ctx, f.worker.ID, message.ID, domain.MessageReadRequest{Command: op()})
				if err != nil || read.ReadAt == 0 || read.State != "pending" || read.Revision != message.Revision {
					t.Fatal("explicit read semantics changed", read, err)
				}
				source, topic = read.ID, "message"
			case "disposition":
				received, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "handled"})
				if err != nil || received.ReadAt == 0 {
					t.Fatal("disposition lost explicit reading", received, err)
				}
				source, topic = received.ID, "message"
			case "authentication":
				refresh := authenticationMessageFixture(t, f, "authentication-refresh")
				source, topic = refresh.ID, "message"
			default:
				var operation domain.AgentOperation
				if change == "operation-queued" {
					operation, _ = requestDiagnosticOperation(t, f)
					message.ID = operation.ID
				} else {
					var err error
					operation, err = f.s.GetAgentOperation(ctx, f.founder.ID, message.ID)
					if err != nil {
						t.Fatal(err)
					}
					if change == "operation-checking" {
						operation, _ = checkingAgentOperation(t, f, operation)
					} else {
						operation, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, diagnosticOperationReport(f, operation))
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				source, topic = operation.ID, "agent-operation"
			}
			founder := updateReplay(t, f.s, f.founder.ID, founderCursor)
			worker := updateReplay(t, f.s, f.worker.ID, workerCursor)
			if len(founder.Items) != 1 || !updateContains(founder, topic, source) || !reflect.DeepEqual(founder.Items, worker.Items) {
				t.Fatal("private mutation failed to notify both exact parties", founder, worker)
			}
			if topic == "agent-operation" && !worker.Items[0].Urgent {
				t.Fatal("protected operation was not urgent", worker)
			}
			if unrelated := updateReplay(t, f.s, f.other.ID, otherCursor); len(unrelated.Items) != 0 {
				t.Fatal("unrelated actor received a private source reference", unrelated)
			}
			var shared int
			if err := f.s.db.QueryRow("SELECT count(*) FROM events WHERE source_id=? OR task_id=? OR project_id=?", source, source, source).Scan(&shared); err != nil || shared != 0 {
				t.Fatal("private source ID leaked into shared events", shared, err)
			}
			encoded, err := encode(worker.Items)
			if err != nil || strings.Contains(string(encoded), "Private fixture text") || strings.Contains(string(encoded), "observations-collected") {
				t.Fatal("private body/result leaked into bodyless hints", string(encoded), err)
			}
			founderCursor, workerCursor = founder.NextCursor, worker.NextCursor
			assertUpdateChangesDrained(t, f.s)
		})
	}
}

func TestUpdatesDiscussionAttentionGenerationsAndReceipts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	since := updateWatermark(t, f.s, f.other.ID)
	entry, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "Explicit question @{" + f.other.ID + "}"})
	if err != nil {
		t.Fatal(err)
	}
	page := updateReplay(t, f.s, f.other.ID, since)
	if !updateContains(page, "comment", entry.ID) || !updateContains(page, "attention", entry.ID) {
		t.Fatal("new mention omitted discussion or attention hints", page)
	}
	founderCursor := updateWatermark(t, f.s, f.founder.ID)
	receipt := domain.AttentionRequest{Command: op(), Generation: entry.Generation, State: "handled"}
	if _, err := f.s.AttentionReceipt(ctx, f.other.ID, entry.ID, receipt); err != nil {
		t.Fatal(err)
	}
	received := updateReplay(t, f.s, f.other.ID, page.NextCursor)
	if len(received.Items) != 1 || !updateContains(received, "attention", entry.ID) {
		t.Fatal("attention disposition omitted its scoped hint", received)
	}
	if founder := updateReplay(t, f.s, f.founder.ID, founderCursor); len(founder.Items) != 0 {
		t.Fatal("personal attention disposition woke unrelated task authority", founder)
	}
	count := updateCount(t, f.s)
	receipt.Command = op()
	if _, err := f.s.AttentionReceipt(ctx, f.other.ID, entry.ID, receipt); err != nil || updateCount(t, f.s) != count {
		t.Fatal("unchanged attention disposition produced a duplicate hint", err)
	}
	_, err = f.s.EditEntry(ctx, f.worker.ID, entry.ID, domain.EntryRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: entry.Revision}, Body: "Question edited; former mention removed"})
	if err != nil {
		t.Fatal(err)
	}
	page = updateReplay(t, f.s, f.other.ID, received.NextCursor)
	if !updateContains(page, "comment", entry.ID) || !updateContains(page, "attention", entry.ID) {
		t.Fatal("old mention recipient missed its generation withdrawal", page)
	}
}

func TestUpdatesRollbackStorageFailureAndExactReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	since := updateWatermark(t, f.s, f.worker.ID)
	waiter, cancel := startUpdateWait(t, f.s, ctx, f.worker.ID, since)
	wake := updateSubscription(t, f.s, f.worker.ID, 1)
	count := updateCount(t, f.s)
	staged := domain.Message{ID: domain.NewID("message"), SenderID: f.founder.ID, RecipientID: f.worker.ID, Body: "Rolled back private fixture", Kind: "direct", Origin: "human", State: "pending", Revision: 1, CreatedAt: f.now.Unix()}
	_, err := mutate(ctx, f.s, f.founder.ID, "fixture.rollback", op(), nil, func(tx *sql.Tx, _ domain.Agent) (domain.Message, error) {
		if _, err := insertMessage(ctx, tx, staged); err != nil {
			return staged, err
		}
		return staged, domain.Fail(domain.Conflict, "refuse after staged mutation")
	})
	assertCode(t, err, domain.Conflict)
	if updateCount(t, f.s) != count {
		t.Fatal("savepoint rollback produced a phantom hint")
	}
	assertNotWoken(t, wake)
	assertUpdateChangesDrained(t, f.s)
	if _, err := f.s.db.Exec("CREATE TRIGGER fixture_refuse_update BEFORE INSERT ON update_feed BEGIN SELECT RAISE(ABORT,'fixture feed storage failure'); END"); err != nil {
		t.Fatal(err)
	}
	req := domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "Atomic private fixture", Origin: "human", Kind: "direct"}
	if _, err := f.s.SendMessage(ctx, f.founder.ID, req); err == nil {
		t.Fatal("business mutation committed without its durable feed")
	}
	var operations, messages int
	if err := f.s.db.QueryRow("SELECT count(*) FROM operations WHERE operation_id=?", req.OperationID).Scan(&operations); err != nil || operations != 0 {
		t.Fatal("failed feed retained an operation receipt", operations, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&messages); err != nil || messages != 0 {
		t.Fatal("failed feed did not roll back business changes", messages, err)
	}
	assertNotWoken(t, wake)
	assertUpdateChangesDrained(t, f.s)
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_refuse_update"); err != nil {
		t.Fatal(err)
	}
	message, err := f.s.SendMessage(ctx, f.founder.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	delivered := awaitUpdateWait(t, waiter)
	cancel()
	if delivered.err != nil || !updateContains(delivered.page, "message", message.ID) {
		t.Fatal("accepted retry omitted its single durable hint", delivered)
	}
	count = updateCount(t, f.s)
	if _, err := f.s.SendMessage(ctx, f.founder.ID, req); err != nil || updateCount(t, f.s) != count {
		t.Fatal("exact operation replay emitted a duplicate", err)
	}
	if page := updateReplay(t, f.s, f.worker.ID, delivered.page.NextCursor); len(page.Items) != 0 {
		t.Fatal("exact replay created a new recipient hint", page)
	}
	read, err := f.s.ReadMessage(ctx, f.worker.ID, message.ID, domain.MessageReadRequest{Command: op()})
	if err != nil {
		t.Fatal(err)
	}
	count = updateCount(t, f.s)
	again, err := f.s.ReadMessage(ctx, f.worker.ID, message.ID, domain.MessageReadRequest{Command: op()})
	if err != nil || read.ReadAt != again.ReadAt || updateCount(t, f.s) != count {
		t.Fatal("already-read acknowledgement produced churn or changed receipt time", again, err)
	}
}

func TestUpdatesCursorValidationAndBoundedReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	since := updateWatermark(t, f.s, f.worker.ID)
	_, err := f.s.Updates(ctx, f.other.ID, since, 1)
	assertCode(t, err, domain.Denied)
	for _, invalid := range []string{"bad", since + "x", strings.Repeat("x", 4097), f.s.makeUpdateCursor(f.worker.ID, 0, -1)} {
		_, err := f.s.Updates(ctx, f.worker.ID, invalid, 1)
		assertCode(t, err, domain.Invalid)
	}
	for _, limit := range []int{-1, 201} {
		_, err := f.s.Updates(ctx, f.worker.ID, since, limit)
		assertCode(t, err, domain.Invalid)
	}
	future := f.s.makeUpdateCursor(f.worker.ID, 0, 1<<60)
	_, err = f.s.Updates(ctx, f.worker.ID, future, 1)
	assertCode(t, err, domain.Conflict)
	stale := f.s.makeUpdateCursor(f.worker.ID, 17, 0)
	_, err = f.s.Updates(ctx, f.worker.ID, stale, 1)
	assertCode(t, err, domain.Conflict)
	want := make(map[string]bool)
	for range 7 {
		message := sendUpdateMessage(t, f, f.worker.ID)
		want[message.ID] = true
	}
	var previous int64
	for range 4 {
		page, err := f.s.Updates(ctx, f.worker.ID, since, 2)
		if err != nil || len(page.Items) > 2 || page.NextCursor == "" {
			t.Fatal("bounded replay failed", page, err)
		}
		for _, update := range page.Items {
			if update.Sequence <= previous || !want[update.SourceID] {
				t.Fatal("replay lost ordering, skipped or duplicated a change", update)
			}
			previous = update.Sequence
			delete(want, update.SourceID)
		}
		since = page.NextCursor
	}
	if len(want) != 0 {
		t.Fatal("bounded replay skipped changes", want)
	}
	if page := updateReplay(t, f.s, f.worker.ID, since); len(page.Items) != 0 {
		t.Fatal("final replay watermark did not acknowledge the page", page)
	}
	if _, err := f.s.db.Exec("UPDATE update_state SET floor_sequence=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Updates(ctx, f.worker.ID, f.s.makeUpdateCursor(f.worker.ID, 0, 0), 1)
	assertCode(t, err, domain.Conflict)
}

func TestUpdatesSubscribeCommitRacesAndCancellation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range 40 {
		t.Run(fmt.Sprintf("race-%d", i), func(t *testing.T) {
			since := updateWatermark(t, f.s, f.worker.ID)
			var message domain.Message
			var result <-chan updateWaitResult
			var cancel context.CancelFunc
			// Alternate commit-before-subscribe, concurrent and subscribed commits.
			if i%3 == 1 {
				message = sendUpdateMessage(t, f, f.worker.ID)
				result, cancel = startUpdateWait(t, f.s, ctx, f.worker.ID, since)
			} else {
				result, cancel = startUpdateWait(t, f.s, ctx, f.worker.ID, since)
				if i%3 == 0 {
					updateSubscription(t, f.s, f.worker.ID, 1)
				}
				message = sendUpdateMessage(t, f, f.worker.ID)
			}
			delivered := awaitUpdateWait(t, result)
			cancel()
			if delivered.err != nil || !updateContains(delivered.page, "message", message.ID) {
				t.Fatal("subscribe/watermark/commit race lost a change", delivered)
			}
		})
	}
	since := updateWatermark(t, f.s, f.worker.ID)
	canceled, cancel := startUpdateWait(t, f.s, ctx, f.worker.ID, since)
	updateSubscription(t, f.s, f.worker.ID, 1)
	cancel()
	if result := awaitUpdateWait(t, canceled); !errors.Is(result.err, context.Canceled) {
		t.Fatal("idle waiter ignored cancellation", result)
	}
	f.s.updates.mu.Lock()
	count := len(f.s.updates.waiters)
	f.s.updates.mu.Unlock()
	if count != 0 {
		t.Fatal("completed waits leaked subscription state", count)
	}
}

func TestUpdatesCloseUnblocksSharedSubscribers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	since := updateWatermark(t, f.s, f.worker.ID)
	results := make([]<-chan updateWaitResult, 16)
	for i := range results {
		results[i], _ = startUpdateWait(t, f.s, ctx, f.worker.ID, since)
	}
	updateSubscription(t, f.s, f.worker.ID, len(results))
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		value := awaitUpdateWait(t, result)
		if domain.AsFault(value.err).Code != domain.Unavailable {
			t.Fatal("closed store did not safely release an idle request", value.page, value.err)
		}
	}
	f.s.updates.mu.Lock()
	count := len(f.s.updates.waiters)
	f.s.updates.mu.Unlock()
	if count != 0 {
		t.Fatal("closed store retained subscribers", count)
	}
	if _, err := f.s.WaitUpdates(ctx, f.worker.ID, since, 1); domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("closed store admitted a new waiter", err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal("idempotent close failed", err)
	}
}

func TestUpdatesCurrentSchemaReopenPreservesState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "sql-three.sqlite")
	db, err := database.Open(ctx, path, []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema}, 5)
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{ID: domain.NewID("agent"), Name: "retained Founder", Founder: true, Active: true, Mode: "manual", Revision: 12, CreatedAt: 101, LastSeen: 102}
	data, _ := encode(agent)
	if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, domain.Hash(domain.NewToken())); err != nil {
		t.Fatal(err)
	}
	seedUpdateIdentity(t, db, agent)
	message := domain.Message{ID: domain.NewID("message"), SenderID: agent.ID, RecipientID: agent.ID, Body: "Retained private message", ReadAt: 110, CreatedAt: 103, State: "handled", Revision: 2, Origin: "human", Kind: "direct"}
	data, _ = encode(message)
	if _, err := db.Exec("INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,0)", message.ID, data, agent.ID, agent.ID, message.State); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM update_changes"); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, table := range []string{"agents", "messages", "settings", "operations", "histories", "events"} {
		before[table] = securityTableHash(t, db, table)
	}
	identityQuery := "SELECT id,data,ssh_user,uid FROM agent_identities ORDER BY rowid"
	identityBefore := securityQueryHash(t, db, identityQuery)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 8 {
		t.Fatal("ordered migrations through SQL8 failed", version, err)
	}
	for table, hash := range before {
		if got := securityTableHash(t, s.db, table); got != hash {
			t.Fatal("SQL4 migration rewrote retained state", table)
		}
		if got := securityQueryHash(t, s.db, identityQuery); got != identityBefore {
			t.Fatal("ordered migrations rewrote retained identity bindings")
		}
	}
	if updateCount(t, s) != 0 {
		t.Fatal("migration invented historical business updates")
	}
	updateWatermark(t, s, agent.ID)
	assertUpdateChangesDrained(t, s)
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

type eventWaitResult struct {
	page domain.Page[domain.Event]
	err  error
}

func startEventWait(t *testing.T, s *Store, actor string, scope eventScope, since string, limit int) (<-chan eventWaitResult, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan eventWaitResult, 1)
	go func() {
		page, err := s.WaitEvents(ctx, actor, scope.Project, scope.Task, since, limit)
		result <- eventWaitResult{page, err}
	}()
	return result, cancel
}

func awaitEventWait(t *testing.T, result <-chan eventWaitResult) eventWaitResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("public event waiter did not unblock")
		return eventWaitResult{}
	}
}

func eventSubscription(t *testing.T, s *Store, scope eventScope, refs int) <-chan struct{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.updates.mu.Lock()
		waiter := s.updates.eventWaiters[scope]
		if waiter != nil && waiter.refs == refs {
			wake := waiter.wake
			s.updates.mu.Unlock()
			return wake
		}
		s.updates.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("public event subscription did not reach expected reference count", scope, refs)
	return nil
}

func eventsCaughtUp(t *testing.T, s *Store, actor string, scope eventScope, limit int) string {
	t.Helper()
	since := ""
	for {
		page, err := s.Events(context.Background(), actor, scope.Project, scope.Task, since, limit)
		if err != nil {
			t.Fatal(err)
		}
		since = page.NextCursor
		if len(page.Items) == 0 {
			return since
		}
	}
}

func TestWaitEventsPreservesSharedReplayAndCursorSemantics(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	for _, scope := range []eventScope{{}, {Project: task.ProjectID}, {Task: task.ID}, {task.ProjectID, task.ID}} {
		t.Run(fmt.Sprintf("project=%s/task=%s", scope.Project, scope.Task), func(t *testing.T) {
			since := ""
			for {
				expected, err := f.s.Events(ctx, f.other.ID, scope.Project, scope.Task, since, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(expected.Items) == 0 {
					result, cancel := startEventWait(t, f.s, f.other.ID, scope, since, 1)
					eventSubscription(t, f.s, scope, 1)
					cancel()
					caughtUp := awaitEventWait(t, result)
					if !errors.Is(caughtUp.err, context.Canceled) {
						t.Fatal("caught-up public replay did not wait for cancellation", caughtUp.err)
					}
					break
				}
				actual, err := f.s.WaitEvents(ctx, f.other.ID, scope.Project, scope.Task, since, 1)
				if err != nil || !reflect.DeepEqual(actual, expected) {
					t.Fatal("unassigned team viewer lost existing shared replay/output semantics", expected, actual, err)
				}
				since = actual.NextCursor
			}
			for _, invalid := range []struct {
				actor, project, task string
				limit                int
			}{
				{f.worker.ID, scope.Project, scope.Task, 1},
				{f.other.ID, scope.Project + "-different", scope.Task, 1},
				{f.other.ID, scope.Project, scope.Task + "-different", 1},
				{f.other.ID, scope.Project, scope.Task, 2},
			} {
				_, expected := f.s.Events(ctx, invalid.actor, invalid.project, invalid.task, since, invalid.limit)
				_, actual := f.s.WaitEvents(ctx, invalid.actor, invalid.project, invalid.task, since, invalid.limit)
				if expected == nil || actual == nil || !reflect.DeepEqual(domain.AsFault(actual), domain.AsFault(expected)) {
					t.Fatal("public watch changed actor/filter/page-size cursor fences", actual, expected)
				}
			}
		})
	}
	for _, scope := range []eventScope{{Project: "project_missing"}, {Project: task.ProjectID, Task: "task_missing"}} {
		for _, last := range []string{"", "9000000000000000000"} {
			since := ""
			if last != "" {
				since = makeCursor(f.other.ID, scope.Project+":"+scope.Task+":1", last)
			}
			expected, err := f.s.Events(ctx, f.other.ID, scope.Project, scope.Task, since, 1)
			if err != nil || len(expected.Items) != 0 {
				t.Fatal("empty/future public cursor fixture is invalid", expected, err)
			}
			result, cancel := startEventWait(t, f.s, f.other.ID, scope, since, 1)
			eventSubscription(t, f.s, scope, 1)
			cancel()
			actual := awaitEventWait(t, result)
			if !errors.Is(actual.err, context.Canceled) {
				t.Fatal("empty/future public cursor was rejected or did not wait", actual.err)
			}
		}
	}
	for _, last := range []string{"-1", "not-a-sequence"} {
		since := makeCursor(f.other.ID, task.ProjectID+":"+task.ID+":1", last)
		_, err := f.s.WaitEvents(ctx, f.other.ID, task.ProjectID, task.ID, since, 1)
		assertCode(t, err, domain.Invalid)
	}
}

func TestWaitEventsCaughtUpHasNoQueryLoopOrPrivateWake(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := f.ready(t)
	scope := eventScope{task.ProjectID, task.ID}
	since := eventsCaughtUp(t, f.s, f.other.ID, scope, 200)
	waiter, err := f.s.subscribeEvents(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer f.s.unsubscribeEvents(scope, waiter)
	var queries atomic.Int64
	queried := make(chan struct{}, 1)
	result := make(chan eventWaitResult, 1)
	go func() {
		page, err := waitFeed(ctx, f.s, waiter, since, func(cursor string) (domain.Page[domain.Event], error) {
			queries.Add(1)
			page, err := f.s.Events(ctx, f.other.ID, scope.Project, scope.Task, cursor, 200)
			select {
			case queried <- struct{}{}:
			default:
			}
			return page, err
		})
		result <- eventWaitResult{page, err}
	}()
	select {
	case <-queried:
	case <-time.After(3 * time.Second):
		t.Fatal("caught-up event replay did not complete")
	}
	wake := eventSubscription(t, f.s, scope, 1)
	f.s.db.SetMaxOpenConns(1)
	leaseCtx, release := context.WithTimeout(ctx, time.Second)
	defer release()
	conn, err := f.s.db.Conn(leaseCtx)
	if err != nil {
		t.Fatal("idle public watch retained its SQL connection", err)
	}
	waits := f.s.db.Stats().WaitCount
	select {
	case value := <-result:
		conn.Close()
		t.Fatal("caught-up public watch returned without a public event", value.page, value.err)
	case <-time.After(150 * time.Millisecond):
	}
	if f.s.db.Stats().WaitCount != waits || queries.Load() != 1 {
		conn.Close()
		t.Fatal("caught-up public watch performed timer-driven replay queries", queries.Load())
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// These private controls even carry the watched project scope, but no
	// shared event. A message to the viewer also exercises the separate actor hub.
	sendUpdateMessage(t, f, f.other.ID)
	if _, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.other.ID, domain.DiagnosticRefreshRequest{Command: op(), Reason: "Private watch fixture control"}); err != nil {
		t.Fatal(err)
	}
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.other.ID, ProjectID: task.ProjectID,
		Mode: "tmux", Purpose: "terminal", Reason: "Private project-scoped watch fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, ProjectID: task.ProjectID, TargetID: f.other.ID,
		Mode: "tmux", Purpose: "terminal", Reason: "Private project-scoped watch session", ExpiresAt: f.now.Unix() + 120}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Unrelated public scope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateTask(ctx, f.founder.ID, domain.TaskRequest{Command: op(), ProjectID: task.ProjectID, Title: "Different task in the same project"}); err != nil {
		t.Fatal(err)
	}
	assertNotWoken(t, wake)
	if queries.Load() != 1 {
		t.Fatal("private/unrelated commit caused a public replay query", queries.Load())
	}
	title := "Relevant public fixture change"
	f.change(t, f.founder.ID, task.ID, "edit", func(req *domain.TaskChange) { req.Title = &title })
	delivered := awaitEventWait(t, result)
	expected, err := f.s.Events(ctx, f.other.ID, scope.Project, scope.Task, since, 200)
	if delivered.err != nil || err != nil || !reflect.DeepEqual(delivered.page, expected) || len(expected.Items) != 1 || queries.Load() != 2 {
		t.Fatal("relevant public commit did not wake with the exact existing event page", delivered.page, delivered.err, expected, err, queries.Load())
	}
	assertUpdateChangesDrained(t, f.s)
}

func TestWaitEventsRoutesProjectTaskAndGlobalSubscriptions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	scopes := []eventScope{{}, {Project: task.ProjectID}, {Task: task.ID}, {task.ProjectID, task.ID},
		{Project: "project_unrelated"}, {Task: "task_unrelated"}, {"project_unrelated", task.ID}}
	results := make([]<-chan eventWaitResult, len(scopes))
	cancels := make([]context.CancelFunc, len(scopes))
	wakes := make([]<-chan struct{}, len(scopes))
	for i, scope := range scopes {
		since := eventsCaughtUp(t, f.s, f.other.ID, scope, 200)
		results[i], cancels[i] = startEventWait(t, f.s, f.other.ID, scope, since, 200)
		wakes[i] = eventSubscription(t, f.s, scope, 1)
	}
	sendUpdateMessage(t, f, f.other.ID)
	if _, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.other.ID, domain.DiagnosticRefreshRequest{Command: op(), Reason: "Private control, not a public event"}); err != nil {
		t.Fatal(err)
	}
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.other.ID,
		ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "Private project scope, not a public project event"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, ProjectID: task.ProjectID, TargetID: f.other.ID,
		Mode: "tmux", Purpose: "terminal", Reason: "Private session, not a shared event", ExpiresAt: f.now.Unix() + 120}); err != nil {
		t.Fatal(err)
	}
	for _, wake := range wakes {
		assertNotWoken(t, wake)
	}
	title := "Shared task changed by its authority, not the viewer"
	f.change(t, f.founder.ID, task.ID, "edit", func(req *domain.TaskChange) { req.Title = &title })
	for i, scope := range scopes {
		if i < 4 {
			delivered := awaitEventWait(t, results[i])
			if delivered.err != nil || len(delivered.page.Items) != 1 || delivered.page.Items[0].TaskID != task.ID {
				t.Fatal("matching public scope did not wake its unassigned viewer", scope, delivered.page, delivered.err)
			}
		} else {
			assertNotWoken(t, wakes[i])
			cancels[i]()
			if canceled := awaitEventWait(t, results[i]); !errors.Is(canceled.err, context.Canceled) {
				t.Fatal("unrelated public scope did not remain cancellably idle", scope, canceled.err)
			}
		}
	}
}

func TestWaitEventsRollbackReplayRacesAndClose(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	scope := eventScope{task.ProjectID, task.ID}
	since := eventsCaughtUp(t, f.s, f.other.ID, scope, 200)
	result, _ := startEventWait(t, f.s, f.other.ID, scope, since, 200)
	wake := eventSubscription(t, f.s, scope, 1)
	_, err := mutate(ctx, f.s, f.founder.ID, "fixture.public-rollback", op(), nil, func(tx *sql.Tx, _ domain.Agent) (domain.Task, error) {
		if err := f.s.event(ctx, tx, "task", task.ID, task.ID, task.ProjectID); err != nil {
			return task, err
		}
		assertNotWoken(t, wake)
		return task, domain.Fail(domain.Conflict, "Roll back the staged shared event")
	})
	assertCode(t, err, domain.Conflict)
	assertNotWoken(t, wake)
	if page, err := f.s.Events(ctx, f.other.ID, scope.Project, scope.Task, since, 200); err != nil || len(page.Items) != 0 {
		t.Fatal("rolled-back shared event leaked into replay", page, err)
	}
	title := "Accepted public fixture change"
	req := domain.TaskChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision}, Kind: "edit", Title: &title, Reason: "Public watch exact replay fixture"}
	if _, err := f.s.ChangeTask(ctx, f.founder.ID, task.ID, req); err != nil {
		t.Fatal(err)
	}
	delivered := awaitEventWait(t, result)
	if delivered.err != nil || len(delivered.page.Items) != 1 {
		t.Fatal(delivered.page, delivered.err)
	}
	result, cancel := startEventWait(t, f.s, f.other.ID, scope, delivered.page.NextCursor, 200)
	wake = eventSubscription(t, f.s, scope, 1)
	if _, err := f.s.ChangeTask(ctx, f.founder.ID, task.ID, req); err != nil {
		t.Fatal(err)
	}
	assertNotWoken(t, wake)
	cancel()
	if value := awaitEventWait(t, result); !errors.Is(value.err, context.Canceled) {
		t.Fatal("exact operation replay returned a phantom public change", value.page, value.err)
	}
	for i := range 18 {
		since := eventsCaughtUp(t, f.s, f.other.ID, scope, 200)
		title := fmt.Sprintf("Public subscribe/commit race %d", i)
		if i%3 == 0 {
			f.change(t, f.founder.ID, task.ID, "edit", func(req *domain.TaskChange) { req.Title = &title })
		}
		result, cancel := startEventWait(t, f.s, f.other.ID, scope, since, 200)
		if i%3 != 0 {
			if i%3 == 1 {
				eventSubscription(t, f.s, scope, 1)
			}
			f.change(t, f.founder.ID, task.ID, "edit", func(req *domain.TaskChange) { req.Title = &title })
		}
		if value := awaitEventWait(t, result); value.err != nil || len(value.page.Items) != 1 {
			t.Fatal("public subscribe/replay/commit race lost its event", value.page, value.err)
		}
		cancel()
	}
	results := make([]<-chan eventWaitResult, 16)
	for i := range results {
		actor := f.other.ID
		if i%2 == 1 {
			actor = f.worker.ID
		}
		since := eventsCaughtUp(t, f.s, actor, scope, 200)
		results[i], _ = startEventWait(t, f.s, actor, scope, since, 200)
	}
	eventSubscription(t, f.s, scope, len(results))
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if value := awaitEventWait(t, result); domain.AsFault(value.err).Code != domain.Unavailable {
			t.Fatal("closing the store did not release public event subscribers", value.page, value.err)
		}
	}
	f.s.updates.mu.Lock()
	remaining := len(f.s.updates.eventWaiters)
	f.s.updates.mu.Unlock()
	if remaining != 0 {
		t.Fatal("public event subscriptions leaked after close", remaining)
	}
	_, err = f.s.WaitEvents(ctx, f.other.ID, scope.Project, scope.Task, since, 200)
	assertCode(t, err, domain.Unavailable)
}
