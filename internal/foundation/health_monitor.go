package foundation

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type healthEpisode struct {
	Incident           domain.HealthIncident `json:"incident"`
	Fingerprint        string                `json:"fingerprint"`
	Cause              string                `json:"cause"`
	ActionKey          string                `json:"action_key"`
	Generation         int64                 `json:"generation"`
	TaskID             string                `json:"task_id,omitempty"`
	Sequence           int64                 `json:"sequence"`
	ReportObservedAt   int64                 `json:"report_observed_at"`
	ObservationAt      int64                 `json:"observation_at"`
	RecoveryObservedAt int64                 `json:"recovery_observed_at,omitempty"`
	RecoveryReason     string                `json:"recovery_reason,omitempty"`
	RecoveryNextAction string                `json:"recovery_next_action,omitempty"`
	DeliveryState      string                `json:"delivery_state"`
}

const sessionCleanupAlertGrace int64 = 30

type sessionCleanupWait struct {
	Generation int64 `json:"generation"`
	Since      int64 `json:"since"`
}

func (s *Store) deferSessionCleanupAlert(ctx context.Context, tx *sql.Tx, id string, assessment *healthAssessment, episode healthEpisode, now int64) error {
	key := "health-session-cleanup:" + id
	if !assessment.alert || assessment.diagnostics.Reason != "session-fence" {
		if assessment.fresh && assessment.diagnostics.Reason != "session-fence" &&
			assessment.diagnostics.State != "unknown" && assessment.diagnostics.State != "offline" {
			_, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key=?", key)
			return err
		}
		return nil
	}
	if episode.Incident.State == "active" && episode.Incident.Kind == "session-fence" && episode.Generation == assessment.generation {
		return nil
	}
	wait, err := healthSetting[sessionCleanupWait](ctx, tx, key)
	if err != nil {
		return err
	}
	if wait.Generation != assessment.generation || wait.Since == 0 {
		wait = sessionCleanupWait{Generation: assessment.generation, Since: now}
		if err := saveHealthSetting(ctx, tx, key, wait); err != nil {
			return err
		}
	}
	assessment.alert = now-wait.Since >= sessionCleanupAlertGrace
	return nil
}

// SweepHealth observes deterministic server state without invoking workers or AI.
// Each bounded page and per-agent transaction can be cancelled independently.
func (s *Store) SweepHealth(ctx context.Context) error {
	last := ""
	for {
		rows, err := s.db.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 AND id>? ORDER BY id LIMIT 128", last)
		if err != nil {
			return err
		}
		agents, err := scanJSON[domain.Agent](rows)
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if err := s.sweepAgentHealth(ctx, agent.ID); err != nil {
				return err
			}
			last = agent.ID
		}
		if len(agents) < 128 {
			return nil
		}
	}
}

func healthConditionPresent(episode healthEpisode, assessment healthAssessment) bool {
	if episode.Generation != assessment.generation {
		return false
	}
	d := assessment.diagnostics
	if slices.Contains([]string{"unknown", "offline", "paused"}, d.State) || d.Reason == "founder-paused" {
		return true
	}
	h := d.Health
	if h == nil || episode.ActionKey != assessment.actionKey {
		return false
	}
	if h.Blocker == episode.Incident.Kind && !(h.Blocker == "session-fence" && d.Reason == "session-active") {
		return true
	}
	for _, problem := range h.Problems {
		if problem.ResolvedAt == 0 && episode.Cause == problem.Component+"-error:"+string(problem.Code) {
			return true
		}
	}
	return episode.Incident.Kind == "phase-deadline-exceeded" && healthWorking(h) && h.DeadlineAt != 0 && domain.HealthDeadlineRemaining(*h, d.ReceivedAt, d.ObservedAt) < -domain.HealthDeadlineGraceSeconds
}

