package foundation

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func operationInspection(at int64, mode string) *domain.AgentInspection {
	return &domain.AgentInspection{ObservedAt: at, WorkSlot: "free", Health: &domain.WorkerHealth{Version: domain.WorkerHealthVersion, ObservedAt: at, Mode: mode, Authentication: "unknown", Phase: "unknown"}, Provider: &domain.ProviderObservation{Provider: "copilot", Authentication: "unknown", ObservedAt: at, ProblemCode: domain.Unavailable}}
}

func requestDiagnosticOperation(t *testing.T, f *fixture) (domain.AgentOperation, domain.DiagnosticRefreshRequest) {
	t.Helper()
	req := domain.DiagnosticRefreshRequest{Command: op(), Reason: "Collect current bounded observations without inference"}
	operation, err := f.s.RequestDiagnosticRefresh(context.Background(), f.founder.ID, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return operation, req
}

func checkingAgentOperation(t *testing.T, f *fixture, operation domain.AgentOperation) (domain.AgentOperation, domain.AgentOperationReport) {
	t.Helper()
	req := domain.AgentOperationReport{Command: op(), State: "checking"}
	req.ExpectedRevision = operation.Revision
	checked, err := f.s.ReportAgentOperation(context.Background(), operation.TargetID, operation.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return checked, req
}

func diagnosticOperationReport(f *fixture, operation domain.AgentOperation) domain.AgentOperationReport {
	return domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: operation.Revision}, State: "refreshed", Result: &domain.AgentOperationResult{Code: "observations-collected", ModeAtCheck: "ai", CapturedAt: f.now.Unix(), Observation: operationInspection(f.now.Unix(), "ai")}}
}

func seedOperationSession(t *testing.T, f *fixture, target, state string, expiresAt int64) domain.Session {
	t.Helper()
	session := domain.Session{ID: domain.NewID("session"), RequesterID: f.other.ID, TargetID: target, Mode: "tmux", Purpose: "terminal", Reason: "Private fixture session", State: state, ExpiresAt: expiresAt, Revision: 1, CreatedAt: f.now.Unix() - 60}
	saveOperationSession(t, f.s, session)
	return session
}

