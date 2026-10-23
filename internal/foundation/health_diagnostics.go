package foundation

import (
	"context"
	"database/sql"
	"slices"
	"sort"

	"github.com/thtpark/fdn/internal/domain"
)

type healthAssessment struct {
	diagnostics   domain.AgentDiagnostics
	generation    int64
	sequence      int64
	taskID        string
	actionKey     string
	cause         string
	since         int64
	alert         bool
	fresh         bool
	reported      bool
	authoritative bool
}

func healthSince(report healthReport, eventAt int64) int64 {
	if eventAt == 0 || report.Health == nil {
		return report.ReceivedAt
	}
	return max(1, report.ReceivedAt-max(0, report.Health.ObservedAt-eventAt))
}

func pendingHealthReplies(ctx context.Context, q queryer, agent string) (int, int64, error) {
	var count int
	var oldest sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT count(*), min(json_extract(data,'$.created_at')) FROM messages
WHERE recipient_id=? AND state='pending' AND json_extract(data,'$.kind')='direct'
AND json_extract(data,'$.response_requested')=1`, agent).Scan(&count, &oldest)
	return count, oldest.Int64, err
}

func healthScopedTask(ctx context.Context, q queryer, a domain.Agent, h *domain.WorkerHealth) (string, error) {
	if h != nil && h.ActionKind != "message" {
		id := h.TaskID
		if h.ActionKind == "task" {
			id = h.ActionID
		}
		if h.ActionKind == "attention" {
			relevant, err := healthActionRelevant(ctx, q, a, h.ActionKind, h.ActionID, true)
			if err != nil {
				return "", err
			}
			if relevant {
				entry, err := get[domain.Entry](ctx, q, "entries", h.ActionID)
				if err != nil {
					return "", err
				}
				task, err := get[domain.Task](ctx, q, "tasks", entry.TaskID)
				if err != nil {
					return "", err
				}
				if task.ArchivedAt == 0 {
					return task.ID, nil
				}
			}
		}
		if id != "" {
			_, relevant, err := healthTaskRelevant(ctx, q, a, id, true)
			if err != nil || relevant {
				return id, err
			}
		}
	}
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM tasks WHERE archived=0 AND json_extract(data,'$.active_run.agent_id')=? ORDER BY id LIMIT 1`, a.ID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Store) healthSessionActive(ctx context.Context, q queryer, target string, now int64) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT data FROM sessions WHERE target_id=? AND state IN ('consented','active') AND expires_at>? AND json_extract(data,'$.open_requested')=1 ORDER BY id`, target, now)
	if err != nil {
		return false, err
	}
	sessions, err := scanJSON[domain.Session](rows)
	if err != nil {
		return false, err
	}
	for _, session := range sessions {
		if err := s.sessionAuthority(ctx, q, session); err == nil {
			return true, nil
		} else if domain.AsFault(err).Code == domain.Internal {
			return false, err
		}
	}
	return false, nil
}

func healthBlockerAction(blocker string) (string, bool) {
	switch blocker {
	case "founder-paused":
		return "Only Founder may explicitly resume automation; sign-in does not clear a pause.", false
	case "session-active":
		return "Wait for the authorized session to finish; do not open another work slot.", false
	case "session-fence":
		return "An authorized session party must reconcile or close the retained session fence.", true
	case "invocation-uncertain", "unreconciled-claim":
		return "Inspect retained work evidence and use explicit authorized recovery; do not retry inference or clear ownership automatically.", true
	case "authentication-required":
		return "Founder may arrange an authorized provider sign-in handoff; no sign-in or refresh was started.", true
	case "authentication-unknown", "provider-unavailable", "provider-model-unavailable", "provider-permission":
		return "Inspect provider readiness and the approved configuration; do not infer authentication or launch a model.", true
	case "identity-unavailable", "transport-unavailable":
		return "Inspect the identity proxy and transport through an authorized operator; do not rotate keys automatically.", true
	case "approval-required", "work-prerequisite":
		return "The current authorized task actors must satisfy the work prerequisite; no approval or retry is implied.", false
	case "retry-wait":
		return "Wait for the existing retry policy and current authority; do not reset retry budgets.", false
	case "slot-occupied":
		return "Wait for the existing serial work slot to finish or be explicitly reconciled.", false
	case "workspace-permission":
		return "An authorized operator must inspect the granted workspace permissions; no path or permission change is implied.", true
	case "maintenance":
		return "An authorized operator must finish maintenance without erasing retained identity or work state.", true
	case "report-pending":
		return "Restore report transport and replay the exact retained report; do not repeat inference.", true
	default:
		return "Inspect safe local service diagnostics and configuration without erasing retained work or changing authority.", true
	}
}

func healthWorking(h *domain.WorkerHealth) bool {
	return h != nil && slices.Contains([]string{"checking", "reconciling", "provider-check", "preparing", "invoking", "applying", "reporting"}, h.Phase) && (h.ActionKind != "" || slices.Contains([]string{"invoking", "applying", "reporting"}, h.Phase))
}

func (s *Store) assessHealth(ctx context.Context, q queryer, a domain.Agent, now int64) (healthAssessment, error) {
	assessment := healthAssessment{diagnostics: domain.AgentDiagnostics{AgentID: a.ID, Name: a.Name, Mode: a.Mode, ObservedAt: now}}
	d := &assessment.diagnostics
	set := func(state, reason, next string, alert bool, since int64) (healthAssessment, error) {
		d.State, d.Reason, d.NextAction = state, reason, next
		assessment.alert, assessment.since, assessment.cause = alert, max(1, since), reason
		return assessment, nil
	}
	generation, active, err := healthIdentity(ctx, q, a)
	if err != nil {
		return assessment, err
	}
	assessment.generation = generation
	if !active {
		d.Stale = true
		return set("inactive", "identity-inactive", "Explicit identity authority is required; diagnostics grant no recovery or work permission.", false, now)
	}
	report, err := healthSetting[healthReport](ctx, q, healthReportKey+a.ID)
	if err != nil {
		return assessment, err
	}
	if report.Generation != generation {
		report = healthReport{Generation: generation}
	}
	assessment.reported = report.Health != nil
	if generation == 0 || report.HeartbeatAt != 0 {
		d.LastSeen = a.LastSeen
	}
	if report.Health != nil {
		assessment.sequence = report.Health.Sequence
	}
	d.ReceivedAt = report.ReceivedAt
	if report.UnobservedAt == 0 && report.Health != nil {
		h := *report.Health
		h.Problems = append([]domain.HealthProblem{}, h.Problems...)
		// Recheck scope against current assignments, not an earlier observation.
		relevant, err := healthActionRelevant(ctx, q, a, h.ActionKind, h.ActionID, true)
		if err != nil {
			return assessment, err
		}
		if !relevant {
			h.ActionID, h.TaskID, h.JournalID = "", "", ""
		}
		d.Health = &h
	}
	assessment.taskID, err = healthScopedTask(ctx, q, a, d.Health)
	if err != nil {
		return assessment, err
	}
	assessment.actionKey = report.ActionKey
	d.PendingReplies, d.OldestReplyAt, err = pendingHealthReplies(ctx, q, a.ID)
	if err != nil {
		return assessment, err
	}
	baseline := d.LastSeen
	if baseline == 0 {
		baseline = a.CreatedAt
	}
	if d.LastSeen == 0 && generation > 1 {
		baseline = max(a.CreatedAt, a.LastSeen)
	}
	if now-baseline > domain.HealthHeartbeatSeconds {
		d.Stale = true
		return set("offline", "heartbeat-missing", "An authorized operator should inspect the target identity proxy, worker service and transport; no work recovery was performed.", true, baseline+domain.HealthHeartbeatSeconds)
	}
	if d.Health == nil {
		d.Stale = true
		since := report.UnobservedAt
		if since == 0 {
			since = a.CreatedAt
		}
		return set("unknown", "health-unobserved", "The worker must publish a current bounded health report; heartbeat alone does not establish healthy automation.", now-since > domain.HealthReportSeconds, since)
	}
	if now-report.ReceivedAt > domain.HealthReportSeconds {
		d.Stale = true
		return set("unknown", "health-stale", "Inspect the worker health publisher; a live heartbeat does not refresh work observations.", true, report.ReceivedAt+domain.HealthReportSeconds)
	}
	assessment.fresh = true
	h := d.Health
	problems := append([]domain.HealthProblem{}, h.Problems...)
	sort.Slice(problems, func(i, j int) bool { return problems[i].Component < problems[j].Component })
	for _, problem := range problems {
		if problem.ResolvedAt != 0 {
			continue
		}
		// The controller fault also records the same step's concrete safety blocker.
		if problem.Component == "controller" && h.Blocker != "" && h.Blocker != "controller-error" && h.Blocker != "work-prerequisite" {
			continue
		}
		result, _ := set("blocked", problem.Component+"-error", "Inspect the safe "+problem.Component+" diagnostic code "+string(problem.Code)+" locally; no model, retry or recovery was started.", true, healthSince(report, problem.FirstSeenAt))
		result.cause += ":" + string(problem.Code)
		return result, nil
	}
	if a.Mode == "manual" {
		next, _ := healthBlockerAction("founder-paused")
		return set("paused", "founder-paused", next, false, a.ModeChangedAt)
	}
	activeSession, err := s.healthSessionActive(ctx, q, a.ID, now)
	if err != nil {
		return assessment, err
	}
	if activeSession && slices.Contains([]string{"", "session-fence", "session-active"}, h.Blocker) {
		assessment.authoritative = true
		next, _ := healthBlockerAction("session-active")
		return set("blocked", "session-active", next, false, healthSince(report, h.PhaseStartedAt))
	}
	if h.Blocker != "" {
		blocker := h.Blocker
		if blocker == "session-active" {
			blocker = "session-fence"
		}
		next, alert := healthBlockerAction(blocker)
		return set("blocked", blocker, next, alert, healthSince(report, h.BlockedSinceAt))
	}
	if h.Phase == "session" {
		next, alert := healthBlockerAction("session-fence")
		return set("blocked", "session-fence", next, alert, healthSince(report, h.PhaseStartedAt))
	}
	if h.Authentication == "login-required" || h.Authentication == "sign-in-required" {
		next, alert := healthBlockerAction("authentication-required")
		return set("blocked", "authentication-required", next, alert, healthSince(report, h.AuthenticationObservedAt))
	}
	if healthWorking(h) {
		if h.DeadlineAt != 0 {
			remaining := domain.HealthDeadlineRemaining(*h, report.ReceivedAt, now)
			if remaining < -domain.HealthDeadlineGraceSeconds {
				return set("blocked", "phase-deadline-exceeded", "Inspect retained invocation and report evidence, then use explicit authorized reconciliation; do not rerun uncertain work.", true, max(1, now+remaining))
			}
			return set("working", "within-phase-deadline", "Wait for the bounded current phase; queued replies do not authorize concurrent inference.", false, healthSince(report, h.PhaseStartedAt))
		}
		progressAt := max(h.LastProgressAt, h.PhaseStartedAt)
		if domain.HealthAge(h.ObservedAt, progressAt, report.ReceivedAt, now) > domain.HealthReportSeconds {
			return set("blocked", "work-progress-stale", "Inspect retained work and controller evidence; heartbeats and idle polls are not business progress.", true, healthSince(report, progressAt))
		}
		return set("working", "work-observed", "Wait for current work or its next bounded health observation.", false, healthSince(report, h.PhaseStartedAt))
	}
	if h.Phase == "unknown" || h.Phase == "blocked" || h.Phase == "paused" {
		return set("unknown", "worker-state-unknown", "Inspect the worker's safe local diagnostics; authentication alone does not establish automation readiness.", domain.HealthAge(h.ObservedAt, h.PhaseStartedAt, report.ReceivedAt, now) > domain.HealthReportSeconds, healthSince(report, h.PhaseStartedAt))
	}
	if d.PendingReplies != 0 {
		if now-d.OldestReplyAt > domain.HealthReplyWaitSeconds {
			var messageID string
			if err := q.QueryRowContext(ctx, `SELECT id FROM messages WHERE recipient_id=? AND state='pending'
