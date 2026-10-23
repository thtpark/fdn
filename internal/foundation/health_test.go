package foundation

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func observedHealth(at int64) domain.WorkerHealth {
	return domain.WorkerHealth{Version: domain.WorkerHealthVersion, Sequence: 1, ObservedAt: at,
		Mode: "ai", PreferredProvider: "copilot", Provider: "codex", ProviderVersion: "1.2.3", Model: "fixture-model",
		Authentication: "authenticated", AuthenticationObservedAt: at, Phase: "idle", PhaseStartedAt: at,
		Problems: []domain.HealthProblem{}}
}

func publishHealth(t *testing.T, s *Store, actor string, h domain.WorkerHealth) domain.Agent {
	t.Helper()
	a, err := s.HeartbeatWithHealth(context.Background(), actor, &h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func diagnoseHealth(t *testing.T, s *Store, viewer, target string) domain.AgentDiagnostics {
	t.Helper()
	d, err := s.AgentDiagnostics(context.Background(), viewer, target)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sweepHealth(t *testing.T, s *Store) {
	t.Helper()
	if err := s.SweepHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func healthMessages(t *testing.T, s *Store, target, recipient string) []domain.Message {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "SELECT data FROM messages WHERE sender_id=? AND recipient_id=? AND json_extract(data,'$.kind')='health' ORDER BY id", target, recipient)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := scanJSON[domain.Message](rows)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func directHealthMessage(t *testing.T, f *fixture, sender string, response bool) domain.Message {
	t.Helper()
	m, err := f.s.SendMessage(context.Background(), sender, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "Private fixture question", Origin: "human", Kind: "direct", ResponseRequest: response})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHealthLiveHeartbeatOverDeadlineAndStableDelivery(t *testing.T) {
	f := newFixture(t)
	h := observedHealth(f.now.Unix() + 6*3600)
	h.Phase, h.ActionKind, h.ActionID = "invoking", "onboarding", "onboarding"
	h.PhaseStartedAt, h.LastProgressAt, h.DeadlineAt = h.ObservedAt-400, h.ObservedAt-400, h.ObservedAt-35
	a := publishHealth(t, f.s, f.worker.ID, h)
	if a.LastSeen != f.now.Unix() || a.Revision != f.worker.Revision || a.Mode != f.worker.Mode {
		t.Fatal("health changed authority or lost heartbeat compatibility", a)
	}
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "blocked" || d.Reason != "phase-deadline-exceeded" || d.Stale || d.Health == nil {
		t.Fatal("live heartbeat hid an overdeadline invocation", d)
	}
	if len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 0 || d.Incident != nil {
		t.Fatal("GET diagnostics performed a sweep or sent messages")
	}
	sweepHealth(t, f.s)
	first := healthMessages(t, f.s, f.worker.ID, f.founder.ID)
	if len(first) != 1 || first[0].ResponseRequest || first[0].ExpiresAt != 0 || first[0].Origin != "service" || first[0].SenderID != f.worker.ID || !strings.Contains(first[0].Body, "Next authorized action:") || !strings.Contains(first[0].Body, "T12:") {
		t.Fatal("incorrect protected server observation", first)
	}
	incident := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident
	if incident == nil || incident.Since != f.now.Unix()-35 {
		t.Fatal("remote wall-clock skew changed the incident UTC age", incident)
	}
	for range 5 {
		f.now = f.now.Add(time.Second)
		h.ObservedAt++
		publishHealth(t, f.s, f.worker.ID, h)
		sweepHealth(t, f.s)
	}
	if messages := healthMessages(t, f.s, f.worker.ID, f.founder.ID); len(messages) != 1 || messages[0].ID != first[0].ID {
		t.Fatal("unchanged polling duplicated the durable alert", messages)
	}
	f.now = f.now.Add(901 * time.Second)
	h.ObservedAt += 901
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	m, err := f.s.Message(context.Background(), f.founder.ID, first[0].ID)
	if err != nil || m.State != "pending" || m.ExpiresAt != 0 || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
		t.Fatal("health alert expired like a sign-in notice or was resent", m, err)
	}
}

func TestHealthWithinDeadlineExplainsQueuedReply(t *testing.T) {
	f := newFixture(t)
	task := f.ready(t)
	m := directHealthMessage(t, f, f.founder.ID, true)
	f.now = f.now.Add(150 * time.Second)
	h := observedHealth(f.now.Unix() - 4*3600)
	h.Phase, h.ActionKind, h.ActionID, h.TaskID = "invoking", "task", task.ID, task.ID
	h.PhaseStartedAt, h.LastProgressAt, h.DeadlineAt = h.ObservedAt-500, h.ObservedAt-500, h.ObservedAt+1000
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	md, err := f.s.MessageDiagnostics(context.Background(), f.founder.ID, m.ID)
	if d.State != "working" || d.Reason != "within-phase-deadline" || err != nil || md.State != "queued" || md.Reason != "recipient-busy" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 0 {
		t.Fatal("bounded long work was incorrectly stuck or exposed another task", d, md, err)
	}
	data, _ := json.Marshal(md)
	if strings.Contains(string(data), task.ID) || strings.Contains(string(data), task.WorkspaceRef) {
		t.Fatal("message sender learned unrelated task details", string(data))
	}
}

func TestHealthSignedInPauseSessionAndUncertainFences(t *testing.T) {
	for _, blocker := range []string{"founder-paused", "session-fence", "invocation-uncertain", "unreconciled-claim"} {
		t.Run(blocker, func(t *testing.T) {
			f := newFixture(t)
			if blocker == "founder-paused" {
				_, err := f.s.SetMode(context.Background(), f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Explicit fixture pause"})
				if err != nil {
					t.Fatal(err)
				}
			}
			h := observedHealth(f.now.Unix())
			h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", blocker, h.ObservedAt
			h.Problems = []domain.HealthProblem{{Component: "controller", Code: domain.Prerequisite, FirstSeenAt: h.ObservedAt, LastSeenAt: h.ObservedAt}}
			publishHealth(t, f.s, f.worker.ID, h)
			sweepHealth(t, f.s)
			d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
			if d.Reason != blocker || d.State == "healthy" {
				t.Fatal("authentication erased a separate safety fence", d)
			}
			if blocker == "founder-paused" {
				if d.State != "paused" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 0 {
					t.Fatal("explicit pause was called stuck", d)
				}
				return
			}
			incidentID := d.Incident.ID
			notice, err := f.s.SendMessage(context.Background(), f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Safe fixture sign-in notice", Origin: "service", Kind: "authentication"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.s.ReceiveMessage(context.Background(), f.founder.ID, notice.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: notice.Revision}, State: "handled"})
			if err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Second)
			h.ObservedAt++
			publishHealth(t, f.s, f.worker.ID, h)
			sweepHealth(t, f.s)
			d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
			if d.Incident == nil || d.Incident.ID != incidentID || d.Incident.State != "active" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
				t.Fatal("notice acknowledgement or sign-in fabricated recovery", d)
			}
		})
	}
}

func TestHealthLiveAuthoritativeSessionExplainsLocalFence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture authorized session"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, TaskID: task.ID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture explicit session", ExpiresAt: f.now.Unix() + 600})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	m := directHealthMessage(t, f, f.founder.ID, true)
	f.now = f.now.Add(150 * time.Second)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "session-fence", h.ObservedAt-150
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	md, err := f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID)
	if d.Reason != "session-active" || strings.Contains(d.NextAction, "reconcile") || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 0 || err != nil || md.Reason != "session-active" {
		t.Fatal("legitimate live session was labelled interrupted cleanup", d, md, err)
	}
	_, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "close"})
	if err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Reason != "session-fence" || d.Incident == nil || d.Incident.State != "active" {
		t.Fatal("unproven local cleanup fence disappeared when the server session ended", d)
	}
}

func TestHealthReplyStagesAndUnrequestedMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	unrequested := directHealthMessage(t, f, f.founder.ID, false)
	f.now = f.now.Add(300 * time.Second)
	h := observedHealth(f.now.Unix())
	h.LastProgressAt = h.ObservedAt - 250
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	md, err := f.s.MessageDiagnostics(ctx, f.founder.ID, unrequested.ID)
	if err != nil || md.State != "not-requested" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 0 {
		t.Fatal("no-response message caused a stuck alert", md, err)
	}
	m := directHealthMessage(t, f, f.founder.ID, true)
	md, err = f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID)
	if err != nil || md.State != "queued" {
		t.Fatal(md, err)
	}
	h.Sequence++
	h.ActionKind, h.ActionID, h.Phase, h.DeadlineAt = "message", m.ID, "preparing", h.ObservedAt+60
	publishHealth(t, f.s, f.worker.ID, h)
	md, err = f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID)
	if err != nil || md.State != "generating" || md.Stage != "preparing" {
		t.Fatal(md, err)
	}
	h.Sequence++
	h.Phase, h.PendingReports = "reporting", 1
	publishHealth(t, f.s, f.worker.ID, h)
	md, err = f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID)
	if err != nil || md.State != "awaiting-delivery" {
		t.Fatal(md, err)
	}
	_, err = f.s.ReceiveMessage(ctx, f.worker.ID, m.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "handled"})
	if err != nil {
		t.Fatal(err)
	}
	md, err = f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID)
	if err != nil || md.State != "handled" || md.Reason != "handled-receipt-only" || !strings.Contains(md.NextAction, "not proof") {
		t.Fatal("handled receipt was treated as a verified reply", md, err)
	}
}