func saveOperationSession(t *testing.T, s *Store, session domain.Session) {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.saveSession(context.Background(), tx, session, session.RequesterID, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func requestRecoveryOperation(t *testing.T, f *fixture, session domain.Session) domain.AgentOperation {
	t.Helper()
	operation, err := f.s.RequestSessionRecovery(context.Background(), f.founder.ID, f.worker.ID, domain.SessionRecoveryRequest{Command: op(), SessionID: session.ID, ExpectedFence: domain.Hash("exact durable local fence"), Reason: "Reconcile only this already terminal session"})
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func recoveryOperationReport(f *fixture, operation domain.AgentOperation) domain.AgentOperationReport {
	inspection := operationInspection(f.now.Unix(), "ai")
	inspection.SessionFence = &domain.SessionFenceObservation{SessionID: operation.SessionID, Fingerprint: operation.ExpectedFence, CleanupProven: true, SessionState: "closed", RecoveryState: "recoverable"}
	return domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: operation.Revision}, State: "recovered", Result: &domain.AgentOperationResult{Code: "session-recovered", ModeAtCheck: "ai", CapturedAt: f.now.Unix(), AutomationMayResume: true, Observation: inspection}}
}

func TestAgentOperationsFounderOnlyPrivateProtectedAndNonexecuting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	request := domain.DiagnosticRefreshRequest{Command: op(), Reason: "Explicit fresh inspection"}
	for _, actor := range []string{f.worker.ID, f.other.ID} {
		_, err := f.s.RequestDiagnosticRefresh(ctx, actor, f.worker.ID, request)
		assertCode(t, err, domain.Denied)
	}
	paused, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Remain paused during diagnostic inspection"})
	if err != nil {
		t.Fatal(err)
	}
	var events int
	if err := f.s.db.QueryRow("SELECT count(*) FROM events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	operation, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, request)
	if err != nil || operation.State != "queued" || operation.Result != nil || operation.RequesterGeneration != 0 || operation.TargetGeneration != 0 || operation.ExpiresAt != operation.CreatedAt+900 {
		t.Fatal("fresh request fabricated completion or changed the generation-zero fixture contract", operation, err)
	}
	for _, actor := range []string{f.founder.ID, f.worker.ID} {
		read, err := f.s.GetAgentOperation(ctx, actor, operation.ID)
		if err != nil || !reflect.DeepEqual(read, operation) {
			t.Fatal("operation party could not read its protected control", read, err)
		}
		_, err = f.s.Message(ctx, actor, operation.ID)
		assertCode(t, err, domain.Denied)
	}
	_, err = f.s.GetAgentOperation(ctx, f.other.ID, operation.ID)
	assertCode(t, err, domain.Denied)
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		page, err := f.s.ListAgentOperations(ctx, actor, "", 200)
		if err != nil || len(page.Items) != 0 {
			t.Fatal("issuer or unrelated actor listed another target's controls", page, err)
		}
	}
	page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != operation.ID {
		t.Fatal("target could not discover pending operation", page, err)
	}
	for _, urgent := range []bool{false, true} {
		inbox, err := f.s.Inbox(ctx, f.worker.ID, "", 200, urgent)
		if err != nil || len(inbox.Items) != 0 {
			t.Fatal("control entered ordinary model/message scheduling", inbox, err)
		}
	}
	for _, state := range []string{"handled", "deferred", "stale", "pending"} {
		_, err := f.s.ReceiveMessage(ctx, f.worker.ID, operation.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: state})
		assertCode(t, err, domain.Denied)
	}
	_, err = f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "Forged runtime control", Origin: "service", Kind: "agent-operation", Urgent: true})
	assertCode(t, err, domain.Invalid)
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		_, err := f.s.ReportAgentOperation(ctx, actor, operation.ID, domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "checking"})
		assertCode(t, err, domain.Denied)
	}
	current, err := f.s.Agent(ctx, f.founder.ID, f.worker.ID)
	if err != nil || !reflect.DeepEqual(current, paused) {
		t.Fatal("queued diagnostics changed mode or agent state", current, err)
	}
	for _, table := range []string{"runs", "sessions", "assistance_grants", "tasks"} {
		var count int
		if err := f.s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("control manufactured work or authority", table, count, err)
		}
	}
	var afterEvents int
	if err := f.s.db.QueryRow("SELECT count(*) FROM events").Scan(&afterEvents); err != nil || afterEvents != events {
		t.Fatal("private runtime request created a shared event", events, afterEvents, err)
	}
}

func TestAgentOperationReportsRetainSafeObservationNotHealthyFallback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	private, err := f.s.SendMessage(ctx, f.other.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "Unrelated private request", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	operation, request := requestDiagnosticOperation(t, f)
	premature := diagnosticOperationReport(f, operation)
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, premature)
	assertCode(t, err, domain.Conflict)
	operation, checking := checkingAgentOperation(t, f, operation)
	if operation.StartedAt != f.now.Unix() || operation.Revision != 2 {
		t.Fatal("checking did not durably establish an execution window", operation)
	}
	report := diagnosticOperationReport(f, operation)
	h := report.Result.Observation.Health
	h.Sequence, h.Phase, h.PhaseStartedAt = 1, "invoking", f.now.Unix()
	h.Provider, h.PreferredProvider, h.Model, h.Authentication = "codex", "copilot", "current-model", "authenticated"
	h.ActionKind, h.ActionID, h.LastActionKind, h.LastActionID, h.JournalID = "message", private.ID, "message", private.ID, "journal_private"
	final, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
	if err != nil || final.State != "refreshed" || final.Revision != 3 || final.Result == nil || final.Result.AutomationMayResume {
		t.Fatal("captured probe failure was dropped or promoted to permission", final, err)
	}
	observation := final.Result.Observation
	if observation.Provider.ProblemCode != domain.Unavailable || observation.Provider.Authentication != "unknown" || observation.Health.Provider != "codex" || observation.Health.Model != "current-model" || observation.Health.Phase != "invoking" || observation.Health.ActionID != "" || observation.Health.LastActionID != "" || observation.Health.JournalID != "" {
		t.Fatal("fresh read-only observation overwrote current work or leaked private references", observation)
	}
	for _, retry := range []func() (domain.AgentOperation, error){
		func() (domain.AgentOperation, error) {
			return f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, request)
		},
		func() (domain.AgentOperation, error) {
			return f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, checking)
		},
		func() (domain.AgentOperation, error) {
			return f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
		},
	} {
		replayed, err := retry()
		if err != nil || !reflect.DeepEqual(replayed, final) {
			t.Fatal("lost acknowledgment replay changed the captured observation", replayed, err)
		}
	}
	changed := report
	changed.Result = &domain.AgentOperationResult{Code: "inspection-unavailable", ModeAtCheck: "ai"}
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, changed)
	assertCode(t, err, domain.Conflict)
	changed.Command = domain.Command{OperationID: op().OperationID, ExpectedRevision: final.Revision}
	changed.State = "rejected"
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, changed)
	assertCode(t, err, domain.Conflict)
	page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 200)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("final operation was still dispatched", page, err)
	}
	var receiptSize, exactAudit int
	if err := f.s.db.QueryRow("SELECT max(length(result)) FROM operations").Scan(&receiptSize); err != nil || receiptSize > 256 {
		t.Fatal("operation receipts duplicated private mutable payloads", receiptSize, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM audit WHERE action LIKE ? AND outcome='accepted'", "agent-operation.%:"+operation.ID).Scan(&exactAudit); err != nil || exactAudit != 5 {
		t.Fatal("actual mutations lost their exact immutable operation ID audit references", exactAudit, err)
	}
	worker, err := f.s.Agent(ctx, f.founder.ID, f.worker.ID)
	if err != nil || !reflect.DeepEqual(worker, f.worker) {
		t.Fatal("inspection mutated heartbeat/global work state", worker, err)
	}
}

