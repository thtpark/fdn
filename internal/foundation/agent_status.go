package foundation

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

const agentStatusKey = "agent.status:"

// Only the latest publication and coalesced candidate are retained here. Private
// scoping keys and unpublished notes never enter Agent.data or roster responses.
type agentStatusRecord struct {
	Version             int                     `json:"version"`
	Generation          int64                   `json:"generation"`
	Published           *domain.AgentTextStatus `json:"published,omitempty"`
	PublishedContext    string                  `json:"published_context,omitempty"`
	Automatic           domain.ActivitySummary  `json:"automatic"`
	AutomaticObservedAt int64                   `json:"automatic_observed_at"`
	Note                *agentStatusNote        `json:"note,omitempty"`
	Pending             *agentStatusCandidate   `json:"pending,omitempty"`
	HealthHash          string                  `json:"health_hash,omitempty"`
	HealthReceivedAt    int64                   `json:"health_received_at,omitempty"`
	HealthMissing       bool                    `json:"health_missing,omitempty"`
	SessionActive       bool                    `json:"session_active,omitempty"`
	Mode                string                  `json:"mode,omitempty"`
}

type agentStatusNote struct {
	Text       string `json:"text"`
	ContextKey string `json:"context_key"`
	ObservedAt int64  `json:"observed_at"`
}

type agentStatusCandidate struct {
	Text       string `json:"text"`
	Source     string `json:"source"`
	State      string `json:"state"`
	ObservedAt int64  `json:"observed_at"`
	ContextKey string `json:"context_key"`
}

type agentStatusResult struct {
	Generation int64                     `json:"generation"`
	Receipt    domain.AgentStatusReceipt `json:"receipt"`
}

func loadAgentStatus(ctx context.Context, q queryer, id string, generation int64) (agentStatusRecord, error) {
	r, err := healthSetting[agentStatusRecord](ctx, q, agentStatusKey+id)
	if err != nil {
		return r, err
	}
	if r.Generation != generation || r.Version == 0 {
		return agentStatusRecord{Version: domain.AgentStatusVersion, Generation: generation}, nil
	}
	if r.Version != domain.AgentStatusVersion {
		return r, domain.Fail(domain.Prerequisite, "unsupported saved agent status version")
	}
	return r, nil
}

func agentStatusContext(summary domain.ActivitySummary) string {
	return domain.Hash(summary.ContextKey + ":" + summary.State + ":" + summary.Text)
}

func unobservedAgentActivity() domain.ActivitySummary {
	return domain.ActivitySummary{Text: "Activity not yet observed", State: "unknown", ContextKey: domain.Hash("agent-status-unobserved")}
}

func statusCandidate(r agentStatusRecord, now int64) agentStatusCandidate {
	candidate := agentStatusCandidate{Text: r.Automatic.Text, Source: "controller", State: r.Automatic.State,
		ObservedAt: r.AutomaticObservedAt, ContextKey: agentStatusContext(r.Automatic)}
	if r.Note != nil && r.Note.ContextKey == r.Automatic.ContextKey {
		candidate.Text, candidate.Source = r.Note.Text, "agent"
		candidate.ObservedAt = max(candidate.ObservedAt, r.Note.ObservedAt)
		if (candidate.State == "working" || candidate.State == "idle") && (r.AutomaticObservedAt == 0 || now-r.AutomaticObservedAt > domain.HealthReportSeconds || r.HealthMissing) {
			candidate.State = "unknown"
		}
	}
	return candidate
}

func queueAgentStatus(r *agentStatusRecord, candidate agentStatusCandidate, immediate bool, now int64) {
	if p := r.Published; p != nil {
		if p.Text == candidate.Text && p.Source == candidate.Source && p.State == candidate.State {
			p.ObservedAt = max(p.ObservedAt, candidate.ObservedAt)
			r.PublishedContext, r.Pending = candidate.ContextKey, nil
			return
		}
		if !immediate && now < p.UpdatedAt+domain.AgentStatusInterval {
			r.Pending = &candidate
			return
		}
	}
	r.Published = &domain.AgentTextStatus{Text: candidate.Text, Source: candidate.Source, State: candidate.State,
		UpdatedAt: now, ObservedAt: candidate.ObservedAt}
	r.PublishedContext, r.Pending = candidate.ContextKey, nil
}

