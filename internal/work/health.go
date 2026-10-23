package work

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sync"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

const workerHealthKey = "worker-health"

var healthComponents = []struct{ key, name string }{
	{"configuration-problem", "configuration"},
	{"mode-control-problem", "mode-control"},
	{"provider-update-problem", "provider-update"},
	{"last-problem", "controller"},
	{"watch-problem", "watch"},
	{"heartbeat-problem", "heartbeat"},
	{"connection-problem", "connection"},
	{"session-problem", "session"},
}

var healthVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$`)
var healthModelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/+-]{0,255}$`)

type workerHealthState struct {
	Health      domain.WorkerHealth `json:"health"`
	StepBlocker string              `json:"step_blocker,omitempty"`
}

// The work slot serializes phase writers; watch diagnostics use separate keys.
var healthProblemMu sync.Mutex

func newWorkerHealth(c config.Worker) domain.WorkerHealth {
	return domain.WorkerHealth{Version: domain.WorkerHealthVersion, Mode: c.Mode, Provider: c.Provider, PreferredProvider: c.Provider,
		Authentication: "unknown", Phase: "unknown", Problems: []domain.HealthProblem{}}
}

func (e *Engine) defaultHealth() domain.WorkerHealth {
	e.configMu.RLock()
	defer e.configMu.RUnlock()
	return newWorkerHealth(e.Config)
}

func (e *Engine) changeHealth(ctx context.Context, change func(*workerHealthState)) error {
	state := workerHealthState{Health: e.defaultHealth()}
	if found, err := e.State.Get(ctx, workerHealthKey, &state); err != nil {
		return err
	} else if found && state.Health.Version != domain.WorkerHealthVersion {
		return domain.Fail(domain.Prerequisite, "unsupported worker health format; use matching software without resetting retained state")
	}
	change(&state)
	state.Health.Version = domain.WorkerHealthVersion
	state.Health.Sequence++
	state.Health.ObservedAt = e.Now().UTC().Unix()
	return e.State.Put(ctx, workerHealthKey, state)
}

func (e *Engine) beginHealth(ctx context.Context, c config.Worker) error {
	return e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		now := e.Now().UTC().Unix()
		h.Mode, h.PreferredProvider, h.Phase, h.PhaseStartedAt = c.Mode, c.Provider, "checking", now
		h.StepStartedAt, h.StepFinishedAt, h.DeadlineAt = now, 0, now+120
		h.ActionKind, h.ActionID, h.TaskID, h.JournalID = "", "", "", ""
		state.StepBlocker = ""
	})
}

func healthAction(launch Launch) (kind, id, taskID string) {
	if launch.Task != nil {
		taskID = launch.Task.ID
	}
	switch {
	case launch.Message != nil:
		return "message", launch.Message.ID, taskID
	case launch.Attention != nil:
		return "attention", launch.Attention.EntryID, taskID
	case launch.Task != nil:
		return "task", taskID, taskID
	case launch.Phase == "onboarding":
		return "onboarding", "", ""
	case launch.Phase == "research":
		return "research", "", ""
	default:
		return "", "", ""
	}
}

func (e *Engine) phaseHealth(ctx context.Context, phase string, launch *Launch, journalID string, timeout int, progress bool) error {
	err := e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		now := e.Now().UTC().Unix()
		h.Phase, h.PhaseStartedAt, h.DeadlineAt = phase, now, 0
		if timeout > 0 {
			h.DeadlineAt = now + int64(timeout)
		}
		if launch != nil {
			h.ActionKind, h.ActionID, h.TaskID = healthAction(*launch)
		}
		if journalID != "" {
			h.JournalID = journalID
		}
		if progress {
			h.LastProgressAt = now
			h.Blocker, h.BlockedSinceAt, state.StepBlocker = "", 0, ""
		}
	})
	if err == nil && progress {
		e.clearProblem(ctx, "last-problem")
	}
	return err
}