AND json_extract(data,'$.kind')='direct' AND json_extract(data,'$.response_requested')=1 ORDER BY json_extract(data,'$.created_at'),id LIMIT 1`, a.ID).Scan(&messageID); err != nil {
				return assessment, err
			}
			assessment.actionKey, assessment.taskID = "message:"+messageID, ""
			return set("blocked", "reply-overdue", "The recipient should inspect reply scheduling and retained reports; only authorized actors may intervene, and no reply was verified.", true, d.OldestReplyAt)
		}
		return set("queued", "reply-queued", "Wait for the recipient's serial controller to schedule the requested reply.", false, d.OldestReplyAt)
	}
	return set("healthy", "idle-observed", "No intervention is indicated by the current observation; no task result or AI reply is verified.", false, report.ReceivedAt)
}

func (s *Store) AgentDiagnostics(ctx context.Context, actor, target string) (domain.AgentDiagnostics, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentDiagnostics{}, err
	}
	defer tx.Rollback()
	viewer, err := s.actor(ctx, tx, actor)
	if err != nil {
		return domain.AgentDiagnostics{}, err
	}
	if viewer.ID != target && !viewer.Founder {
		return domain.AgentDiagnostics{}, domain.Fail(domain.Denied, "agent diagnostics are private to the target and active Founder")
	}
	agent, err := get[domain.Agent](ctx, tx, "agents", target)
	if err != nil {
		return domain.AgentDiagnostics{}, err
	}
	assessment, err := s.assessHealth(ctx, tx, agent, s.timestamp())
	if err != nil {
		return domain.AgentDiagnostics{}, err
	}
	d := assessment.diagnostics
	incident, err := healthSetting[healthEpisode](ctx, tx, healthIncidentKey+target)
	if err != nil {
		return d, err
	}
	if incident.Incident.ID != "" && incident.Generation == assessment.generation {
		d.Incident = &incident.Incident
		if assessment.authoritative || incident.Incident.State == "resolved" {
			d.Incident.NextAction = d.NextAction
		}
	}
	if d.Health != nil && actor != target {
		d.Health.JournalID = ""
		for _, action := range []struct {
			kind string
			id   *string
		}{{d.Health.ActionKind, &d.Health.ActionID}, {d.Health.LastActionKind, &d.Health.LastActionID}} {
			if action.kind == "message" && *action.id != "" {
				m, err := get[domain.Message](ctx, tx, "messages", *action.id)
				if err != nil {
					return d, err
				}
				if m.SenderID != actor && m.RecipientID != actor {
					*action.id = ""
				}
			} else if action.kind == "onboarding" || action.kind == "research" {
				*action.id = ""
			}
		}
	}
	return d, nil
}

func (s *Store) MessageDiagnostics(ctx context.Context, actor, id string) (domain.MessageDiagnostics, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MessageDiagnostics{}, err
	}
	defer tx.Rollback()
	if _, err := s.actor(ctx, tx, actor); err != nil {
		return domain.MessageDiagnostics{}, err
	}
	m, err := get[domain.Message](ctx, tx, "messages", id)
	if err != nil {
		return domain.MessageDiagnostics{}, err
	}
	if m.SenderID != actor && m.RecipientID != actor {
		return domain.MessageDiagnostics{}, domain.Fail(domain.Denied, "message diagnostics are private to the message parties")
	}
	now := s.timestamp()
	d := domain.MessageDiagnostics{MessageID: m.ID, RecipientID: m.RecipientID, ResponseRequested: m.ResponseRequest, ObservedAt: now}
	set := func(state, stage, reason, next string) (domain.MessageDiagnostics, error) {
		d.State, d.Stage, d.Reason, d.NextAction = state, stage, reason, next
		return d, nil
	}
	if !m.ResponseRequest || m.Kind != "direct" {
		return set("not-requested", "receipt", "no-automatic-response-requested", "No automatic AI reply is requested by this message.")
	}
	if m.State == "handled" {
		return set("handled", "receipt", "handled-receipt-only", "A handled receipt is not proof of a verified AI reply; inspect any reply as a message party.")
	}
	if m.State != "pending" {
		return set(m.State, "receipt", "recipient-disposition", "Only the recipient may change an eligible message disposition; no AI reply is verified.")
	}
	a, err := get[domain.Agent](ctx, tx, "agents", m.RecipientID)
	if err != nil {
		return d, err
	}
	assessment, err := s.assessHealth(ctx, tx, a, now)
	if err != nil {
		return d, err
	}
	health := assessment.diagnostics
	d.HealthReceivedAt, d.Stale = health.ReceivedAt, health.Stale
	if slices.Contains([]string{"offline", "inactive", "unknown", "paused"}, health.State) {
		return set("blocked", "recipient", health.Reason, "Wait for a current recipient observation or ask the responsible authority; this grants no access to the recipient's other work.")
	}
	if health.State == "blocked" && health.Reason != "reply-overdue" {
		return set("blocked", "recipient", health.Reason, "The recipient has a safe reported prerequisite or fence; only its authorized actors may intervene.")
	}
	h := health.Health
	if assessment.fresh && h != nil && h.ActionKind == "message" && h.ActionID == m.ID {
		if h.Phase == "applying" || h.Phase == "reporting" || h.PendingReports != 0 {
			return set("awaiting-delivery", "reporting", "reply-report-pending", "Wait for exact retained report delivery; generation or a receipt is not a verified reply.")
		}
		if healthWorking(h) {
			return set("generating", h.Phase, "reply-work-observed", "Wait for the current bounded reply phase; no completed reply is yet verified.")
		}
	}
	if assessment.fresh && h != nil && h.LastActionKind == "message" && h.LastActionID == m.ID && h.PendingReports != 0 {
		return set("awaiting-delivery", "reporting", "reply-report-pending", "Wait for exact retained report delivery; do not repeat inference.")
	}
	if health.State == "working" {
		return set("queued", "scheduling", "recipient-busy", "The recipient has known active work; wait for its serial slot without inspecting unrelated private work.")
	}
	if now-m.CreatedAt > domain.HealthReplyWaitSeconds {
		return set("blocked", "scheduling", "reply-overdue", "Ask the recipient or responsible authority to inspect reply scheduling; no reply or recovery action is implied.")
	}
	return set("queued", "scheduling", "reply-queued", "Wait for the recipient's serial controller to schedule this reply.")
}