func TestHealthUnknownOldHeartbeatStaleReportsAndOfflineObservation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	before, _ := f.s.Me(ctx, f.worker.ID)
	a, err := f.s.Heartbeat(ctx, f.worker.ID)
	if err != nil || a.LastSeen != f.now.Unix() || a.Revision != before.Revision {
		t.Fatal("legacy heartbeat behavior changed", a, err)
	}
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "unknown" || d.Reason != "health-unobserved" || !d.Stale || d.Health != nil {
		t.Fatal("old worker was presented as healthy", d)
	}
	f.now = f.now.Add(91 * time.Second)
	if _, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Incident == nil || d.Incident.Kind != "health-unobserved" || d.LastSeen != f.now.Unix() {
		t.Fatal("live heartbeat hid missing health", d)
	}
	h := observedHealth(f.now.Unix())
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "healthy" || d.Incident.State != "resolved" {
		t.Fatal("actual health observation did not recover unknown state", d)
	}
	f.now = f.now.Add(91 * time.Second)
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "offline" || !d.Stale || d.Incident == nil || d.Incident.Kind != "heartbeat-missing" {
		t.Fatal("offline target needed its own running controller to be diagnosed", d)
	}
	// Replaying an old snapshot proves liveness without renewing its health observation.
	publishHealth(t, f.s, f.worker.ID, h)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Reason != "health-stale" || !d.Stale {
		t.Fatal("live heartbeat was substituted for report freshness", d)
	}
}