func TestAgentOperationPaginationExpiryAndRestartDoNotRenew(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	operation, request := requestDiagnosticOperation(t, f)
	_, _ = requestDiagnosticOperation(t, f)
	first, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("target operation pagination failed", first, err)
	}
	second, err := f.s.ListAgentOperations(ctx, f.worker.ID, first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.NextCursor != "" {
		t.Fatal("target operation keyset page changed", second, err)
	}
	_, err = f.s.ListAgentOperations(ctx, f.other.ID, first.NextCursor, 1)
	assertCode(t, err, domain.Conflict)
	f.now = f.now.Add(time.Duration(domain.AgentOperationTTL) * time.Second)
	expired, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
	if err != nil || expired.State != "expired" || expired.FinishedAt != operation.ExpiresAt || expired.StartedAt != 0 || expired.Result.Code != "operation-expired" {
		t.Fatal("fixed queued deadline was not projected", expired, err)
	}
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "checking"})
	assertCode(t, err, domain.Conflict)
	var sequence int
	var name, path string
	if err := f.s.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reopened.now = func() time.Time { return f.now }
	f.s = reopened
	replayed, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, request)
	if err != nil || !reflect.DeepEqual(replayed, expired) {
		t.Fatal("restart/retry renewed or replaced an expired operation", replayed, err)
	}
	page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 200)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("expired controls remained dispatchable", page, err)
	}
	raw, err := getAgentOperation(ctx, f.s.db, operation.ID)
	if err != nil || raw.State != "queued" || raw.Revision != 1 || raw.ExpiresAt != operation.ExpiresAt {
		t.Fatal("read-only expiry rewrote immutable request evidence", raw, err)
	}
}

func TestAgentOperationLateEvidenceRequiresCheckingAndRetainsOriginalObservation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	session := seedOperationSession(t, f, f.worker.ID, "closed", f.now.Unix()+600)
	operation := requestRecoveryOperation(t, f, session)
	operation, _ = checkingAgentOperation(t, f, operation)
	report := recoveryOperationReport(f, operation)
	captured := report.Result.Observation.ObservedAt
	f.now = f.now.Add(901 * time.Second)
	projected, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
	if err != nil || projected.State != "expired" {
		t.Fatal("checking deadline failed to stop new work", projected, err)
	}
	late, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
	if err != nil || late.State != "recovered" || late.Result.AutomationMayResume || late.Result.Observation.ObservedAt != captured || late.StartedAt >= late.ExpiresAt || late.FinishedAt <= late.ExpiresAt {
		t.Fatal("late committed evidence was dropped, made fresh, or authorized automation", late, err)
	}
	replayed, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
	if err != nil || !reflect.DeepEqual(replayed, late) {
		t.Fatal("late receipt replay changed retained evidence", replayed, err)
	}
	queued, _ := requestDiagnosticOperation(t, f)
	checking, _ := checkingAgentOperation(t, f, queued)
	f.now = f.now.Add(901 * time.Second)
	future := diagnosticOperationReport(f, checking)
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, checking.ID, future)
	assertCode(t, err, domain.Conflict)
	noEvidence := domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: checking.Revision}, State: "rejected", Result: &domain.AgentOperationResult{Code: "inspection-unavailable", ModeAtCheck: "ai"}}
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, checking.ID, noEvidence)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, queued.ID, domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "checking"})
	assertCode(t, err, domain.Conflict)
}

