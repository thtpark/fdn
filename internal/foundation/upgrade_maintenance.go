package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func maintenanceKey(id string) string { return "upgrade-maintenance:" + id }

func readMaintenance(ctx context.Context, q queryer, id string) (domain.UpgradeMaintenance, error) {
	var result domain.UpgradeMaintenance
	var raw string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", maintenanceKey(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, domain.Fail(domain.NotFound, "upgrade maintenance pause is not recorded")
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}

func saveMaintenance(ctx context.Context, tx *sql.Tx, value domain.UpgradeMaintenance) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", maintenanceKey(value.ID), string(raw))
	return err
}

func (s *Store) PauseUpgradeMaintenance(ctx context.Context, actor string, req domain.UpgradeMaintenanceRequest) (domain.UpgradeMaintenance, error) {
	return mutate(ctx, s, actor, "upgrade.maintenance", req.Command, req, func(tx *sql.Tx, founder domain.Agent) (domain.UpgradeMaintenance, error) {
		var result domain.UpgradeMaintenance
		if !founder.Founder {
			return result, domain.Fail(domain.Denied, "only Founder may record a temporary upgrade pause")
		}
		if !domain.ValidOperation(req.ID) || len(req.Proposals) == 0 || len(req.Proposals) > 32 {
			return result, domain.Fail(domain.Invalid, "temporary upgrade pauses require an exact ID and 1-32 proposals")
		}
		if _, err := readMaintenance(ctx, tx, req.ID); err == nil {
			return result, domain.Fail(domain.Conflict, "temporary maintenance ID is already recorded; replay the original operation, never replace its mode baseline")
		} else if domain.AsFault(err).Code != domain.NotFound {
			return result, err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, founder)
		if err != nil {
			return result, err
		}
		if !active {
			return result, domain.Fail(domain.Denied, "temporary upgrade pause requires the current Founder identity")
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return result, err
		}
		all := false
		targets := map[string]bool{founder.ID: true}
		seen := make(map[string]bool)
		for i, proposal := range req.Proposals {
			challenge := proposal.Challenge
			if err := upgrade.ValidateProposal(proposal); err != nil {
				return result, err
			}
			if challenge.InstanceID != instance || challenge.ID != req.ID+"."+strconv.Itoa(i) || challenge.ExpiresAt <= s.timestamp() || challenge.ExpiresAt > s.timestamp()+900 ||
				seen[challenge.TargetID] || proposal.Release.Version != req.Proposals[0].Release.Version || proposal.Release.Commit != req.Proposals[0].Release.Commit {
				return result, domain.Fail(domain.Invalid, "temporary pause must name distinct exact current-source upgrade proposals")
			}
			seen[challenge.TargetID] = true
			if challenge.Role == "foundation" {
				if challenge.TargetID != instance || challenge.Generation != 1 || i != 0 {
					return result, domain.Fail(domain.Denied, "Foundation maintenance pause belongs to another instance")
				}
				all = true
			} else {
				target, err := activeIdentitySubject(ctx, tx, challenge.TargetID)
				if err != nil {
					return result, err
				}
				targetGeneration, targetActive, err := agentOperationIdentity(ctx, tx, target)
				if err != nil {
					return result, err
				}
				if !targetActive || targetGeneration != challenge.Generation {
					return result, domain.Fail(domain.Denied, "temporary upgrade target generation changed")
				}
				targets[target.ID] = true
			}
		}
		result = domain.UpgradeMaintenance{ID: req.ID, FounderID: founder.ID, FounderGeneration: generation,
			State: "paused", Proposals: slices.Clone(req.Proposals), PausedModes: []domain.UpgradePausedMode{},
			PermitHashes: make([]string, len(req.Proposals)), MessageIDs: make([]string, len(req.Proposals))}
		rows, err := tx.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 ORDER BY id")
		if err != nil {
			return result, err
		}
		agents, err := scanJSON[domain.Agent](rows)
		if err != nil {
			return result, err
		}
		for _, target := range agents {
			if (!all && !targets[target.ID]) || target.Mode == "manual" {
				continue
			}
			if target.Mode != "ai" {
				return result, domain.Fail(domain.Conflict, "temporary maintenance cannot adopt an unknown automation mode")
			}
			targetGeneration, targetActive, err := agentOperationIdentity(ctx, tx, target)
			if err != nil {
				return result, err
			}
			if !targetActive {
				return result, domain.Fail(domain.Denied, "temporary pause cannot adopt an inactive target identity")
			}
			prior := target.Mode
			target, err = s.setAgentMode(ctx, tx, target, founder.ID, "manual",
				"Temporary Founder-authorized upgrade "+req.ID+"; prior AI intent will resume only after exact verified maintenance")
			if err != nil {
				return result, err
			}
			result.PausedModes = append(result.PausedModes, domain.UpgradePausedMode{AgentID: target.ID,
				Generation: targetGeneration, Revision: target.Revision, PriorMode: prior})
		}
		return result, saveMaintenance(ctx, tx, result)
	})
}

