package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

const healthReportKey = "health.report:"
const healthIncidentKey = "health.incident:"

type healthReport struct {
	Generation   int64                `json:"generation"`
	HeartbeatAt  int64                `json:"heartbeat_at"`
	ReceivedAt   int64                `json:"received_at"`
	UnobservedAt int64                `json:"unobserved_at,omitempty"`
	SnapshotHash string               `json:"snapshot_hash,omitempty"`
	ActionKey    string               `json:"action_key,omitempty"`
	Health       *domain.WorkerHealth `json:"health,omitempty"`
}

func healthSetting[T any](ctx context.Context, q queryer, key string) (T, error) {
	var value T
	var data string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal([]byte(data), &value)
	return value, err
}

func saveHealthSetting(ctx context.Context, tx *sql.Tx, key string, value any) error {
	data, err := encode(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(data))
	return err
}

func healthIdentity(ctx context.Context, q queryer, a domain.Agent) (int64, bool, error) {
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", a.ID)
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return 0, a.Active && !a.Banned, nil
		}
		return 0, false, err
	}
	if a.Founder {
		return agentOperationIdentity(ctx, q, a)
	}
	return identity.Generation, a.Active && !a.Banned && identity.State == "active", nil
}

// HeartbeatWithHealth records observations under the authenticated identity fence.
// It deliberately grants no work, retry, mode or recovery authority.
func (s *Store) HeartbeatWithHealth(ctx context.Context, actor string, health *domain.WorkerHealth) (domain.Agent, error) {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return domain.Agent{}, err
	}
	defer finish()
	a, err := s.actor(ctx, tx, actor)
	if err != nil {
		return a, err
	}
	generation, active, err := healthIdentity(ctx, tx, a)
	if err != nil {
		return a, err
	}
	if !active {
		return a, domain.Fail(domain.Denied, "health reporter identity is not active")
	}
	report, err := healthSetting[healthReport](ctx, tx, healthReportKey+actor)
	if err != nil {
		return a, err
	}
	if report.Generation != generation {
		report = healthReport{Generation: generation}
	}
	now := s.timestamp()
	if health != nil {
		if err := domain.ValidateWorkerHealth(*health); err != nil {
			return a, err
		}
		copy := *health
		copy.Problems = append([]domain.HealthProblem{}, health.Problems...)
		input, err := encode(copy)
		if err != nil {
			return a, err
		}
		snapshotHash := domain.Hash(string(input))
		if report.Health != nil {
			if health.Sequence == report.Health.Sequence && health.ObservedAt == report.Health.ObservedAt && snapshotHash == report.SnapshotHash {
				// A retry proves liveness, not a fresh work observation or business progress.
				return s.commitHealthHeartbeat(ctx, tx, a, report, now)
			}
			if health.Sequence < report.Health.Sequence || health.ObservedAt < report.Health.ObservedAt || (health.Sequence == report.Health.Sequence && health.ObservedAt == report.Health.ObservedAt) {
				a, err := s.commitHealthHeartbeat(ctx, tx, a, report, now)
				if err != nil {
					return a, err
				}
				return a, domain.Fail(domain.Conflict, "health report sequence or observation time is out of order")
			}
		}
		action := health.ActionKind + ":" + health.ActionID
		if health.ActionID == "" && health.JournalID != "" {
			action = "journal:" + health.JournalID
		}
		if err := sanitizeHealthReferences(ctx, tx, a, &copy); err != nil {
			return a, err
		}
		report.SnapshotHash, report.ActionKey = snapshotHash, domain.Hash(action)
		report.Health, report.ReceivedAt, report.UnobservedAt = &copy, now, 0
	} else if report.UnobservedAt == 0 {
		report.UnobservedAt = now
	}
	return s.commitHealthHeartbeat(ctx, tx, a, report, now)
}

func (s *Store) commitHealthHeartbeat(ctx context.Context, tx *sql.Tx, a domain.Agent, report healthReport, now int64) (domain.Agent, error) {
	report.HeartbeatAt, a.LastSeen = now, now
	if err := saveHealthSetting(ctx, tx, healthReportKey+a.ID, report); err != nil {
		return a, err
	}
	if err := saveIdentityAgent(ctx, tx, a); err != nil {
		return a, err
	}
	if err := s.observeAgentHealthStatus(ctx, tx, a, report, now); err != nil {
		return a, err
	}
	a, err := s.projectAgentStatus(ctx, tx, a, now)
	if err != nil {
		return a, err
	}
	return a, s.commit(ctx, tx)
}

func healthTaskRelevant(ctx context.Context, q queryer, a domain.Agent, id string, current bool) (domain.Task, bool, error) {
	task, err := get[domain.Task](ctx, q, "tasks", id)
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return task, false, nil
		}
		return task, false, err
	}
	if current && (task.ArchivedAt != 0 || task.Status.Terminal()) {
		return task, false, nil
	}
	if task.AssigneeID == a.ID || (task.ActiveRun != nil && task.ActiveRun.AgentID == a.ID) {
		return task, true, nil
	}
	project, err := get[domain.Project](ctx, q, "projects", task.ProjectID)
	if err != nil {
		return task, false, err
	}
	return task, domain.EligibleApprover(a, project, task, true) && (!current || task.Status == domain.Completed), nil
}

func healthActionRelevant(ctx context.Context, q queryer, a domain.Agent, kind, id string, current bool) (bool, error) {
	switch kind {
	case "message":
		m, err := get[domain.Message](ctx, q, "messages", id)
		if err != nil {
			if domain.AsFault(err).Code == domain.NotFound {
				return false, nil
			}
			return false, err
		}
		return m.RecipientID == a.ID && m.Kind == "direct" && m.ResponseRequest, nil
	case "task":
		_, relevant, err := healthTaskRelevant(ctx, q, a, id, current)
		return relevant, err
	case "attention":
		var n int
		err := q.QueryRowContext(ctx, "SELECT count(*) FROM attention WHERE agent_id=? AND entry_id=?", a.ID, id).Scan(&n)
		return n != 0, err
	default:
		return true, nil
	}
}

// A retained local journal can outlive assignment, archival or restored server
// references. Unproved references are redacted, never converted into authority or
// allowed to make a reachable identity appear offline.
func sanitizeHealthReferences(ctx context.Context, q queryer, a domain.Agent, h *domain.WorkerHealth) error {
	for _, action := range []struct {
		kind    string
		id      *string
		current bool
	}{{h.ActionKind, &h.ActionID, true}, {h.LastActionKind, &h.LastActionID, false}} {
		relevant, err := healthActionRelevant(ctx, q, a, action.kind, *action.id, action.current)
		if err != nil {
			return err
		}
		if !relevant {
			*action.id = ""
			if action.current {
				h.TaskID = ""
			}
		}
	}
	if h.TaskID == "" {
		return nil
	}
	if h.ActionKind == "message" {
		h.TaskID = ""
		return nil
	}
	if h.ActionKind == "attention" && h.ActionID != "" {
		entry, err := get[domain.Entry](ctx, q, "entries", h.ActionID)
		if err != nil {
			return err
		}
		if entry.TaskID != h.TaskID {
			h.TaskID = ""
		}
		return nil
	}
	_, relevant, err := healthTaskRelevant(ctx, q, a, h.TaskID, true)
	if err != nil {
		return err
	}
	if !relevant || (h.ActionKind == "task" && h.ActionID != h.TaskID) {
		h.TaskID = ""
	}
	return nil
}
