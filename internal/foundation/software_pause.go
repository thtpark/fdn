package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func readSoftwareMaintenance(ctx context.Context, q queryer, id string) (domain.SoftwareMaintenance, error) {
	var result domain.SoftwareMaintenance
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "software-maintenance:"+id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, domain.Fail(domain.NotFound, "software maintenance batch is not recorded")
		}
		return result, err
	}
	return result, json.Unmarshal([]byte(raw), &result)
}

func saveSoftwareMaintenance(ctx context.Context, tx *sql.Tx, batch domain.SoftwareMaintenance) error {
	raw, err := encode(batch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		"software-maintenance:"+batch.ID, string(raw))
	return err
}

func (s *Store) PauseSoftwareMaintenance(ctx context.Context, actor string, req domain.SoftwareMaintenanceRequest) (domain.SoftwareMaintenance, error) {
	return mutate(ctx, s, actor, "software.maintenance", req.Command, req, func(tx *sql.Tx, founder domain.Agent) (domain.SoftwareMaintenance, error) {
		var zero domain.SoftwareMaintenance
		if !founder.Founder {
			return zero, domain.Fail(domain.Denied, "only Founder may explicitly pause native software rollout targets")
		}
		if !domain.ValidOperation(req.ID) || len(req.PreflightIDs) == 0 || len(req.PreflightIDs) > 32 {
			return zero, domain.Fail(domain.Invalid, "software rollout requires an exact batch ID and 1-32 native observations")
		}
		if err := domain.CheckText(req.Reason, "software rollout pause reason", 2048, true); err != nil {
			return zero, err
		}
		if _, err := readSoftwareMaintenance(ctx, tx, req.ID); err == nil {
			return zero, domain.Fail(domain.Conflict, "maintenance batch already exists; replay its original proposal rather than replacing intent")
		} else if domain.AsFault(err).Code != domain.NotFound {
			return zero, err
		}
		identity, err := currentSoftwareBinding(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return zero, err
		}
		targets, all := map[string]bool{founder.ID: true}, false
		seen := make(map[string]bool)
		for i, id := range req.PreflightIDs {
			control, err := get[domain.SoftwarePreflight](ctx, tx, "software_preflights", id)
			if err != nil {
				return zero, err
			}
			if err := s.softwareControlCurrent(ctx, tx, control); err != nil {
				return zero, err
			}
			if control.FounderID != actor || control.FounderBinding != identity.BindingID() || control.State != "observed" ||
				control.Result == nil || control.Result.Challenge == nil || seen[control.Request.TargetID] {
				return zero, domain.Fail(domain.Denied, "rollout must bind distinct exact observed targets for this Founder device")
			}
			seen[control.Request.TargetID] = true
			if control.Request.TargetID == instance {
				if i != 0 {
					return zero, domain.Fail(domain.Invalid, "Foundation must precede dependent worker installations")
				}
				all = true
			} else {
				targets[control.Request.TargetID] = true
			}
		}
		batch := domain.SoftwareMaintenance{ID: req.ID, FounderID: actor, FounderBinding: identity.BindingID(), Generation: identity.Generation,
			PreflightIDs: slices.Clone(req.PreflightIDs), DeploymentIDs: make([]string, len(req.PreflightIDs)),
			PausedModes: []domain.UpgradePausedMode{}, State: "paused"}
		rows, err := tx.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 ORDER BY id")
		if err != nil {
			return zero, err
		}
		agents, err := scanJSON[domain.Agent](rows)
		if err != nil {
			return zero, err
		}
		for _, agent := range agents {
			if (!all && !targets[agent.ID]) || agent.Mode == "manual" {
				continue
			}
			generation, active, err := agentOperationIdentity(ctx, tx, agent)
			if err != nil {
				return zero, err
			}
			if !active {
				return zero, domain.Fail(domain.Denied, "rollout cannot pause an unenrolled identity")
			}
			updated, err := s.setAgentMode(ctx, tx, agent, actor, "manual", req.Reason)
			if err != nil {
				return zero, err
			}
			batch.PausedModes = append(batch.PausedModes, domain.UpgradePausedMode{AgentID: agent.ID,
				Generation: generation, Revision: updated.Revision, PriorMode: agent.Mode})
		}
		return batch, saveSoftwareMaintenance(ctx, tx, batch)
	})
}