func TestHealthPrivacyProtectedKindAndForgedReferences(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	private := directHealthMessage(t, f, f.other.ID, true)
	h := observedHealth(f.now.Unix())
	h.ActionKind, h.ActionID, h.LastActionKind, h.LastActionID = "message", private.ID, "message", private.ID
	h.JournalID, h.Phase, h.DeadlineAt = "journal_private_fixture", "invoking", h.ObservedAt+120
	publishHealth(t, f.s, f.worker.ID, h)
	founder := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if founder.Health.ActionID != "" || founder.Health.LastActionID != "" || founder.Health.JournalID != "" {
		t.Fatal("general Founder health leaked private message or local journal references", founder)
	}
	self := diagnoseHealth(t, f.s, f.worker.ID, f.worker.ID)
	if self.Health.ActionID != private.ID || self.Health.JournalID != h.JournalID {
		t.Fatal("self diagnostics lost its own safe references", self)
	}
	_, err := f.s.MessageDiagnostics(ctx, f.founder.ID, private.ID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.AgentDiagnostics(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
	md, err := f.s.MessageDiagnostics(ctx, f.other.ID, private.ID)
	if err != nil || md.State != "generating" {
		t.Fatal("actual message party lost diagnosis", md, err)
	}
	_, err = f.s.SendMessage(ctx, f.other.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Forged health alert", Origin: "service", Kind: "health"})
	assertCode(t, err, domain.Invalid)
	for i, forged := range []domain.WorkerHealth{
		func() domain.WorkerHealth { n := h; n.ActionID = domain.NewID("message"); return n }(),
		func() domain.WorkerHealth { n := h; n.TaskID = domain.NewID("task"); return n }(),
	} {
		f.now = f.now.Add(time.Second)
		forged.Sequence, forged.ObservedAt = h.Sequence+int64(i)+1, f.now.Unix()
		if _, err := f.s.HeartbeatWithHealth(ctx, f.worker.ID, &forged); err != nil {
			t.Fatal("unproved references should be redacted without dropping liveness", err)
		}
		got := diagnoseHealth(t, f.s, f.worker.ID, f.worker.ID)
		if got.LastSeen != f.now.Unix() || got.Health.TaskID != "" || (i == 0 && got.Health.ActionID != "") {
			t.Fatal("forged reference became current scope or destroyed liveness", got)
		}
	}
	if got := diagnoseHealth(t, f.s, f.worker.ID, f.worker.ID); got.Health.LastActionID != private.ID || got.Health.Phase != "invoking" {
		t.Fatal("reference redaction lost valid observations", got)
	}
}

func TestHealthFreshControllerWatchErrorsAndIdleNotProgress(t *testing.T) {
	for _, component := range []string{"controller", "watch"} {
		t.Run(component, func(t *testing.T) {
			f := newFixture(t)
			m := directHealthMessage(t, f, f.founder.ID, true)
			f.now = f.now.Add(121 * time.Second)
			h := observedHealth(f.now.Unix())
			h.LastProgressAt, h.Phase = h.ObservedAt-120, "checking"
			h.Problems = []domain.HealthProblem{{Component: component, Code: domain.Unavailable, FirstSeenAt: h.ObservedAt - 100, LastSeenAt: h.ObservedAt}}
			publishHealth(t, f.s, f.worker.ID, h)
			sweepHealth(t, f.s)
			d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
			if d.Reason != component+"-error" || d.Stale || d.Incident == nil {
				t.Fatal("fresh heartbeat hid controller/watch errors", d)
			}
			f.now = f.now.Add(time.Second)
			h.ObservedAt++
			h.Problems = nil
			publishHealth(t, f.s, f.worker.ID, h)
			sweepHealth(t, f.s)
			d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
			md, err := f.s.MessageDiagnostics(context.Background(), f.founder.ID, m.ID)
			if d.Reason != "reply-overdue" || d.PendingReplies != 1 || err != nil || md.Reason != "reply-overdue" {
				t.Fatal("idle polls/heartbeat reset the reply wait clock", d, md, err)
			}
		})
	}
}

func TestHealthReportOrderingAndIdentityGenerationFences(t *testing.T) {
	f := newSecurityFixture(t)
	worker, _, _ := f.register(t, "health worker", 4101)
	workerCtx := securityOwnerContext(t, f.s, worker)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	h := observedHealth(f.now.Unix() + 3600)
	h.Sequence, h.Phase = 0, "unknown"
	if _, err := f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &h); err != nil {
		t.Fatal("pre-controller health rejected", err)
	}
	h.Sequence, h.Phase = 7, "idle"
	if _, err := f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &h); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	replayed, err := f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &h)
	if err != nil || replayed.LastSeen != f.now.Unix() {
		t.Fatal("lost acknowledgement discarded authenticated liveness", replayed, err)
	}
	if d := diagnoseHealth(t, f.s, f.founder.AgentID, worker.AgentID); d.ReceivedAt != f.now.Unix()-2 {
		t.Fatal("lost acknowledgement renewed report freshness", d)
	}
	h.ObservedAt++
	h.Problems = []domain.HealthProblem{{Component: "watch", Code: domain.Unavailable, FirstSeenAt: h.ObservedAt, LastSeenAt: h.ObservedAt}}
	if _, err := f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &h); err != nil {
		t.Fatal("same phase sequence with a fresh problem snapshot rejected", err)
	}
	for _, bad := range []domain.WorkerHealth{
		func() domain.WorkerHealth { n := h; n.Sequence--; n.ObservedAt++; return n }(),
		func() domain.WorkerHealth { n := h; n.Sequence++; n.ObservedAt--; n.Problems = nil; return n }(),
		func() domain.WorkerHealth { n := h; n.Problems = nil; return n }(),
	} {
		_, err := f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &bad)
		assertCode(t, err, domain.Conflict)
	}
	_, err = f.s.HeartbeatWithHealth(founderCtx, worker.AgentID, &h)
	assertCode(t, err, domain.Denied)
	sweepHealth(t, f.s)
	keys := securityPublic(t, "fresh recovered health worker")
	recovered, err := f.s.RecoverIdentity(founderCtx, f.founder.AgentID, worker.AgentID, domain.RecoverIdentityRequest{Command: op(), Identity: keys.public, Reason: "Fixture explicit generation change"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, keys, worker.UID)
	d := diagnoseHealth(t, f.s, f.founder.AgentID, worker.AgentID)
	if d.Health != nil || d.LastSeen != 0 || !d.Stale || d.Incident != nil {
		t.Fatal("pre-recovery report/incident appeared current", d)
	}
	_, err = f.s.HeartbeatWithHealth(workerCtx, worker.AgentID, &h)
	assertCode(t, err, domain.Denied)
	freshCtx := securityOwnerContext(t, f.s, recovered)
	fresh := observedHealth(f.now.Unix() - 3600)
	if _, err := f.s.HeartbeatWithHealth(freshCtx, worker.AgentID, &fresh); err != nil {
		t.Fatal("new generation could not reset observation sequence/remote clock", err)
	}
	if got := diagnoseHealth(t, f.s, f.founder.AgentID, worker.AgentID); got.State != "healthy" || got.Health.Sequence != 1 {
		t.Fatal(got)
	}
}