func (e *Engine) blockHealth(ctx context.Context, blocker string) error {
	return e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		if h.Blocker != blocker || h.BlockedSinceAt == 0 {
			h.BlockedSinceAt = e.Now().UTC().Unix()
		}
		h.Blocker, state.StepBlocker = blocker, blocker
	})
}

func (e *Engine) providerHealth(ctx context.Context, name, model string, observation provider.Health, checkErr error) error {
	return e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		h.Provider, h.ProviderVersion, h.Model = name, "", ""
		if healthModelPattern.MatchString(model) {
			h.Model = model
		}
		if healthVersionPattern.MatchString(observation.Version) {
			h.ProviderVersion = observation.Version
		}
		h.Authentication, h.AuthenticationObservedAt = "unknown", e.Now().UTC().Unix()
		if checkErr == nil && slices.Contains([]string{"authenticated", "credentials-present-unverified", "login-required", "credentials-rejected", "unknown"}, observation.Authentication) {
			h.Authentication = observation.Authentication
		}
		if checkErr == nil && (h.Authentication == "authenticated" || h.Authentication == "credentials-present-unverified") &&
			(h.Blocker == "authentication-required" || h.Blocker == "authentication-unknown") {
			h.Blocker, h.BlockedSinceAt, state.StepBlocker = "", 0, ""
		}
	})
}

func (e *Engine) finishHealth(ctx context.Context, status Status, stepErr error) error {
	return e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		now := e.Now().UTC().Unix()
		h.Mode, h.StepFinishedAt, h.DeadlineAt = status.Mode, now, 0
		blocker := state.StepBlocker
		switch {
		case stepErr != nil:
			if blocker == "" {
				blocker = "controller-error"
				if domain.AsFault(stepErr).Code == domain.Unavailable {
					blocker = "transport-unavailable"
				} else if domain.AsFault(stepErr).Code == domain.Prerequisite || domain.AsFault(stepErr).Code == domain.Denied || domain.AsFault(stepErr).Code == domain.Conflict {
					blocker = "work-prerequisite"
				}
			}
			h.Phase = "blocked"
		case status.State == "manual" || status.Mode == "manual":
			h.Phase, blocker = "paused", "founder-paused"
		case status.State == "session-pending":
			h.Phase, blocker = "session", "session-active"
		case status.State == "blocked":
			h.Phase = "blocked"
			if blocker == "" {
				blocker = "work-prerequisite"
			}
		default:
			h.Phase, blocker = "idle", ""
		}
		if h.Phase != "blocked" && h.Phase != "session" {
			h.ActionKind, h.ActionID, h.TaskID, h.JournalID = "", "", "", ""
		}
		if h.Blocker != blocker || h.BlockedSinceAt == 0 {
			h.BlockedSinceAt = now
		}
		h.Blocker, state.StepBlocker = blocker, blocker
		if blocker == "" {
			h.BlockedSinceAt = 0
		}
		h.PhaseStartedAt = now
	})
}

func (e *Engine) completedHealth(ctx context.Context) error {
	return e.changeHealth(ctx, func(state *workerHealthState) {
		h := &state.Health
		h.LastProgressAt, h.LastActionAt = e.Now().UTC().Unix(), e.Now().UTC().Unix()
		h.LastActionKind, h.LastActionID = h.ActionKind, h.ActionID
		h.Blocker, h.BlockedSinceAt, state.StepBlocker = "", 0, ""
	})
}

func healthComponent(key string) string {
	for _, component := range healthComponents {
		if component.key == key {
			return component.name
		}
	}
	return ""
}