func (s *Store) bindSoftwareMaintenance(ctx context.Context, tx *sql.Tx, actor domain.Agent, id, controlID, deploymentID string) error {
	if id == "" {
		return nil
	}
	batch, err := readSoftwareMaintenance(ctx, tx, id)
	if err != nil {
		return err
	}
	identity, err := currentSoftwareBinding(ctx, tx, actor.ID)
	if err != nil {
		return err
	}
	index := slices.Index(batch.PreflightIDs, controlID)
	if batch.State != "paused" || batch.FounderID != actor.ID || batch.FounderBinding != identity.BindingID() ||
		batch.Generation != identity.Generation || index < 0 || batch.DeploymentIDs[index] != "" {
		return domain.Fail(domain.Conflict, "software authorization differs from its exact temporary rollout batch")
	}
	for _, earlier := range batch.DeploymentIDs[:index] {
		if earlier == "" {
			return domain.Fail(domain.Prerequisite, "rollout predecessor has not been submitted")
		}
		previous, err := get[domain.SoftwareDeployment](ctx, tx, "software_deployments", earlier)
		if err != nil {
			return err
		}
		if previous.State != "verified" {
			return domain.Fail(domain.Prerequisite, "previous rollout target is not actually verified; later waves remain queued in the plan")
		}
	}
	batch.DeploymentIDs[index] = deploymentID
	return saveSoftwareMaintenance(ctx, tx, batch)
}

func (s *Store) finishSoftwareMaintenance(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return nil
	}
	batch, err := readSoftwareMaintenance(ctx, tx, id)
	if err != nil {
		return err
	}
	if batch.State != "paused" {
		return nil
	}
	for _, deploymentID := range batch.DeploymentIDs {
		if deploymentID == "" {
			return nil
		}
		deployment, err := get[domain.SoftwareDeployment](ctx, tx, "software_deployments", deploymentID)
		if err != nil {
			return err
		}
		if deployment.State == "failed" || deployment.State == "operator-required" {
			batch.State, batch.Reason = "blocked", "A native rollout failed or is uncertain; installation history and prior intent were preserved"
			return saveSoftwareMaintenance(ctx, tx, batch)
		}
		if deployment.State != "verified" {
			return nil
		}
		if deployment.Result == nil || !deployment.Result.ResumeSafe {
			batch.State, batch.Reason = "blocked", "Verified installation retained independent work/session/report fences; no automation was resumed"
			return saveSoftwareMaintenance(ctx, tx, batch)
		}
	}
	founder, err := activeIdentitySubject(ctx, tx, batch.FounderID)
	if err != nil {
		return err
	}
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", batch.FounderBinding)
	if err != nil {
		return err
	}
	if !founder.Founder || identity.Generation != batch.Generation || identity.State != "active" {
		batch.State, batch.Reason = "blocked", "Founder device authority changed; exact native results remain verified without restoring modes"
		return saveSoftwareMaintenance(ctx, tx, batch)
	}
	candidates := []domain.Agent{}
	for _, paused := range batch.PausedModes {
		agent, err := activeIdentitySubject(ctx, tx, paused.AgentID)
		if err != nil {
			return err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, agent)
		if err != nil {
			return err
		}
		if !active || generation != paused.Generation {
			batch.State, batch.Reason = "blocked", "A paused computer generation changed; no modes were restored"
			return saveSoftwareMaintenance(ctx, tx, batch)
		}
		if paused.PriorMode != "ai" || agent.Mode != "manual" || agent.Revision != paused.Revision || agent.ModeRequestedBy != batch.FounderID {
			continue
		}
		if err := upgradeQuiescent(ctx, tx, domain.UpgradeChallenge{Role: "agent", TargetID: agent.ID}, s.timestamp()); err != nil {
			if domain.AsFault(err).Code != domain.Prerequisite {
				return err
			}
			batch.State, batch.Reason = "blocked", "Independent work or assistance still blocks automation; verified installations were preserved"
			return saveSoftwareMaintenance(ctx, tx, batch)
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM software_deployments WHERE target_id=? AND state IN ('queued','staged','operator-required')
AND COALESCE(json_extract(data,'$.maintenance_id'),'')<>?`, agent.ID, batch.ID).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			batch.State, batch.Reason = "blocked", "Another exact native operation still covers this computer"
			return saveSoftwareMaintenance(ctx, tx, batch)
		}
		candidates = append(candidates, agent)
	}
	for _, agent := range candidates {
		if _, err := s.setAgentMode(ctx, tx, agent, founder.ID, "ai", "Restore unchanged prior AI intent after the exact verified internal rollout"); err != nil {
			return err
		}
	}
	batch.State = "verified"
	return saveSoftwareMaintenance(ctx, tx, batch)
}

func (s *Store) SoftwareMaintenance(ctx context.Context, actor, id string) (domain.SoftwareMaintenance, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwareMaintenance{}, err
	}
	batch, err := readSoftwareMaintenance(ctx, s.db, id)
	if err == nil && batch.FounderID != actor {
		err = domain.Fail(domain.Denied, "temporary software mode intent is private to its Founder")
	}
	return batch, err
}
