package foundation

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (s *Store) UpgradeContext(ctx context.Context, actor string) (domain.UpgradeContext, error) {
	var result domain.UpgradeContext
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Close()
	a, err := s.actor(ctx, tx, actor)
	if err != nil {
		return result, err
	}
	if !a.Founder {
		return result, domain.Fail(domain.Denied, "only authenticated Founder may inspect software upgrades")
	}
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='instance_id'").Scan(&result.InstanceID); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM agents ORDER BY id")
	if err != nil {
		return result, err
	}
	result.Agents, err = scanJSON[domain.Agent](rows)
	result.Version, result.Commit = domain.Version, domain.Commit
	return result, err
}

func (s *Store) AuthorizeUpgrade(ctx context.Context, actor string, req domain.UpgradeAuthorizationRequest, sign func(domain.UpgradePermit) (string, error)) (domain.UpgradePermit, error) {
	return mutate(ctx, s, actor, "upgrade.authorize", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.UpgradePermit, error) {
		var permit domain.UpgradePermit
		if !a.Founder || a.Mode != "manual" {
			return permit, domain.Fail(domain.Denied, "only an active Founder in manual mode may authorize native upgrades")
		}

		if err := upgrade.ValidateProposal(req.Proposal); err != nil {
			return permit, err
		}
		challenge := req.Proposal.Challenge
		now := s.timestamp()
		if challenge.ExpiresAt <= now || challenge.ExpiresAt > now+900 {
			return permit, domain.Fail(domain.Conflict, "native upgrade challenge expired or has an invalid lifetime; prepare a new plan")
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return permit, err
		}
		if challenge.InstanceID != instance {
			return permit, domain.Fail(domain.Denied, "native updater belongs to another Foundation instance")
		}
		generation, active, err := agentOperationIdentity(ctx, tx, a)
		if err != nil {
			return permit, err
		}
		if !active || generation <= 0 {
			return permit, domain.Fail(domain.Denied, "upgrade requires a current enrolled Founder key generation")
		}
		if challenge.Role == "agent" {
			target, err := activeIdentitySubject(ctx, tx, challenge.TargetID)
			if err != nil {
				return permit, err
			}
			targetGeneration, targetActive, err := agentOperationIdentity(ctx, tx, target)
			if err != nil {
				return permit, err
			}
			if target.Mode != "manual" || !targetActive || targetGeneration != challenge.Generation {
				return permit, domain.Fail(domain.Conflict, "native upgrade requires the current paused target identity and exact key generation")
			}
		} else {
			if challenge.TargetID != instance || challenge.Generation != 1 {
				return permit, domain.Fail(domain.Denied, "Foundation upgrade target must name this exact service instance")
			}
		}
		if err := upgradeQuiescent(ctx, tx, challenge, now); err != nil {
			return permit, err
		}
		permit = domain.UpgradePermit{Proposal: req.Proposal, ActorID: a.ID, ActorGeneration: generation,
			IssuedAt: now, ExpiresAt: now + 900}
		permit.Signature, err = sign(permit)
		if err != nil {
			return permit, err
		}
		data, err := json.Marshal(permit)
		if err != nil {
			return permit, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?)",
			"upgrade-permit:"+domain.Hash(permit.Signature), string(data))
		return permit, err
	})
}

func (s *Store) LeaseUpgrade(ctx context.Context, actor string, request domain.UpgradeLeaseRequest, maintenance bool, sign func(domain.UpgradeLease) (string, error)) (domain.UpgradeLease, error) {
	var lease domain.UpgradeLease
	if !domain.ValidHash(request.PermitSHA256) || len(request.Nonce) != 43 {
		return lease, domain.Fail(domain.Invalid, "native upgrade lease requires the exact authorization fingerprint and a fresh native nonce")
	}
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return lease, err
	}
	defer tx.Close()
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "upgrade-permit:"+request.PermitSHA256).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return lease, domain.Fail(domain.NotFound, "native upgrade authorization not found")
		}
		return lease, err
	}
	var permit domain.UpgradePermit
	if err := json.Unmarshal([]byte(raw), &permit); err != nil {
		return lease, err
	}
	challenge := permit.Proposal.Challenge
	now := s.timestamp()
	if permit.ExpiresAt <= now || challenge.ExpiresAt <= now ||
		(!maintenance && actor != permit.ActorID && actor != challenge.TargetID) ||
		(maintenance && challenge.Role != "foundation") {
		return lease, domain.Fail(domain.Denied, "native upgrade lease is expired or requested outside its exact current parties")
	}
	founder, err := s.actor(ctx, tx, permit.ActorID)
	if err != nil {
		return lease, err
	}
	generation, active, err := agentOperationIdentity(ctx, tx, founder)
	if err != nil {
		return lease, err
	}
	if !active || !founder.Founder || founder.Mode != "manual" || generation != permit.ActorGeneration {
		return lease, domain.Fail(domain.Denied, "native upgrade issuer is no longer the current paused Founder identity")
	}
	if challenge.Role == "agent" {
		target, err := s.actor(ctx, tx, challenge.TargetID)
		if err != nil {
			return lease, err
		}
		generation, active, err := agentOperationIdentity(ctx, tx, target)
		if err != nil {
			return lease, err
		}
		if !active || target.Mode != "manual" || generation != challenge.Generation {
			return lease, domain.Fail(domain.Denied, "native upgrade target identity/generation/pause changed before start")
		}
	}
	if err := upgradeQuiescent(ctx, tx, challenge, now); err != nil {
		return lease, err
	}
	lease = domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce, IssuedAt: now, ExpiresAt: now + 30}
	lease.Signature, err = sign(lease)
	return lease, err
}

func upgradeQuiescent(ctx context.Context, tx queryer, challenge domain.UpgradeChallenge, now int64) error {
	query := "SELECT count(*) FROM tasks WHERE json_extract(data,'$.active_run') IS NOT NULL"
	arguments := []any{}
	if challenge.Role == "agent" {
		query += " AND json_extract(data,'$.active_run.agent_id')=?"
		arguments = append(arguments, challenge.TargetID)
	} else {
		var unpaused int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE active=1 AND COALESCE(json_extract(data,'$.mode'),'')!='manual'").Scan(&unpaused); err != nil {
			return err
		}
		if unpaused != 0 {
			return domain.Fail(domain.Prerequisite, "Founder must pause every active agent before a Foundation server upgrade")
		}
	}
	var occupied int
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(&occupied); err != nil {
		return err
	}
	if occupied != 0 {
		return domain.Fail(domain.Prerequisite, "active task ownership must stop and reconcile before a native upgrade")
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE state IN ('consented','active') AND expires_at>?", now).Scan(&occupied); err != nil {
		return err
	}
	if occupied != 0 {
		return domain.Fail(domain.Prerequisite, "active assistance sessions must close before a native upgrade")
	}
	return nil
}