func TestHealthIncidentReopenMissingFounderAndObservedRecovery(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	worker, _, _ := f.register(t, "durable health worker", 4101)
	h := observedHealth(f.now.Unix())
	h.Phase, h.ActionKind, h.ActionID = "invoking", "onboarding", "onboarding"
	h.PhaseStartedAt, h.DeadlineAt = h.ObservedAt-200, h.ObservedAt-40
	publishHealth(t, f.s, worker.AgentID, h)
	if err := f.s.BanIdentity(ctx, f.founder.AgentID, "Fixture Founder unavailable"); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	episode, err := healthSetting[healthEpisode](ctx, f.s.db, healthIncidentKey+worker.AgentID)
	if err != nil || episode.DeliveryState != "pending" || episode.Incident.ID == "" || len(healthMessages(t, f.s, worker.AgentID, f.founder.AgentID)) != 0 {
		t.Fatal("missing recipient was falsely marked delivered", episode, err)
	}
	originalID := episode.Incident.ID
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	sweepHealth(t, f.s)
	keys := securityPublic(t, "recovered available Founder")
	founder, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Identity: keys.public, Reason: "Fixture root-local recovery boundary"})
	if err != nil {
		t.Fatal(err)
	}
	founder, _ = securityActivate(t, f.s, founder, keys, f.founder.UID)
	sweepHealth(t, f.s)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := f.s.SweepHealth(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	messages := healthMessages(t, f.s, worker.AgentID, founder.AgentID)
	episode, _ = healthSetting[healthEpisode](ctx, f.s.db, healthIncidentKey+worker.AgentID)
	if len(messages) != 1 || episode.DeliveryState != "delivered" || episode.Incident.ID != originalID {
		t.Fatal("restart/replay/polling duplicated or lost pending Founder delivery", messages, episode)
	}
	f.now = f.now.Add(time.Second)
	h = observedHealth(f.now.Unix())
	h.Sequence = 2
	publishHealth(t, f.s, worker.AgentID, h)
	sweepHealth(t, f.s)
	sweepHealth(t, f.s)
	episode, _ = healthSetting[healthEpisode](ctx, f.s.db, healthIncidentKey+worker.AgentID)
	messages = healthMessages(t, f.s, worker.AgentID, founder.AgentID)
	if episode.Incident.ID != originalID || episode.Incident.State != "resolved" || len(messages) != 2 {
		t.Fatal("observed recovery lost alert history or duplicated recovery", episode, messages)
	}
	var settings int
	if err := f.s.db.QueryRow("SELECT count(*) FROM settings WHERE key LIKE 'health.%:' || ?", worker.AgentID).Scan(&settings); err != nil || settings != 2 {
		t.Fatal("health current-state retention was not bounded", settings, err)
	}
}

func TestHealthTaskAlertRecipientsFollowCurrentDelegation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project, _ := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	project, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	h := observedHealth(f.now.Unix())
	h.Phase, h.ActionKind, h.ActionID, h.TaskID = "invoking", "task", task.ID, task.ID
	h.PhaseStartedAt, h.DeadlineAt = h.ObservedAt-100, h.ObservedAt-40
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	if len(healthMessages(t, f.s, f.worker.ID, f.other.ID)) != 1 {
		t.Fatal("current task delegate did not receive the incident")
	}
	_, err = f.s.AgentDiagnostics(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	h.ObservedAt++
	h.Blocker, h.BlockedSinceAt = "provider-permission", h.ObservedAt
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	if len(healthMessages(t, f.s, f.worker.ID, f.other.ID)) != 1 {
		t.Fatal("former delegate received a later incident/transition")
	}
}