func TestSessionRecoveryRequiresExactTerminalTargetAndProof(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, state := range []string{"requested", "consented", "active"} {
		session := seedOperationSession(t, f, f.worker.ID, state, f.now.Unix()+600)
		_, err := f.s.RequestSessionRecovery(ctx, f.founder.ID, f.worker.ID, domain.SessionRecoveryRequest{Command: op(), SessionID: session.ID, ExpectedFence: domain.Hash("fence"), Reason: "Do not touch live sessions"})
		assertCode(t, err, domain.Prerequisite)
	}
	foreign := seedOperationSession(t, f, f.other.ID, "closed", f.now.Unix()+600)
	_, err := f.s.RequestSessionRecovery(ctx, f.founder.ID, f.worker.ID, domain.SessionRecoveryRequest{Command: op(), SessionID: foreign.ID, ExpectedFence: domain.Hash("fence"), Reason: "Wrong target"})
	assertCode(t, err, domain.Denied)
	for _, state := range []string{"denied", "revoked", "closed", "expired", "failed"} {
		session := seedOperationSession(t, f, f.worker.ID, state, f.now.Unix()+600)
		requestRecoveryOperation(t, f, session)
	}
	expiredLive := seedOperationSession(t, f, f.worker.ID, "active", f.now.Unix()-1)
	requestRecoveryOperation(t, f, expiredLive)
	closed := seedOperationSession(t, f, f.worker.ID, "closed", f.now.Unix()+600)
	_, err = f.s.Session(ctx, f.founder.ID, closed.ID)
	assertCode(t, err, domain.Denied)
	operation := requestRecoveryOperation(t, f, closed)
	operation, _ = checkingAgentOperation(t, f, operation)
	unproved := recoveryOperationReport(f, operation)
	unproved.State, unproved.Result.Code, unproved.Result.AutomationMayResume = "operator-required", "cleanup-unproven", false
	unproved.Result.Observation.SessionFence.CleanupProven, unproved.Result.Observation.SessionFence.RecoveryState = false, "cleanup-unproven"
	final, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, unproved)
	if err != nil || final.State != "operator-required" || final.Result.Code != "cleanup-unproven" || final.Result.AutomationMayResume {
		t.Fatal("legacy cleanup uncertainty was automatically acknowledged", final, err)
	}
	current, err := f.s.Session(ctx, f.worker.ID, closed.ID)
	if err != nil || !reflect.DeepEqual(current, closed) {
		t.Fatal("operation reopened or rewrote the private session", current, err)
	}
	operation = requestRecoveryOperation(t, f, closed)
	operation, _ = checkingAgentOperation(t, f, operation)
	for _, change := range []func(*domain.AgentOperationReport){
		func(r *domain.AgentOperationReport) {
			r.Result.Observation.SessionFence.Fingerprint = domain.Hash("other")
		},
		func(r *domain.AgentOperationReport) { r.Result.Observation.SessionFence.SessionID = foreign.ID },
		func(r *domain.AgentOperationReport) { r.Result.Observation.SessionFence.CleanupProven = false },
		func(r *domain.AgentOperationReport) { r.Result.Observation.WorkSlot = "occupied" },
	} {
		report := recoveryOperationReport(f, operation)
		change(&report)
		_, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
		assertCode(t, err, domain.Invalid)
	}
	closed.State = "active"
	saveOperationSession(t, f.s, closed)
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, recoveryOperationReport(f, operation))
	assertCode(t, err, domain.Prerequisite)
	projected, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
	if err != nil || projected.State != "rejected" || projected.Result.Code != "session-live" {
		t.Fatal("current live session remained actionable", projected, err)
	}
	closed.State = "closed"
	saveOperationSession(t, f.s, closed)
	_, err = f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Do not resume automation after recovery"})
	if err != nil {
		t.Fatal(err)
	}
	final, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, recoveryOperationReport(f, operation))
	if err != nil || final.State != "recovered" || final.Result.ModeAtCheck != "ai" || final.Result.AutomationMayResume {
		t.Fatal("current Founder pause was bypassed or recovery evidence lost", final, err)
	}
}