func (e *Engine) observeProblem(ctx context.Context, key string, cause error) error {
	component := healthComponent(key)
	if component == "" {
		return nil
	}
	healthProblemMu.Lock()
	defer healthProblemMu.Unlock()
	var problem domain.HealthProblem
	if _, err := e.State.Get(ctx, "health-problem:"+component, &problem); err != nil {
		return err
	}
	now := e.Now().UTC().Unix()
	if cause == nil {
		if problem.Component == "" || problem.ResolvedAt != 0 {
			return nil
		}
		problem.ResolvedAt = now
	} else {
		code := domain.AsFault(cause).Code
		if problem.Component == "" || problem.ResolvedAt != 0 || problem.Code != code {
			problem.FirstSeenAt = now
		}
		problem.Component, problem.Code, problem.LastSeenAt, problem.ResolvedAt = component, code, now, 0
	}
	return e.State.Put(ctx, "health-problem:"+component, problem)
}

func (e *Engine) clearProblem(ctx context.Context, key string) {
	if err := e.observeProblem(ctx, key, nil); err != nil {
		e.Log.Error("cannot resolve worker diagnostic", "component", healthComponent(key), "code", domain.AsFault(err).Code)
	}
}

func (e *Engine) health(ctx context.Context) (*domain.WorkerHealth, error) {
	keys := []string{workerHealthKey, "session-fence"}
	for _, component := range healthComponents {
		keys = append(keys, "health-problem:"+component.name)
	}
	snapshot, err := e.State.WorkSnapshot(ctx, keys)
	if err != nil {
		return nil, err
	}
	state := workerHealthState{Health: e.defaultHealth()}
	data, found := snapshot.Values[workerHealthKey]
	if found {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		if state.Health.Version != domain.WorkerHealthVersion {
			return nil, domain.Fail(domain.Prerequisite, "unsupported worker health format; use matching software without resetting retained state")
		}
	}
	h := state.Health
	h.Problems = []domain.HealthProblem{}
	for _, component := range healthComponents {
		if data, ok := snapshot.Values["health-problem:"+component.name]; ok {
			var problem domain.HealthProblem
			if err := json.Unmarshal(data, &problem); err != nil {
				return nil, err
			}
			h.Problems = append(h.Problems, problem)
		}
	}
	h.PendingReports = snapshot.PendingReports
	for _, j := range snapshot.Journals {
		if j.State == "uncertain" || (j.State == "invoking" && (!found || h.JournalID != j.ID || h.Phase != "invoking")) {
			if h.Blocker != "invocation-uncertain" || h.BlockedSinceAt == 0 {
				h.BlockedSinceAt = j.CreatedAt
			}
			h.Phase, h.Blocker, h.JournalID, h.TaskID, h.DeadlineAt = "blocked", "invocation-uncertain", j.ID, j.TaskID, 0
			var launch Launch
			if len(j.Context) > 0 {
				if err := json.Unmarshal(j.Context, &launch); err != nil {
					return nil, err
				}
				h.ActionKind, h.ActionID, h.TaskID = healthAction(launch)
			}
			break
		}
	}
	if data, ok := snapshot.Values["session-fence"]; ok {
		var fence struct {
			Active bool `json:"active"`
		}
		if err := json.Unmarshal(data, &fence); err != nil {
			return nil, err
		}
		if fence.Active {
			h.Phase, h.Blocker, h.DeadlineAt = "blocked", "session-fence", 0
		}
	}
	if !found && len(h.Problems) == 0 && h.Blocker == "" && snapshot.PendingReports == 0 {
		return nil, nil
	}
	h.ObservedAt = e.Now().UTC().Unix()
	return &h, nil
}

func (e *Engine) journalHealth(ctx context.Context, j local.Journal, phase string) error {
	launch := Launch{Phase: j.Kind}
	if len(j.Context) > 0 {
		if err := json.Unmarshal(j.Context, &launch); err != nil {
			return err
		}
	}
	if launch.Task == nil && j.TaskID != "" {
		launch.Task = &domain.Task{ID: j.TaskID}
	}
	return e.phaseHealth(ctx, phase, &launch, j.ID, 120, false)
}
