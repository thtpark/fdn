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

func statusRequest(text string) domain.AgentStatusRequest {
	return domain.AgentStatusRequest{Command: op(), Text: &text}
}

func publishStatus(t *testing.T, s *Store, actor string, text string) domain.AgentStatusReceipt {
	t.Helper()
	receipt, err := s.PublishAgentStatus(context.Background(), actor, actor, statusRequest(text))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func savedStatus(t *testing.T, s *Store, actor string) agentStatusRecord {
	t.Helper()
	r, err := healthSetting[agentStatusRecord](context.Background(), s.db, agentStatusKey+actor)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rosterStatus(t *testing.T, s *Store, viewer, actor string) *domain.AgentTextStatus {
	t.Helper()
	a, err := s.Agent(context.Background(), viewer, actor)
	if err != nil {
		t.Fatal(err)
	}
	return a.Status
}

func statusCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func advanceStatusHealth(f *fixture, h *domain.WorkerHealth, seconds int64) {
	f.now = f.now.Add(time.Duration(seconds) * time.Second)
	h.ObservedAt += seconds
	h.Sequence++
}

func TestAgentStatusAutomaticFirstPublicationCoalescesLatestAndReadsStayReadOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	start := f.now.Unix()
	h := observedHealth(start + 6*3600)
	h.Activity = "idle"
	events := statusCount(t, f.s, "SELECT count(*) FROM events")
	histories := statusCount(t, f.s, "SELECT count(*) FROM histories")
	first := publishHealth(t, f.s, f.worker.ID, h)
	if first.Status == nil || first.Status.Source != "controller" || first.Status.State != "idle" || first.Status.Stale || first.Status.UpdatedAt != start || first.Status.ObservedAt != start {
		t.Fatal("first publication did not use safe server time", first)
	}
	advanceStatusHealth(f, &h, 10)
	h.Phase, h.Activity, h.ActionKind, h.ActionID, h.TaskID = "invoking", "execution", "task", task.ID, task.ID
	publishHealth(t, f.s, f.worker.ID, h)
	advanceStatusHealth(f, &h, 10)
	h.Activity = "verification"
	publishHealth(t, f.s, f.worker.ID, h)
	if r := savedStatus(t, f.s, f.worker.ID); r.Pending == nil || r.Pending.Text != domain.SummarizeActivity(h).Text || r.Published.UpdatedAt != start {
		t.Fatal("rapid observations did not coalesce to latest", r)
	}
	f.now = f.now.Add(40 * time.Second)
	before, _ := encode(savedStatus(t, f.s, f.worker.ID))
	for _, viewer := range []domain.Agent{f.founder, f.worker, f.other} {
		status := rosterStatus(t, f.s, viewer.ID, f.worker.ID)
		if status == nil || status.State != "idle" || status.UpdatedAt != start {
			t.Fatal("public read flushed a pending publication", status)
		}
		if _, err := f.s.Agents(ctx, viewer.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Me(ctx, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	if d := diagnoseHealth(t, f.s, f.worker.ID, f.worker.ID); d.Status == nil || d.Status.State != "idle" {
		t.Fatal("diagnostic read flushed pending status", d)
	}
	after, _ := encode(savedStatus(t, f.s, f.worker.ID))
	if string(before) != string(after) {
		t.Fatal("status reads mutated service-owned settings")
	}
	sweepHealth(t, f.s)
	status := rosterStatus(t, f.s, f.other.ID, f.worker.ID)
	if status == nil || status.Text != domain.SummarizeActivity(h).Text || status.UpdatedAt != start+60 || status.ObservedAt != start+20 || status.Stale || savedStatus(t, f.s, f.worker.ID).Pending != nil {
		t.Fatal("monitor did not flush latest candidate without another heartbeat", status)
	}
	advanceStatusHealth(f, &h, 10)
	unchanged := publishHealth(t, f.s, f.worker.ID, h)
	if unchanged.Status.UpdatedAt != start+60 || unchanged.Status.ObservedAt != start+70 || unchanged.Revision != f.worker.Revision || unchanged.LastSeen != start+70 {
		t.Fatal("unchanged observation became a publication or authority change", unchanged)
	}
	f.now = f.now.Add(time.Second)
	retry := publishHealth(t, f.s, f.worker.ID, h)
	if retry.Status.ObservedAt != start+70 || retry.Status.UpdatedAt != start+60 || retry.LastSeen != start+71 {
		t.Fatal("exact heartbeat retry renewed status observation", retry)
	}
	if statusCount(t, f.s, "SELECT count(*) FROM events") != events || statusCount(t, f.s, "SELECT count(*) FROM histories") != histories || statusCount(t, f.s, "SELECT count(*) FROM settings WHERE key LIKE 'agent.status:%'") != 1 {
		t.Fatal("automatic status created history, events or unbounded settings")
	}
	var data string
	if err := f.s.db.QueryRow("SELECT data FROM agents WHERE id=?", f.worker.ID).Scan(&data); err != nil || strings.Contains(data, `"status"`) {
		t.Fatal("public projection entered authoritative agent data", data, err)
	}
}

func TestAgentStatusReturningToPublishedCandidateSuppressesPendingChange(t *testing.T) {
	f := newFixture(t)
	h := observedHealth(f.now.Unix())
	first := publishHealth(t, f.s, f.worker.ID, h).Status
	advanceStatusHealth(f, &h, 10)
	h.Phase, h.Activity = "invoking", "research"
	publishHealth(t, f.s, f.worker.ID, h)
	advanceStatusHealth(f, &h, 10)
	h.Phase, h.Activity = "idle", ""
	current := publishHealth(t, f.s, f.worker.ID, h).Status
	if r := savedStatus(t, f.s, f.worker.ID); r.Pending != nil || current.UpdatedAt != first.UpdatedAt || current.ObservedAt != f.now.Unix() {
		t.Fatal("a cancelled rapid change was later publishable", current, r)
	}
	f.now = f.now.Add(60 * time.Second)
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status.Text != first.Text || status.UpdatedAt != first.UpdatedAt {
		t.Fatal("suppressed change was published by monitor", status)
	}
}

func TestAgentStatusAuthoredNoteScopedCoalescedClearedAndInvalidated(t *testing.T) {
	f := newFixture(t)
	task := f.ready(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Activity, h.ActionKind, h.ActionID, h.TaskID = "invoking", "execution", "task", task.ID, task.ID
	publishHealth(t, f.s, f.worker.ID, h)
	advanceStatusHealth(f, &h, 60)
	first := publishStatus(t, f.s, f.worker.ID, "  Testing the approved change  ")
	if first.Pending || first.Status == nil || first.Status.Text != "Testing the approved change" || first.Status.Source != "agent" || first.Status.State != "working" {
		t.Fatal("optional authored publication failed", first)
	}
	if r := savedStatus(t, f.s, f.worker.ID); r.Note == nil || r.Note.ContextKey != r.Automatic.ContextKey {
		t.Fatal("note did not use the shared semantic context key", r)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase = "checking"
	same := publishHealth(t, f.s, f.worker.ID, h).Status
	if same == nil || same.Text != first.Status.Text || same.UpdatedAt != first.Status.UpdatedAt || same.ObservedAt != f.now.Unix() {
		t.Fatal("bookkeeping poll erased a scoped note or republished it", same)
	}
	latest := publishStatus(t, f.s, f.worker.ID, "Checking the regression cases")
	if !latest.Pending || latest.NextUpdateAt != first.Status.UpdatedAt+60 || latest.Status.Text != first.Status.Text {
		t.Fatal("normal authored change bypassed the interval", latest)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase, h.Blocker = "blocked", "workspace-permission"
	blocked := publishHealth(t, f.s, f.worker.ID, h).Status
	if blocked == nil || blocked.State != "blocked" || blocked.Source != "controller" || blocked.UpdatedAt != f.now.Unix() || savedStatus(t, f.s, f.worker.ID).Note != nil || savedStatus(t, f.s, f.worker.ID).Pending != nil {
		t.Fatal("important blocker did not immediately replace authored and pending text", blocked)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase, h.Blocker, h.Activity = "invoking", "", "verification"
	recovered := publishHealth(t, f.s, f.worker.ID, h).Status
	if recovered == nil || recovered.State != "working" || recovered.Source != "controller" || recovered.UpdatedAt != f.now.Unix() {
		t.Fatal("actual recovery did not publish immediately", recovered)
	}
	advanceStatusHealth(f, &h, 60)
	publishHealth(t, f.s, f.worker.ID, h)
	note := publishStatus(t, f.s, f.worker.ID, "Testing the final checks")
	if note.Pending || note.Status.Source != "agent" {
		t.Fatal(note)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase, h.Activity, h.ActionKind, h.ActionID, h.TaskID = "idle", "idle", "", "", ""
	completed := publishHealth(t, f.s, f.worker.ID, h)
	if completed.Status != nil || savedStatus(t, f.s, f.worker.ID).Note != nil {
		t.Fatal("completed activity left its expired authored note visible", completed)
	}
	f.now = f.now.Add(59 * time.Second)
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.Source != "controller" || status.State != "idle" {
		t.Fatal("expired note did not restore coalesced automatic status", status)
	}
	advanceStatusHealth(f, &h, 60)
	publishHealth(t, f.s, f.worker.ID, h)
	publishStatus(t, f.s, f.worker.ID, "Reviewing the next step")
	clear := publishStatus(t, f.s, f.worker.ID, "   ")
	if !clear.Pending || savedStatus(t, f.s, f.worker.ID).Note != nil {
		t.Fatal("empty explicit text did not clear the note", clear)
	}
	f.now = f.now.Add(60 * time.Second)
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.Source != "controller" || status.State != "idle" || status.Text == "Reviewing the next step" {
		t.Fatal("clear did not restore automatic status", status)
	}
}

func TestAgentStatusAuthoritativeModeTransitionsDoNotClaimResumedInference(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	h := observedHealth(f.now.Unix())
	h.Mode = "manual"
	if status := publishHealth(t, f.s, f.worker.ID, h).Status; status == nil || status.State != "idle" {
		t.Fatal("worker-reported mode overrode server authority", status)
	}
	f.now = f.now.Add(time.Second)
	paused, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Fixture explicit pause"})
	if err != nil {
		t.Fatal(err)
	}
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.State != "paused" || status.UpdatedAt != f.now.Unix() {
		t.Fatal("authoritative pause was not immediately public", status)
	}
	advanceStatusHealth(f, &h, 1)
	h.Mode, h.Phase, h.Activity = "ai", "invoking", "execution"
	if status := publishHealth(t, f.s, f.worker.ID, h).Status; status == nil || status.State != "paused" || status.UpdatedAt != paused.ModeChangedAt {
		t.Fatal("target observation bypassed Founder pause", status)
	}
	f.now = f.now.Add(time.Second)
	_, err = f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: paused.Revision}, Mode: "ai", Reason: "Fixture explicit resume intent"})
	if err != nil {
		t.Fatal(err)
	}
	resume := rosterStatus(t, f.s, f.other.ID, f.worker.ID)
	if resume == nil || resume.State != "unknown" || !resume.Stale || resume.Text != "Automation enabled; awaiting activity" || resume.UpdatedAt != f.now.Unix() {
		t.Fatal("mode alone claimed resumed inference", resume)
	}
	f.now = f.now.Add(time.Second)
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.Text != resume.Text || status.UpdatedAt != resume.UpdatedAt {
		t.Fatal("replayed observation invented a resumed phase", status)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase, h.Blocker = "blocked", "authentication-required"
	blocked := publishHealth(t, f.s, f.worker.ID, h).Status
	if blocked == nil || blocked.State != "blocked" || blocked.UpdatedAt != f.now.Unix() {
		t.Fatal(blocked)
	}
	advanceStatusHealth(f, &h, 1)
	h.Phase, h.Activity, h.Blocker = "idle", "idle", ""
	if status := publishHealth(t, f.s, f.worker.ID, h).Status; status == nil || status.State != "idle" || status.UpdatedAt != f.now.Unix() {
		t.Fatal("observed recovery was delayed", status)
	}
}

func TestAgentStatusExplicitNotesCanAnnotateStableSafetyStateWithoutChangingAuthority(t *testing.T) {
	for _, state := range []string{"blocked", "paused"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			h := observedHealth(f.now.Unix())
			h.Phase, h.Activity, h.Blocker = "blocked", "approval", "approval-required"
			var paused domain.Agent
			if state == "paused" {
				var err error
				paused, err = f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Fixture explicit pause"})
				if err != nil {
					t.Fatal(err)
				}
			}
			publishHealth(t, f.s, f.worker.ID, h)
			advanceStatusHealth(f, &h, 60)
			note := publishStatus(t, f.s, f.worker.ID, "Reviewing the current prerequisite")
			if note.Pending || note.Status == nil || note.Status.Source != "agent" || note.Status.State != state {
				t.Fatal("explicit annotation was ignored or changed the safety state", note)
			}
			advanceStatusHealth(f, &h, 1)
			if status := publishHealth(t, f.s, f.worker.ID, h).Status; status == nil || status.Source != "agent" || status.Text != note.Status.Text || status.State != state || status.UpdatedAt != note.Status.UpdatedAt {
				t.Fatal("unchanged safety context erased or republished an explicit note", status)
			}
			advanceStatusHealth(f, &h, 1)
			if state == "paused" {
				if _, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: paused.Revision}, Mode: "ai", Reason: "Fixture resume intent"}); err != nil {
					t.Fatal(err)
				}
			} else {
				h.Phase, h.Activity, h.Blocker = "idle", "idle", ""
				publishHealth(t, f.s, f.worker.ID, h)
			}
			if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.Source != "controller" || status.UpdatedAt != f.now.Unix() || savedStatus(t, f.s, f.worker.ID).Note != nil {
				t.Fatal("important transition failed to override the stable-context note", status)
			}
		})
	}
}

func TestAgentStatusLiveSessionIsNotUnprovenRecovery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture public session status"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, TaskID: task.ID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture explicit session", ExpiresAt: f.now.Unix() + 180})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	h := observedHealth(f.now.Unix())
	h.Phase, h.Activity, h.Blocker = "blocked", "session", "session-fence"
	live := publishHealth(t, f.s, f.worker.ID, h).Status
	if live == nil || live.State != "working" || live.Text != "Using an assistance session" || strings.Contains(live.Text, "recovery") {
		t.Fatal("live authoritative session was called recovering", live)
	}
	f.now = f.now.Add(time.Second)
	_, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "close"})
	if err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.other.ID, f.worker.ID); status == nil || status.State != "blocked" || status.Text != "Waiting for session recovery" || status.UpdatedAt != f.now.Unix() || status.ObservedAt != live.ObservedAt {
		t.Fatal("ended session cleared an unproven local fence or renewed its health observation", status)
	}
}

