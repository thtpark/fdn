package work

import (
	"context"
	"path/filepath"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

type ModeControl struct {
	AgentID     string `json:"agent_id"`
	Mode        string `json:"mode"`
	Revision    int64  `json:"revision"`
	RequestedBy string `json:"requested_by,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func ReconcileMode(ctx context.Context, state *local.State, c config.Worker, path string, agent domain.Agent) (config.Worker, error) {
	modeLock, err := secure.Acquire(filepath.Join(state.Root, "mode-control.lock"))
	if err != nil {
		return c, err
	}
	defer modeLock.Close()
	var previous ModeControl
	found, err := state.Get(ctx, "automation-control", &previous)
	if err != nil {
		return c, err
	}
	if found && (previous.AgentID != agent.ID || previous.Revision > agent.Revision) {
		return c, domain.Fail(domain.Conflict, "mode control observation is stale or belongs to another identity; reread Founder authority")
	}
	if agent.ID == "" || agent.Revision < 1 || (agent.Mode != "ai" && agent.Mode != "manual") {
		return c, domain.Fail(domain.Prerequisite, "Foundation must support authoritative Founder mode control before this worker starts")
	}
	if !agent.Founder && agent.Mode == "manual" && agent.ModeRequestedBy == "" {
		return c, domain.Fail(domain.Denied, "non-Founder manual mode requires an explicit authenticated Founder request")
	}
	apply := func(current config.Worker) (config.Worker, error) {
		if current.Mode == "manual" && agent.Mode == "ai" && agent.ModeRequestedBy == "" {
			return current, domain.Fail(domain.Prerequisite, "legacy local manual pause needs an explicit Founder resume decision; it is not silently reset")
		}
		current.Mode = agent.Mode
		return current, nil
	}
	if c.Mode != agent.Mode {
		if path != "" {
			lock, err := secure.Acquire(path + ".lock")
			if err != nil {
				return c, err
			}
			defer lock.Close()
			current, err := config.LoadWorker(path)
			if err != nil {
				return c, err
			}
			current, err = apply(current)
			if err != nil {
				return c, err
			}
			if err := config.Write(path, current); err != nil {
				return c, err
			}
			c = current
		} else {
			var err error
			c, err = apply(c)
			if err != nil {
				return c, err
			}
		}
	}
	control := ModeControl{AgentID: agent.ID, Mode: agent.Mode, Revision: agent.Revision, RequestedBy: agent.ModeRequestedBy, Reason: agent.ModeReason}
	if !found || previous != control {
		if err := state.Put(ctx, "automation-control", control); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (e *Engine) applyMode(ctx context.Context, c config.Worker, agent domain.Agent) (config.Worker, error) {
	e.configMu.Lock()
	defer e.configMu.Unlock()
	c, err := ReconcileMode(ctx, e.State, c, e.ConfigPath, agent)
	if err != nil {
		return c, err
	}
	if e.ConfigPath == "" {
		e.Config = c
	}
	return c, nil
}