func healthTransitionObserved(episode healthEpisode, assessment healthAssessment) bool {
	h := assessment.diagnostics.Health
	return !healthConditionPresent(episode, assessment) && assessment.fresh && h != nil && assessment.diagnostics.State != "unknown" && assessment.diagnostics.State != "paused" && assessment.diagnostics.Reason != "founder-paused" &&
		(assessment.generation != episode.Generation || h.Sequence > episode.Sequence || h.ObservedAt > episode.ReportObservedAt ||
			(assessment.authoritative && assessment.diagnostics.Reason == "session-active" && episode.Incident.Kind == "session-fence"))
}

func resolveHealthEpisode(episode *healthEpisode, assessment healthAssessment, now int64) {
	episode.Incident.State, episode.Incident.ResolvedAt, episode.RecoveryObservedAt = "resolved", now, now
	episode.RecoveryReason, episode.RecoveryNextAction = assessment.diagnostics.Reason, assessment.diagnostics.NextAction
}

func (s *Store) sweepAgentHealth(ctx context.Context, id string) error {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	agent, err := get[domain.Agent](ctx, tx, "agents", id)
	if err != nil {
		return err
	}
	_, active, err := healthIdentity(ctx, tx, agent)
	if err != nil || !active {
		return err
	}
	now := s.timestamp()
	report, err := healthSetting[healthReport](ctx, tx, healthReportKey+id)
	if err != nil {
		return err
	}
	if err := s.observeAgentHealthStatus(ctx, tx, agent, report, now); err != nil {
		return err
	}
	assessment, err := s.assessHealth(ctx, tx, agent, now)
	if err != nil {
		return err
	}
	if agent.Founder && !assessment.reported {
		return s.commit(ctx, tx)
	}
	episode, err := healthSetting[healthEpisode](ctx, tx, healthIncidentKey+id)
	if err != nil {
		return err
	}
	if err := s.deferSessionCleanupAlert(ctx, tx, id, &assessment, episode, now); err != nil {
		return err
	}
	fingerprint := domain.Hash(fmt.Sprintf("%d:%s:%s", assessment.generation, assessment.cause, assessment.actionKey))
	replace := assessment.alert && (episode.Incident.ID == "" || episode.Fingerprint != fingerprint || episode.Incident.State != "active")
	if episode.Incident.State == "active" && healthConditionPresent(episode, assessment) {
		replace = false
	}
	if replace {
		if episode.Incident.ID != "" && episode.Incident.State == "active" {
			if healthTransitionObserved(episode, assessment) {
				resolveHealthEpisode(&episode, assessment, now)
			}
			if err := s.deliverHealthEpisode(ctx, tx, agent, &episode); err != nil {
				return err
			}
		}
		observed := int64(0)
		if assessment.diagnostics.Health != nil {
			observed = assessment.diagnostics.Health.ObservedAt
		}
		episode = healthEpisode{Generation: assessment.generation, Fingerprint: fingerprint, Cause: assessment.cause, ActionKey: assessment.actionKey, TaskID: assessment.taskID,
			Sequence: assessment.sequence, ReportObservedAt: observed, ObservationAt: now, DeliveryState: "pending",
			Incident: domain.HealthIncident{ID: domain.NewID("incident"), Kind: assessment.diagnostics.Reason, State: "active", Since: assessment.since,
				Reason: assessment.diagnostics.Reason, NextAction: assessment.diagnostics.NextAction}}
	} else if !assessment.alert && episode.Incident.State == "active" && healthTransitionObserved(episode, assessment) {
		resolveHealthEpisode(&episode, assessment, now)
	}
	if episode.Incident.ID != "" {
		if episode.Incident.State == "active" && episode.Fingerprint == fingerprint {
			episode.TaskID = assessment.taskID
		}
		if err := s.deliverHealthEpisode(ctx, tx, agent, &episode); err != nil {
			return err
		}
		if err := saveHealthSetting(ctx, tx, healthIncidentKey+id, episode); err != nil {
			return err
		}
	}
	return s.commit(ctx, tx)
}