func (s *Store) bindMaintenancePermit(ctx context.Context, tx *sql.Tx, founder domain.Agent, id string, permit domain.UpgradePermit) error {
	if id == "" {
		return nil
	}
	record, err := readMaintenance(ctx, tx, id)
	if err != nil {
		return err
	}
	if record.FounderID != founder.ID || record.FounderGeneration != permit.ActorGeneration || record.State != "paused" {
		return domain.Fail(domain.Denied, "native authorization does not belong to this current temporary maintenance pause")
	}
	index := slices.IndexFunc(record.Proposals, func(p domain.UpgradeProposal) bool { return reflect.DeepEqual(p, permit.Proposal) })
	if index < 0 || record.PermitHashes[index] != "" {
		return domain.Fail(domain.Conflict, "temporary maintenance proposal is changed or already authorized")
	}
	hash := domain.Hash(permit.Signature)
	record.PermitHashes[index] = hash
	if err := saveMaintenance(ctx, tx, record); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?)", "upgrade-maintenance-permit:"+hash, id)
	return err
}

func (s *Store) maintenanceMessage(ctx context.Context, tx *sql.Tx, message domain.Message) error {
	if message.Request == nil || message.Request.Action.Upgrade == nil {
		return nil
	}
	hash := domain.Hash(message.Request.Action.Upgrade.Signature)
	var id string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "upgrade-maintenance-permit:"+hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	record, err := readMaintenance(ctx, tx, id)
	if err != nil {
		return err
	}
	index := slices.Index(record.PermitHashes, hash)
	if index < 0 || record.State != "paused" || record.FounderID != message.SenderID || record.FounderGeneration != message.Request.SenderGeneration ||
		!reflect.DeepEqual(record.Proposals[index], message.Request.Action.Upgrade.Proposal) {
		return domain.Fail(domain.Denied, "maintenance request does not match its recorded issuer/proposal")
	}
	if slices.Contains(record.MessageIDs[:index], "") || !slices.Equal(message.Request.Action.Dependencies, record.MessageIDs[:index]) {
		return domain.Fail(domain.Denied, "temporary maintenance requests must preserve the exact recorded server-first rollout dependencies")
	}
	if record.MessageIDs[index] != "" && record.MessageIDs[index] != message.ID {
		return domain.Fail(domain.Conflict, "temporary maintenance already has a different exact request")
	}
	record.MessageIDs[index] = message.ID
	return saveMaintenance(ctx, tx, record)
}

func (s *Store) finishMaintenance(ctx context.Context, tx *sql.Tx, record domain.UpgradeMaintenance, trigger domain.Message, state, reason string) error {
	record.State, record.Reason = state, reason
	if err := saveMaintenance(ctx, tx, record); err != nil {
		return err
	}
	notice := domain.Message{ID: domain.NewID("message"), SenderID: trigger.RecipientID, RecipientID: record.FounderID,
		Body:   "Temporary upgrade " + record.ID + " " + state + ": " + reason,
		Origin: "service", Kind: "request-update", State: "pending", Revision: 1, CreatedAt: s.timestamp(),
		ThreadID: trigger.ThreadID, ReplyToID: trigger.ID}
	_, err := insertMessage(ctx, tx, notice)
	return err
}