func TestAgentStatusMissingLegacyAndStaleHealthNeverInventsFreshActivity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if a, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil || a.Status != nil {
		t.Fatal("legacy no-health heartbeat invented activity", a, err)
	}
	f.now = f.now.Add(120 * time.Second)
	h := observedHealth(f.now.Unix() - 6*3600)
	h.Phase, h.ActionKind, h.ActionID, h.JournalID = "invoking", "onboarding", "private_action_identifier", "journal_private_secret"
	h.Model, h.ProviderVersion = "private-model-fixture", "private-version-fixture"
	first := publishHealth(t, f.s, f.worker.ID, h).Status
	if first == nil || first.Text != "Reviewing computer capabilities" || first.State != "working" || first.UpdatedAt != f.now.Unix() || first.ObservedAt != f.now.Unix() || first.Stale {
		t.Fatal("legacy health lacked a safe coarse summary", first)
	}
	data, _ := json.Marshal(first)
	setting, _ := encode(savedStatus(t, f.s, f.worker.ID))
	for _, private := range []string{h.ActionID, h.JournalID, h.Model, h.ProviderVersion} {
		if strings.Contains(string(data), private) || strings.Contains(string(setting), private) {
			t.Fatal("automatic status copied private health context", string(data), string(setting))
		}
	}
	f.now = f.now.Add(91 * time.Second)
	replayed := publishHealth(t, f.s, f.worker.ID, h).Status
	if replayed == nil || !replayed.Stale || replayed.ObservedAt != first.ObservedAt || replayed.UpdatedAt != first.UpdatedAt {
		t.Fatal("old observation or target skew renewed public freshness", replayed)
	}
	if a, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil || a.Status == nil || !a.Status.Stale || a.Status.ObservedAt != first.ObservedAt || a.Status.Text != first.Text {
		t.Fatal("command-only heartbeat erased or refreshed prior activity", a, err)
	}
	other := observedHealth(f.now.Unix())
	other.Sequence, other.Phase = 0, "unknown"
	if status := publishHealth(t, f.s, f.other.ID, other).Status; status == nil || status.State != "unknown" || !status.Stale {
		t.Fatal("unknown observation defaulted to healthy activity", status)
	}
}