func flushAgentStatus(r *agentStatusRecord, now int64) bool {
	if r.Pending == nil || (r.Published != nil && now < r.Published.UpdatedAt+domain.AgentStatusInterval) {
		return false
	}
	queueAgentStatus(r, *r.Pending, false, now)
	return true
}

func publicAgentStatus(r agentStatusRecord, now int64) *domain.AgentTextStatus {
	if r.Published == nil || (r.Published.Source == "agent" && r.PublishedContext != agentStatusContext(r.Automatic)) {
		// Expired authored text is hidden while its replacement is rate-limited.
		return nil
	}
	status := *r.Published
	status.Stale = status.State == "unknown" || status.ObservedAt == 0 || now-status.ObservedAt > domain.HealthReportSeconds ||
		r.HealthMissing || r.PublishedContext != agentStatusContext(r.Automatic)
	return &status
}

func (s *Store) projectAgentStatus(ctx context.Context, q queryer, a domain.Agent, now int64) (domain.Agent, error) {
	a.Status = nil
	generation, active, err := healthIdentity(ctx, q, a)
	if err != nil || !active {
		return a, err
	}
	r, err := loadAgentStatus(ctx, q, a.ID, generation)
	if err != nil {
		return a, err
	}
	a.Status = publicAgentStatus(r, now)
	return a, nil
}

func (s *Store) activityForStatus(ctx context.Context, q queryer, a domain.Agent, h domain.WorkerHealth, now int64) (domain.ActivitySummary, bool, error) {
	h.Mode = a.Mode
	active := false
	if a.Mode != "manual" && (h.Blocker == "" || h.Blocker == "session-fence" || h.Blocker == "session-active") {
		var err error
		active, err = s.healthSessionActive(ctx, q, a.ID, now)
		if err != nil {
			return domain.ActivitySummary{}, false, err
		}
		if active {
			h.Phase, h.Activity, h.Blocker = "session", "session", "session-active"
		} else if h.Phase == "session" || h.Blocker == "session-active" {
			h.Blocker = "session-fence"
		}
	}
	return domain.SummarizeActivity(h), active, nil
}

// A repeated snapshot proves transport liveness only. Cached reports are
// re-rendered solely for fresh, authoritative assistance-session transitions.
func (s *Store) observeAgentHealthStatus(ctx context.Context, tx *sql.Tx, a domain.Agent, report healthReport, now int64) error {
	generation, active, err := healthIdentity(ctx, tx, a)
	if err != nil || !active {
		return err
	}
	r, err := loadAgentStatus(ctx, tx, a.ID, generation)
	if err != nil {
		return err
	}
	before, err := encode(r)
	if err != nil {
		return err
	}
	if report.Generation == generation && report.Health != nil && report.UnobservedAt == 0 {
		fresh := r.HealthHash != report.SnapshotHash || r.HealthReceivedAt != report.ReceivedAt
		summary, sessionActive, err := s.activityForStatus(ctx, tx, a, *report.Health, now)
		if err != nil {
			return err
		}
		authoritative := sessionActive != r.SessionActive && now-report.ReceivedAt <= domain.HealthReportSeconds
		if fresh || authoritative {
			previous := r.Automatic
			if summary.Text == "" {
				summary = previous
			}
			if summary.Text == "" {
				summary = unobservedAgentActivity()
			}
			if agentStatusContext(previous) != agentStatusContext(summary) {
				r.Note = nil
			}
			r.Automatic, r.HealthMissing, r.SessionActive, r.Mode = summary, false, sessionActive, a.Mode
			r.AutomaticObservedAt = report.ReceivedAt
			r.HealthHash, r.HealthReceivedAt = report.SnapshotHash, report.ReceivedAt
			queueAgentStatus(&r, statusCandidate(r, now), domain.ImportantStatusChange(previous, summary), now)
		}
	} else if r.Published != nil {
		r.HealthMissing = true
	}
	flushAgentStatus(&r, now)
	after, err := encode(r)
	if err != nil {
		return err
	}
	if string(before) == string(after) {
		return nil
	}
	return saveHealthSetting(ctx, tx, agentStatusKey+a.ID, r)
}

