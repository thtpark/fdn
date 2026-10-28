package foundation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestHealthRetainedStaleJournalDoesNotRejectLivenessOrRouteDelegates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "invocation-uncertain", h.ObservedAt
	h.ActionKind, h.ActionID, h.TaskID = "task", task.ID, task.ID
	h.LastActionKind, h.LastActionID, h.JournalID = "task", task.ID, "journal_retained_private"
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	original := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident.ID
	task = f.change(t, f.founder.ID, task.ID, "assign", func(r *domain.TaskChange) { r.AgentID, r.Provider = f.other.ID, "codex" })
	project, _ := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if i == 1 {
			task = f.change(t, f.founder.ID, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.WontDo })
			f.change(t, f.founder.ID, task.ID, "archive", nil)
		}
		f.now = f.now.Add(time.Second)
		h.ObservedAt++
		publishHealth(t, f.s, f.worker.ID, h)
		sweepHealth(t, f.s)
		d := diagnoseHealth(t, f.s, f.worker.ID, f.worker.ID)
		if d.LastSeen != f.now.Unix() || d.Stale || d.Reason != "invocation-uncertain" || d.Health.ActionID != "" || d.Health.TaskID != "" || d.Health.LastActionID != "" || d.Incident.ID != original {
			t.Fatal("retained stale references rejected liveness or changed an unchanged fence episode", d)
		}
		if len(healthMessages(t, f.s, f.worker.ID, f.other.ID)) != 0 || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
			t.Fatal("old journal granted new task-delegate visibility or duplicated the incident")
		}
	}
}

func TestHealthNilHeartbeatCannotResurrectOldReportOrResetOrdering(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	h := observedHealth(f.now.Unix())
	h.Sequence = 4
	publishHealth(t, f.s, f.worker.ID, h)
	f.now = f.now.Add(time.Second)
	if _, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	if d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID); d.Health != nil || d.State != "unknown" || !d.Stale {
		t.Fatal("command-only heartbeat inherited a healthy report", d)
	}
	publishHealth(t, f.s, f.worker.ID, h)
	if d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID); d.Health != nil || d.State != "unknown" {
		t.Fatal("old lost-ack replay resurrected a no-longer-current report", d)
	}
	older := h
	older.Sequence--
	older.ObservedAt++
	_, err := f.s.HeartbeatWithHealth(ctx, f.worker.ID, &older)
	assertCode(t, err, domain.Conflict)
	h.ObservedAt++
	publishHealth(t, f.s, f.worker.ID, h)
	if d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID); d.Health == nil || d.State != "healthy" || d.Stale {
		t.Fatal("fresh same-phase observation could not restore visibility", d)
	}
}

func TestHealthNewDelegateGetsStableEpisodeWithoutNewIncident(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	h := observedHealth(f.now.Unix())
	h.ActionKind, h.ActionID, h.TaskID = "task", task.ID, task.ID
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "authentication-required", h.ObservedAt
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	original := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident.ID
	project, _ := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID}); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	sweepHealth(t, f.s)
	if len(healthMessages(t, f.s, f.worker.ID, f.other.ID)) != 1 || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 || diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident.ID != original {
		t.Fatal("new current authority lost delivery or duplicated existing recipients")
	}
}

func TestHealthRecoveryPendingWithoutFounderKeepsAlertHistory(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	worker, _, _ := f.register(t, "pending recovery worker", 4101)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "invocation-uncertain", h.ObservedAt
	publishHealth(t, f.s, worker.AgentID, h)
	if err := f.s.BanIdentity(ctx, f.founder.AgentID, "Fixture unavailable Founder"); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	f.now = f.now.Add(time.Second)
	h = observedHealth(f.now.Unix())
	h.Sequence = 2
	publishHealth(t, f.s, worker.AgentID, h)
	sweepHealth(t, f.s)
	episode, err := healthSetting[healthEpisode](ctx, f.s.db, healthIncidentKey+worker.AgentID)
	if err != nil || episode.Incident.State != "resolved" || episode.DeliveryState != "pending" {
		t.Fatal("unavailable Founder discarded an observed recovery or claimed delivery", episode, err)
	}
	keys := securityPublic(t, "available recovery Founder")
	founder, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Identity: keys.public, Reason: "Fixture root-local recovery"})
	if err != nil {
		t.Fatal(err)
	}
	founder, _ = securityActivate(t, f.s, founder, keys, f.founder.UID)
	sweepHealth(t, f.s)
	sweepHealth(t, f.s)
	if messages := healthMessages(t, f.s, worker.AgentID, founder.AgentID); len(messages) != 2 {
		t.Fatal("pending alert/recovery history was lost or duplicated when authority returned", messages)
	}
}