func TestAgentStatusSelfOnlyShapeAndNoAuthorityOrReadReceiptSideEffects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	message := directHealthMessage(t, f, f.other.ID, true)
	before, _ := get[domain.Agent](ctx, f.s.db, "agents", f.worker.ID)
	events := statusCount(t, f.s, "SELECT count(*) FROM events")
	histories := statusCount(t, f.s, "SELECT count(*) FROM histories")
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		_, err := f.s.PublishAgentStatus(ctx, actor, f.worker.ID, statusRequest("Cannot write for another identity"))
		assertCode(t, err, domain.Denied)
	}
	for _, req := range []domain.AgentStatusRequest{{Command: op()}, {Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, Text: statusRequest("Bad revision").Text}, statusRequest("token=fixture-not-a-real-credential"), statusRequest("Invalid\nline")} {
		_, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
		assertCode(t, err, domain.Invalid)
	}
	first := publishStatus(t, f.s, f.worker.ID, "Testing the approved change")
	if first.Status == nil || first.Status.Source != "agent" || first.Status.State != "unknown" || !first.Status.Stale || first.Pending {
		t.Fatal("authored text without health falsely claimed known activity", first)
	}
	for _, viewer := range []domain.Agent{f.founder, f.worker, f.other} {
		if status := rosterStatus(t, f.s, viewer.ID, f.worker.ID); !reflect.DeepEqual(status, first.Status) {
			t.Fatal("active roster viewer could not see intentional publication", status)
		}
	}
	_, err := f.s.AgentDiagnostics(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
	after, _ := get[domain.Agent](ctx, f.s.db, "agents", f.worker.ID)
	current, _ := f.s.Task(ctx, f.worker.ID, task.ID)
	read, _ := f.s.Message(ctx, f.worker.ID, message.ID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(task, current) || read.ReadAt != 0 || read.State != message.State || read.Revision != message.Revision || statusCount(t, f.s, "SELECT count(*) FROM events") != events || statusCount(t, f.s, "SELECT count(*) FROM histories") != histories {
		t.Fatal("status changed agent authority, work, heartbeat, history or explicit reading", before, after, current, read)
	}
}

func TestAgentStatusFailedWritesAndExactImmutableReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := statusRequest("  Testing the change  ")
	if _, err := f.s.db.Exec("CREATE TRIGGER fixture_refuse_status BEFORE INSERT ON settings WHEN NEW.key LIKE 'agent.status:%' BEGIN SELECT RAISE(ABORT, 'fixture status persistence failure'); END"); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
	if err == nil || statusCount(t, f.s, "SELECT count(*) FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID) != 0 || statusCount(t, f.s, "SELECT count(*) FROM settings WHERE key=?", agentStatusKey+f.worker.ID) != 0 {
		t.Fatal("failed status write left an accepted receipt or mutation", err)
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_refuse_status"); err != nil {
		t.Fatal(err)
	}
	first, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(120 * time.Second)
	publishStatus(t, f.s, f.worker.ID, "Checking the next change")
	replayed, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatal("exact replay returned current mutable status instead of immutable receipt", first, replayed, err)
	}
	changed := req
	normalized := strings.TrimSpace(*req.Text)
	changed.Text = &normalized
	_, err = f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, changed)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.PublishAgentStatus(ctx, f.worker.ID, f.other.ID, req)
	assertCode(t, err, domain.Conflict)
	if maxSize := statusCount(t, f.s, "SELECT max(length(result)) FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID); maxSize > 2048 {
		t.Fatal("status receipt is not bounded", maxSize)
	}
}