func (s *Store) observeAgentStatusMode(ctx context.Context, tx *sql.Tx, a domain.Agent, now int64) error {
	generation, active, err := healthIdentity(ctx, tx, a)
	if err != nil || !active {
		return err
	}
	r, err := loadAgentStatus(ctx, tx, a.ID, generation)
	if err != nil {
		return err
	}
	previous := r.Automatic
	if a.Mode == "manual" {
		r.Automatic = domain.SummarizeActivity(domain.WorkerHealth{Mode: a.Mode, Phase: "paused", Activity: "paused"})
	} else if r.Mode == "manual" || r.Automatic.State == "paused" {
		r.Automatic = domain.ActivitySummary{Text: "Automation enabled; awaiting activity", State: "unknown", ContextKey: domain.Hash("agent-status-resume-intent")}
	} else {
		return nil
	}
	if agentStatusContext(previous) != agentStatusContext(r.Automatic) {
		r.Note = nil
	}
	r.Mode, r.AutomaticObservedAt, r.HealthMissing = a.Mode, now, false
	queueAgentStatus(&r, statusCandidate(r, now), domain.ImportantStatusChange(previous, r.Automatic), now)
	return saveHealthSetting(ctx, tx, agentStatusKey+a.ID, r)
}

func (s *Store) PublishAgentStatus(ctx context.Context, actor, target string, req domain.AgentStatusRequest) (domain.AgentStatusReceipt, error) {
	result, err := mutate(ctx, s, actor, "agent.status:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (agentStatusResult, error) {
		if a.ID != target {
			return agentStatusResult{}, domain.Fail(domain.Denied, "only the authenticated agent may publish its own public status")
		}
		if err := domain.ValidateAgentStatusRequest(req); err != nil {
			return agentStatusResult{}, err
		}
		generation, active, err := healthIdentity(ctx, tx, a)
		if err != nil {
			return agentStatusResult{}, err
		}
		if !active {
			return agentStatusResult{}, domain.Fail(domain.Denied, "status publisher identity is not active")
		}
		r, err := loadAgentStatus(ctx, tx, a.ID, generation)
		if err != nil {
			return agentStatusResult{}, err
		}
		now := s.timestamp()
		if r.Automatic.Text == "" {
			r.Automatic = unobservedAgentActivity()
		}
		if a.Mode == "manual" && r.Automatic.State != "paused" {
			r.Automatic = domain.SummarizeActivity(domain.WorkerHealth{Mode: a.Mode, Phase: "paused", Activity: "paused"})
			r.AutomaticObservedAt = now
		}
		text, err := domain.NormalizeAgentStatusText(*req.Text)
		if err != nil {
			return agentStatusResult{}, err
		}
		r.Note = nil
		if text != "" {
			r.Note = &agentStatusNote{Text: text, ContextKey: r.Automatic.ContextKey, ObservedAt: now}
		}
		r.Mode = a.Mode
		queueAgentStatus(&r, statusCandidate(r, now), false, now)
		if err := saveHealthSetting(ctx, tx, agentStatusKey+a.ID, r); err != nil {
			return agentStatusResult{}, err
		}
		receipt := domain.AgentStatusReceipt{AgentID: a.ID, Status: publicAgentStatus(r, now), Pending: r.Pending != nil}
		if receipt.Pending {
			receipt.NextUpdateAt = r.Published.UpdatedAt + domain.AgentStatusInterval
		}
		return agentStatusResult{Generation: generation, Receipt: receipt}, nil
	})
	return result.Receipt, err
}

func replayAgentStatus(ctx context.Context, tx *sql.Tx, actor string, ref storedResult) (agentStatusResult, error) {
	var result agentStatusResult
	if err := json.Unmarshal(ref.Value, &result); err != nil {
		return result, err
	}
	if ref.ID != actor || result.Receipt.AgentID != actor {
		return agentStatusResult{}, domain.Fail(domain.Denied, "agent status receipt belongs to a different actor")
	}
	a, err := activeIdentitySubject(ctx, tx, actor)
	if err != nil {
		return agentStatusResult{}, err
	}
	generation, active, err := healthIdentity(ctx, tx, a)
	if err != nil {
		return agentStatusResult{}, err
	}
	if !active || generation != result.Generation {
		return agentStatusResult{}, domain.Fail(domain.Conflict, "agent status receipt belongs to a retired identity generation")
	}
	return result, nil
}