func TestHealthServerSessionTransitionDoesNotAdviseRecoveringLiveSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "session-fence", h.ObservedAt
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	f.now = f.now.Add(time.Duration(sessionCleanupAlertGrace) * time.Second)
	sweepHealth(t, f.s)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture session transition"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "Fixture session transition", ExpiresAt: f.now.Unix() + 300})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"}); err != nil {
		t.Fatal(err)
	}
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Reason != "session-active" || strings.Contains(d.NextAction, "reconcile") || strings.Contains(d.Incident.NextAction, "reconcile") {
		t.Fatal("current session or old incident advised live-session recovery", d)
	}
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	messages := healthMessages(t, f.s, f.worker.ID, f.founder.ID)
	if d.Incident.State != "resolved" || len(messages) != 2 {
		t.Fatal("authoritative session transition required a new worker report to be observed", d, messages)
	}
	for _, m := range messages {
		if strings.Contains(m.Body, "observed recovery or transition") && (!strings.Contains(m.Body, "Reason: session-active") || strings.Contains(m.Body, "Next authorized action: An authorized session party must reconcile")) {
			t.Fatal("recovery message advised recovering a live session", m)
		}
	}
}

func TestHealthPauseAndMissingTelemetryDoNotFabricateRecovery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "invocation-uncertain", h.ObservedAt
	h.JournalID = "journal_uncertain_fixture"
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	original := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident.ID
	paused, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Fixture explicit pause while retaining uncertainty"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	h.ObservedAt++
	h.Sequence++
	h.Mode, h.Phase, h.Blocker = "manual", "paused", "founder-paused"
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "paused" || d.Incident.ID != original || d.Incident.State != "active" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
		t.Fatal("intentional pause fabricated blocker recovery", d)
	}
	f.now = f.now.Add(91 * time.Second)
	if _, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	f.now = f.now.Add(91 * time.Second)
	if _, err := f.s.Heartbeat(ctx, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "unknown" || d.Incident.ID != original || d.Incident.State != "active" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
		t.Fatal("missing telemetry fabricated recovery", d)
	}
	if _, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: paused.Revision}, Mode: "ai", Reason: "Fixture explicit resume after reconciliation"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	h = observedHealth(f.now.Unix())
	h.Sequence = 3
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d = diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.State != "healthy" || d.Incident.ID != original || d.Incident.State != "resolved" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 2 {
		t.Fatal("fresh actual clearance failed to resolve retained incident", d)
	}
}

func TestHealthFreshProblemDoesNotResolveStillReportedFence(t *testing.T) {
	f := newFixture(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "invocation-uncertain", h.ObservedAt
	h.JournalID = "journal_uncertain_fixture"
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	original := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID).Incident.ID
	f.now = f.now.Add(time.Second)
	h.ObservedAt++
	h.Problems = []domain.HealthProblem{{Component: "watch", Code: domain.Unavailable, FirstSeenAt: h.ObservedAt, LastSeenAt: h.ObservedAt}}
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Reason != "watch-error" || d.Incident.ID != original || d.Incident.State != "active" || len(healthMessages(t, f.s, f.worker.ID, f.founder.ID)) != 1 {
		t.Fatal("fresh secondary problem hid or falsely resolved the retained invocation fence", d)
	}
}

func TestHealthStaleBusinessProgressIgnoresStepAndHeartbeatClocks(t *testing.T) {
	f := newFixture(t)
	h := observedHealth(f.now.Unix())
	h.ActionKind, h.ActionID, h.Phase = "onboarding", "onboarding", "preparing"
	h.PhaseStartedAt, h.LastProgressAt = h.ObservedAt-400, h.ObservedAt-400
	h.StepStartedAt, h.StepFinishedAt = h.ObservedAt-1, h.ObservedAt
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	d := diagnoseHealth(t, f.s, f.founder.ID, f.worker.ID)
	if d.Reason != "work-progress-stale" || d.Stale || d.Incident == nil {
		t.Fatal("routine step/heartbeat clocks were treated as business progress", d)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.s.SweepHealth(cancelled); err == nil {
		t.Fatal("sweep ignored cancellation")
	}
}