func seedOperationIdentity(t *testing.T, f *fixture, agent domain.Agent, generation, uid int64) domain.AgentIdentity {
	t.Helper()
	identity := domain.AgentIdentity{AgentID: agent.ID, InstanceID: "instance_fixture", Generation: generation, State: "active", OwnerGeneration: 1, OwnerKey: "fixture-owner", SSHUser: "fixture_" + strings.TrimPrefix(agent.ID, "agent_"), UID: uid}
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := saveIdentity(context.Background(), tx, identity); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestAgentOperationCurrentGenerationFencesIncludingZero(t *testing.T) {
	for _, subject := range []string{"target", "issuer"} {
		t.Run(subject, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			operation, request := requestDiagnosticOperation(t, f)
			operation, checking := checkingAgentOperation(t, f, operation)
			report := diagnosticOperationReport(f, operation)
			agent, uid := f.worker, int64(61001)
			if subject == "issuer" {
				agent, uid = f.founder, 61002
			}
			seedOperationIdentity(t, f, agent, 1, uid)
			projected, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
			if err != nil || projected.State != "rejected" || projected.Result.Code != "authority-changed" || projected.Result.Observation != nil {
				t.Fatal("zero-generation control dispatched to a fresh key identity", projected, err)
			}
			page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 200)
			if err != nil || len(page.Items) != 0 {
				t.Fatal("stale-generation control remained queued", page, err)
			}
			for _, retry := range []domain.AgentOperationReport{checking, report} {
				_, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, retry)
				assertCode(t, err, domain.Denied)
			}
			replayed, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, request)
			if err != nil || replayed.State != "rejected" || replayed.Result.Code != "authority-changed" {
				t.Fatal("request replay renewed stale key authority", replayed, err)
			}
			fresh, _ := requestDiagnosticOperation(t, f)
			if (subject == "issuer" && fresh.RequesterGeneration != 1) || (subject == "target" && fresh.TargetGeneration != 1) {
				t.Fatal("fresh control did not bind current generation", fresh)
			}
			seedOperationIdentity(t, f, agent, 2, uid)
			stale := WithIdentityFence(ctx, domain.CertificateIdentity{AgentID: agent.ID, InstanceID: "instance_fixture", Generation: 1, OwnerGeneration: 1, Purpose: "owner"})
			_, err = f.s.GetAgentOperation(stale, agent.ID, fresh.ID)
			assertCode(t, err, domain.Denied)
			_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, fresh.ID, domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "checking"})
			assertCode(t, err, domain.Denied)
		})
	}
}

func TestAgentOperationConcurrentReportsHaveOneDurableOutcome(t *testing.T) {
	f := newFixture(t)
	operation, _ := requestDiagnosticOperation(t, f)
	operation, _ = checkingAgentOperation(t, f, operation)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			_, err := f.s.ReportAgentOperation(context.Background(), f.worker.ID, operation.ID, diagnosticOperationReport(f, operation))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			assertCode(t, err, domain.Conflict)
		}
	}
	if accepted != 1 {
		t.Fatal("racing reports created multiple outcomes", accepted)
	}
	final, err := f.s.GetAgentOperation(context.Background(), f.worker.ID, operation.ID)
	if err != nil || final.Revision != 3 || final.State != "refreshed" {
		t.Fatal("racing reports lost the exact committed outcome", final, err)
	}
	var value []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", operation.ID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	var stored domain.Message
	if err := json.Unmarshal(value, &stored); err != nil || stored.AgentOperation == nil || stored.State != "handled" {
		t.Fatal("outcome was not durably stored in the protected private control", stored, err)
	}
}