func TestAgentStatusRestartGenerationAndRevocationFenceReceiptsAndPendingText(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	worker, _, _ := f.register(t, "status worker", 4101)
	workerCtx := securityOwnerContext(t, f.s, worker)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	req := statusRequest("Testing the original generation")
	first, err := f.s.PublishAgentStatus(workerCtx, worker.AgentID, worker.AgentID, req)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(10 * time.Second)
	pendingReq := statusRequest("Unpublished latest text")
	pending, err := f.s.PublishAgentStatus(workerCtx, worker.AgentID, worker.AgentID, pendingReq)
	if err != nil || !pending.Pending {
		t.Fatal(pending, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	replayed, err := f.s.PublishAgentStatus(workerCtx, worker.AgentID, worker.AgentID, req)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatal("restart lost exact status receipt", first, replayed, err)
	}
	f.now = f.now.Add(50 * time.Second)
	sweepHealth(t, f.s)
	if status := rosterStatus(t, f.s, f.founder.AgentID, worker.AgentID); status == nil || status.Text != *pendingReq.Text || status.UpdatedAt != f.now.Unix() || status.ObservedAt != f.now.Unix()-50 {
		t.Fatal("restart lost pending status or fabricated a fresh observation", status)
	}
	fresh := securityPublic(t, "fresh status generation")
	recovered, err := f.s.RecoverIdentity(founderCtx, f.founder.AgentID, worker.AgentID, domain.RecoverIdentityRequest{Command: op(), Identity: fresh.public, Reason: "Fixture fresh identity status fencing"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, worker.UID)
	if status := rosterStatus(t, f.s, f.founder.AgentID, worker.AgentID); status != nil || statusCount(t, f.s, "SELECT count(*) FROM settings WHERE key=?", agentStatusKey+worker.AgentID) != 0 {
		t.Fatal("new generation inherited published, pending or authored status", status)
	}
	_, err = f.s.PublishAgentStatus(workerCtx, worker.AgentID, worker.AgentID, req)
	assertCode(t, err, domain.Denied)
	freshCtx := securityOwnerContext(t, f.s, recovered)
	_, err = f.s.PublishAgentStatus(freshCtx, worker.AgentID, worker.AgentID, req)
	assertCode(t, err, domain.Conflict)
	freshReq := statusRequest("Fresh deliberate publication")
	if _, err = f.s.PublishAgentStatus(freshCtx, worker.AgentID, worker.AgentID, freshReq); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Revoke(founderCtx, f.founder.AgentID, worker.AgentID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.PublishAgentStatus(freshCtx, worker.AgentID, worker.AgentID, freshReq)
	assertCode(t, err, domain.Denied)
	_, err = f.s.PublishAgentStatus(ctx, worker.AgentID, worker.AgentID, statusRequest("Revoked publication"))
	assertCode(t, err, domain.Denied)
}

func TestAgentStatusHeartbeatFailureIsAtomicAndNewTaskInvalidatesNote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Activity, h.ActionKind, h.ActionID, h.TaskID = "invoking", "execution", "task", task.ID, task.ID
	publishHealth(t, f.s, f.worker.ID, h)
	beforeAgent, _ := get[domain.Agent](ctx, f.s.db, "agents", f.worker.ID)
	beforeHealth, _ := healthSetting[healthReport](ctx, f.s.db, healthReportKey+f.worker.ID)
	beforeStatus, _ := encode(savedStatus(t, f.s, f.worker.ID))
	if _, err := f.s.db.Exec("CREATE TRIGGER fixture_fail_status_heartbeat BEFORE INSERT ON settings WHEN NEW.key LIKE 'agent.status:%' BEGIN SELECT RAISE(ABORT, 'fixture status heartbeat failure'); END"); err != nil {
		t.Fatal(err)
	}
	advanceStatusHealth(f, &h, 10)
	h.Activity = "verification"
	if _, err := f.s.HeartbeatWithHealth(ctx, f.worker.ID, &h); err == nil {
		t.Fatal("failed status persistence accepted heartbeat")
	}
	afterAgent, _ := get[domain.Agent](ctx, f.s.db, "agents", f.worker.ID)
	afterHealth, _ := healthSetting[healthReport](ctx, f.s.db, healthReportKey+f.worker.ID)
	afterStatus, _ := encode(savedStatus(t, f.s, f.worker.ID))
	if !reflect.DeepEqual(beforeAgent, afterAgent) || !reflect.DeepEqual(beforeHealth, afterHealth) || string(beforeStatus) != string(afterStatus) {
		t.Fatal("failed heartbeat left partial status, liveness or health mutation")
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_fail_status_heartbeat"); err != nil {
		t.Fatal(err)
	}
	advanceStatusHealth(f, &h, 50)
	h.Activity = "execution"
	publishHealth(t, f.s, f.worker.ID, h)
	publishStatus(t, f.s, f.worker.ID, "Testing this task only")
	second := f.ready(t)
	advanceStatusHealth(f, &h, 1)
	h.ActionID, h.TaskID = second.ID, second.ID
	if a := publishHealth(t, f.s, f.worker.ID, h); a.Status != nil || savedStatus(t, f.s, f.worker.ID).Note != nil {
		t.Fatal("identical generic wording carried an authored note into another task", a)
	}
}

func TestAgentStatusAuthoredReplayDoesNotRenewFreshnessOrNoteAge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	h := observedHealth(f.now.Unix())
	h.Phase, h.Activity = "invoking", "execution"
	publishHealth(t, f.s, f.worker.ID, h)
	f.now = f.now.Add(60 * time.Second)
	req := statusRequest("Testing the current change")
	first, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
	if err != nil || first.Status == nil || first.Status.State != "working" || first.Status.Stale {
		t.Fatal(first, err)
	}
	before, _ := encode(savedStatus(t, f.s, f.worker.ID))
	f.now = f.now.Add(91 * time.Second)
	replayed, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatal("replay was not the exact immutable receipt", first, replayed, err)
	}
	after, _ := encode(savedStatus(t, f.s, f.worker.ID))
	public := rosterStatus(t, f.s, f.other.ID, f.worker.ID)
	if string(before) != string(after) || public == nil || !public.Stale || public.ObservedAt != first.Status.ObservedAt || public.UpdatedAt != first.Status.UpdatedAt {
		t.Fatal("replayed custom status renewed note age or public freshness", public)
	}
}