func (s *Store) healthRecipients(ctx context.Context, q queryer, agent domain.Agent, taskID string) ([]string, bool, error) {
	var founder string
	err := q.QueryRowContext(ctx, `SELECT id FROM agents WHERE active=1 AND json_extract(data,'$.founder')=1 AND coalesce(json_extract(data,'$.banned'),0)=0 ORDER BY id LIMIT 1`).Scan(&founder)
	if err != nil && err != sql.ErrNoRows {
		return nil, false, err
	}
	recipients := []string{}
	if founder != "" {
		a, err := get[domain.Agent](ctx, q, "agents", founder)
		if err != nil {
			return nil, false, err
		}
		_, active, err := healthIdentity(ctx, q, a)
		if err != nil {
			return nil, false, err
		}
		if active {
			recipients = append(recipients, founder)
		} else {
			founder = ""
		}
	}
	if taskID != "" {
		task, relevant, err := healthTaskRelevant(ctx, q, agent, taskID, true)
		if err != nil {
			return nil, false, err
		}
		if !relevant {
			var mentions int
			if err := q.QueryRowContext(ctx, "SELECT count(*) FROM attention WHERE agent_id=? AND task_id=? AND state='pending'", agent.ID, taskID).Scan(&mentions); err != nil {
				return nil, false, err
			}
			relevant = mentions != 0 && task.ArchivedAt == 0
		}
		if relevant {
			project, err := get[domain.Project](ctx, q, "projects", task.ProjectID)
			if err != nil {
				return nil, false, err
			}
			if !project.Archived {
				for _, id := range project.Delegates {
					if slices.Contains(recipients, id) {
						continue
					}
					delegate, err := activeIdentitySubject(ctx, q, id)
					if err != nil {
						if domain.AsFault(err).Code == domain.Denied || domain.AsFault(err).Code == domain.NotFound {
							continue
						}
						return nil, false, err
					}
					_, active, err := healthIdentity(ctx, q, delegate)
					if err != nil {
						return nil, false, err
					}
					if active {
						recipients = append(recipients, id)
					}
				}
			}
		}
	}
	slices.Sort(recipients)
	return recipients, founder != "", nil
}

func (s *Store) deliverHealthEpisode(ctx context.Context, tx *sql.Tx, agent domain.Agent, episode *healthEpisode) error {
	recipients, founder, err := s.healthRecipients(ctx, tx, agent, episode.TaskID)
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		for _, state := range []string{"active", "resolved"} {
			if state == "resolved" && episode.Incident.State != "resolved" {
				continue
			}
			id := "message_health_" + domain.Hash(episode.Incident.ID+":"+state+":"+recipient)
			var existing string
			err := tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE id=?", id).Scan(&existing)
			if err == nil {
				continue
			}
			if err != sql.ErrNoRows {
				return err
			}
			observed := episode.ObservationAt
			label := "active incident"
			reason, next := episode.Incident.Reason, episode.Incident.NextAction
			if state == "resolved" {
				observed, label = episode.RecoveryObservedAt, "observed recovery or transition"
				reason, next = episode.RecoveryReason, episode.RecoveryNextAction
			}
			body := fmt.Sprintf("Foundation server health observation for %s: %s (%s). Reason: %s. Next authorized action: %s. Since %s; observed %s. This is a server-origin observation, not a verified AI reply or permission to change work, mode, sign-in or recovery.",
				agent.ID, label, episode.Incident.ID, reason, next,
				time.Unix(episode.Incident.Since, 0).UTC().Format(time.RFC3339), time.Unix(observed, 0).UTC().Format(time.RFC3339))
			if episode.TaskID != "" {
				body += " Current task scope: " + episode.TaskID + "."
			}
			if state == "resolved" {
				body += " The earlier cause is no longer the current observed condition; retained fences and results still require their normal authority."
			}
			message := domain.Message{ID: id, SenderID: agent.ID, RecipientID: recipient, Body: body,
				Origin: "service", Kind: "health", Urgent: true, ResponseRequest: false, State: "pending", CreatedAt: observed, Revision: 1}
			if _, err := insertMessage(ctx, tx, message); err != nil {
				return err
			}
		}
	}
	episode.DeliveryState = "pending"
	if len(recipients) != 0 {
		episode.DeliveryState = "pending-founder"
		if founder {
			episode.DeliveryState = "delivered"
		}
	}
	return nil
}