func (s *Store) completeMaintenance(ctx context.Context, tx *sql.Tx, message domain.Message) error {
	if message.Request == nil || message.Request.Action.Upgrade == nil ||
		!slices.Contains([]string{"completed", "failed", "declined", "deferred"}, message.Request.State) {
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "upgrade-maintenance-permit:"+domain.Hash(message.Request.Action.Upgrade.Signature)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	record, err := readMaintenance(ctx, tx, id)
	if err != nil || record.State != "paused" {
		return err
	}
	if message.Request.State != "completed" {
		return s.finishMaintenance(ctx, tx, record, message, "blocked",
			"An exact native request was "+message.Request.State+". No modes were restored; inspect the retained request/native evidence and obtain explicit Founder reconciliation.")
	}
	for i, requestID := range record.MessageIDs {
		if requestID == "" {
			return nil
		}
		current, err := get[domain.Message](ctx, tx, "messages", requestID)
		if err != nil {
			return err
		}
		if current.Request == nil || current.Request.State != "completed" {
			return nil
		}
		permit := current.Request.Action.Upgrade
		if permit == nil || current.SenderID != record.FounderID || current.Request.SenderGeneration != record.FounderGeneration ||
			domain.Hash(permit.Signature) != record.PermitHashes[i] || !reflect.DeepEqual(permit.Proposal, record.Proposals[i]) ||
			!slices.Equal(current.Request.Action.Dependencies, record.MessageIDs[:i]) {
			return s.finishMaintenance(ctx, tx, record, message, "blocked", "A maintenance result no longer matches its exact recorded issuer, source or rollout dependencies. No modes were restored.")
		}
		if current.Request.UpgradeResult == nil {
			return s.finishMaintenance(ctx, tx, record, message, "blocked", "A completed reply has no typed verified native source/backup result. A text acknowledgement is not maintenance verification; no modes were restored.")
		}
		if err := upgrade.ValidateResult(*permit, *current.Request.UpgradeResult); err != nil {
			return s.finishMaintenance(ctx, tx, record, message, "blocked", "A maintenance result does not bind the exact verified native source and encrypted backup. No modes were restored.")
		}
		if !current.Request.UpgradeResult.ResumeSafe {
			return s.finishMaintenance(ctx, tx, record, message, "blocked", "Native verification retained an independent session safety fence. The installation result remains completed, but no modes were restored; reconcile the exact fence as Founder.")
		}
	}
	founder, err := get[domain.Agent](ctx, tx, "agents", record.FounderID)
	if err != nil {
		return err
	}
	generation, active, err := agentOperationIdentity(ctx, tx, founder)
	if err != nil {
		return err
	}
	if !active || !founder.Founder || generation != record.FounderGeneration {
		return s.finishMaintenance(ctx, tx, record, message, "blocked", "Temporary maintenance issuer authority changed; no modes were restored.")
	}
	candidates := []domain.Agent{}
	for _, paused := range record.PausedModes {
		target, err := get[domain.Agent](ctx, tx, "agents", paused.AgentID)
		if err != nil {
			return err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, target)
		if err != nil {
			return err
		}
		if !active || generation != paused.Generation {
			return s.finishMaintenance(ctx, tx, record, message, "blocked", "A temporarily paused identity became inactive or changed key generation. No modes were restored.")
		}
		if paused.PriorMode != "ai" || target.Mode != "manual" || target.Revision != paused.Revision || target.ModeRequestedBy != record.FounderID {
			continue
		}
		if err := upgradeQuiescent(ctx, tx, domain.UpgradeChallenge{Role: "agent", TargetID: target.ID}, s.timestamp()); err != nil {
			if domain.AsFault(err).Code == domain.Prerequisite {
				return s.finishMaintenance(ctx, tx, record, message, "blocked", "An independent work or assistance admission still blocks maintenance resumption. Verified installation reports were preserved; no modes were restored.")
			}
			return err
		}
		candidates = append(candidates, target)
	}
	rows, err := tx.QueryContext(ctx, "SELECT value FROM settings WHERE key LIKE 'upgrade-maintenance:%' AND key<>? AND json_extract(value,'$.state')='paused'", maintenanceKey(record.ID))
	if err != nil {
		return err
	}
	pending, err := scanJSON[domain.UpgradeMaintenance](rows)
	if err != nil {
		return err
	}
	for _, other := range pending {
		for _, proposal := range other.Proposals {
			if len(candidates) != 0 && (proposal.Challenge.Role == "foundation" ||
				slices.ContainsFunc(candidates, func(a domain.Agent) bool { return a.ID == proposal.Challenge.TargetID }) ||
				slices.ContainsFunc(other.PausedModes, func(p domain.UpgradePausedMode) bool {
					return slices.ContainsFunc(candidates, func(a domain.Agent) bool { return a.ID == p.AgentID })
				})) {
				return s.finishMaintenance(ctx, tx, record, message, "blocked", "Another exact temporary maintenance batch still covers a paused computer. No modes were restored; resolve the outstanding maintenance as Founder.")
			}
		}
	}
	for _, target := range candidates {
		if _, err := s.setAgentMode(ctx, tx, target, record.FounderID, "ai",
			"Verified temporary upgrade "+record.ID+" completed; restoring prior Founder-authorized AI intent"); err != nil {
			return err
		}
	}
	return s.finishMaintenance(ctx, tx, record, message, "completed",
		"All exact native source/backup results were verified. Prior AI intent was restored for "+strconv.Itoa(len(candidates))+
			" unchanged temporary pauses; newer Founder mode requests and preexisting manual intent were preserved. Other work, approval and retry fences remain enforced.")
}