func TestAgentStatusReplayCannotReturnAnotherActorsReceipt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := statusRequest("My deliberate publication")
	if _, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.other.ID, f.worker.ID} {
		forged := agentStatusResult{Receipt: domain.AgentStatusReceipt{AgentID: f.other.ID, Status: &domain.AgentTextStatus{Text: "Another actor's result"}}}
		payload, err := encode(forged)
		if err != nil {
			t.Fatal(err)
		}
		data, err := encode(storedResult{Kind: "agent-status", ID: id, Value: payload})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.db.Exec("UPDATE operations SET result=? WHERE actor_id=? AND operation_id=?", data, f.worker.ID, req.OperationID); err != nil {
			t.Fatal(err)
		}
		receipt, err := f.s.PublishAgentStatus(ctx, f.worker.ID, f.worker.ID, req)
		assertCode(t, err, domain.Denied)
		if receipt.Status != nil || receipt.AgentID != "" {
			t.Fatal("inconsistent operation receipt returned another actor's data", receipt)
		}
	}
}

func TestAgentStatusConcurrentUpdatesKeepOneLatestCandidate(t *testing.T) {
	f := newFixture(t)
	publishStatus(t, f.s, f.worker.ID, "Initial publication")
	f.now = f.now.Add(time.Second)
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			_, err := f.s.PublishAgentStatus(context.Background(), f.worker.ID, f.worker.ID, statusRequest("Testing concurrent changes"))
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if r := savedStatus(t, f.s, f.worker.ID); r.Pending == nil || r.Pending.Text != "Testing concurrent changes" || statusCount(t, f.s, "SELECT count(*) FROM settings WHERE key LIKE 'agent.status:%'") != 1 {
		t.Fatal("concurrent requests did not preserve one bounded candidate", r)
	}
}
