package foundation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thtpark/fdn/internal/domain"
)

type identityFenceKey struct{}

// WithIdentityFence is for trusted transport metadata, never request JSON or headers.
func WithIdentityFence(ctx context.Context, ci domain.CertificateIdentity) context.Context {
	return context.WithValue(ctx, identityFenceKey{}, ci)
}

func activeIdentitySubject(ctx context.Context, q queryer, id string) (domain.Agent, error) {
	agent, err := get[domain.Agent](ctx, q, "agents", id)
	if err == nil && (!agent.Active || agent.Banned) {
		err = domain.Fail(domain.Denied, "agent is revoked or banned")
	}
	return agent, err
}

func (s *Store) checkActorIdentityFence(ctx context.Context, q queryer, id string) error {
	ci, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity)
	if !ok {
		return nil
	}
	if id != ci.AgentID {
		return domain.Fail(domain.Denied, "certificate actor does not match the requested actor")
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", ci.BindingID())
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return domain.Fail(domain.Denied, "certificate actor has no enrolled identity")
		}
		return err
	}
	agent, err := get[domain.Agent](ctx, q, "agents", ci.AgentID)
	if err != nil {
		return err
	}
	if err := identityAllowed(identity, agent, true); err != nil {
		return err
	}
	if ci.AgentID != identity.AgentID || ci.DeviceID != identity.DeviceID || ci.InstanceID != identity.InstanceID || ci.Generation <= 0 || ci.Generation != identity.Generation {
		return domain.Fail(domain.Denied, "certificate actor key generation is stale")
	}
	if (ci.Purpose == "device" && ci.OwnerGeneration != 0) || (ci.Purpose != "device" && ci.Purpose != "owner") {
		return domain.Fail(domain.Denied, "certificate actor owner generation or purpose is stale")
	}
	if ci.Purpose == "owner" {
		_, err = identityOwnerKey(ctx, q, identity, agent, ci.OwnerGeneration)
		return err
	}
	return nil
}

func revokeIdentity(ctx context.Context, tx *sql.Tx, id string) error {
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", id)
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return nil
		}
		return err
	}
	if identity.State == "revoked" || identity.State == "banned" {
		return nil
	}
	identity.State, identity.OwnerKey = "revoked", ""
	identity.Generation++
	identity.OwnerGeneration++
	if _, err := tx.ExecContext(ctx, "UPDATE identity_keys SET retired=1 WHERE agent_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges WHERE agent_id=?", id); err != nil {
		return err
	}
	return saveIdentity(ctx, tx, identity)
}

func (s *Store) stopIdentityWork(ctx context.Context, tx *sql.Tx, target, actor, action string) error {
	rows, err := tx.QueryContext(ctx, `SELECT data FROM tasks WHERE assignee_id=?
OR json_extract(data,'$.readiness.actor_id')=? OR json_extract(data,'$.retry_approval.actor_id')=?
OR json_extract(data,'$.active_run.agent_id')=?`, target, target, target, target)
	if err != nil {
		return err
	}
	tasks, err := scanJSON[domain.Task](rows)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ActiveRun != nil {
			task.ActiveRun.StopRequested = true
		}
		task.Readiness, task.RetryApproval = nil, nil
		if err := s.saveTask(ctx, tx, &task, actor, action); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) BanIdentity(ctx context.Context, id, reason string) error {
	return s.banIdentity(ctx, id, 0, 0, reason, false)
}

// BanDuplicateIdentity commits separately from the request that returns Denied.
// The API must first prove the contender and freshly ping the incumbent owner.
func (s *Store) BanDuplicateIdentity(ctx context.Context, id string, expectedKeyGeneration, expectedOwnerGeneration int64, reason string) error {
	return s.banIdentity(ctx, id, expectedKeyGeneration, expectedOwnerGeneration, reason, true)
}

func (s *Store) banIdentity(ctx context.Context, id string, keyGeneration, ownerGeneration int64, reason string, duplicate bool) error {
	if err := domain.CheckText(reason, "identity ban reason", 2048, true); err != nil {
		return err
	}
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	agent, err := get[domain.Agent](ctx, tx, "agents", id)
	if err != nil {
		return err
	}
	identity, identityErr := get[domain.AgentIdentity](ctx, tx, "agent_identities", id)
	if identityErr != nil && domain.AsFault(identityErr).Code != domain.NotFound {
		return identityErr
	}
	if duplicate {
		if agent.Founder {
			return domain.Fail(domain.Denied, "Founder permits multiple independently authenticated owners")
		}
		if identityErr != nil || keyGeneration <= 0 || ownerGeneration <= 0 || identity.Generation != keyGeneration || identity.OwnerGeneration != ownerGeneration {
			return domain.Fail(domain.Conflict, "duplicate evidence belongs to a retired key or owner generation")
		}
		if err := identityAllowed(identity, agent, true); err != nil {
			return err
		}
		if identity.OwnerKey == "" {
			return domain.Fail(domain.Conflict, "identity has no incumbent owner")
		}
	}
	if agent.Banned {
		return nil
	}
	agent.Active, agent.Banned, agent.BanReason = false, true, reason
	agent.Revision++
	if err := saveIdentityAgent(ctx, tx, agent); err != nil {
		return err
	}
	if identityErr == nil {
		identity.State, identity.BanReason, identity.OwnerKey = "banned", reason, ""
		identity.Generation++
		identity.OwnerGeneration++
		if err := saveIdentity(ctx, tx, identity); err != nil {
			return err
		}
		if err := retireFounderDevices(ctx, tx, id, "banned"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE identity_keys SET retired=1 WHERE agent_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges WHERE agent_id=?", id); err != nil {
		return err
	}
	if err := s.stopIdentityWork(ctx, tx, id, "identity-security", "identity-banned"); err != nil {
		return err
	}
	if err := s.history(ctx, tx, id, "identity-security", "identity-banned", agent, reason); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, id, "identity.ban", "accepted"); err != nil {
		return err
	}
	if agent.Founder {
		if err := s.audit(ctx, tx, id, "identity.founder-offline-recovery-required", "accepted"); err != nil {
			return err
		}
	} else if err := s.notifyIdentityBan(ctx, tx, id); err != nil {
		return err
	}
	if err := s.event(ctx, tx, "agent", id, "", ""); err != nil {
		return err
	}
	return s.commit(ctx, tx)
}

func (s *Store) notifyIdentityBan(ctx context.Context, tx *sql.Tx, target string) error {
	var founder string
	err := tx.QueryRowContext(ctx, "SELECT id FROM agents WHERE active=1 AND json_extract(data,'$.founder')=1 AND coalesce(json_extract(data,'$.banned'),0)=0 ORDER BY id LIMIT 1").Scan(&founder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	message := domain.Message{ID: domain.NewID("message"), SenderID: founder, RecipientID: founder,
		Body:   fmt.Sprintf("Security: identity %s is banned. All of its device and owner access is denied. Explicit recovery with fresh identity keys and account reprovisioning is required; existing work ownership remains fenced.", target),
		Origin: "service", Kind: "authentication", Urgent: true, State: "pending", CreatedAt: s.timestamp(), Revision: 1}
	data, err := encode(message)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,1)", message.ID, data, founder, founder, "pending")
	return err
}

func (s *Store) RecoverIdentity(ctx context.Context, activeFounder, id string, req domain.RecoverIdentityRequest) (domain.AgentIdentity, error) {
	return s.identityMutation(ctx, activeFounder, "identity.recover:"+id, req.Command, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		founder, err := s.actor(ctx, tx, activeFounder)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if !founder.Founder || founder.Banned {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "only active Founder may recover an identity")
		}
		return s.recoverIdentity(ctx, tx, activeFounder, id, req)
	})
}

// RecoverFounderOffline requires the caller's root-only local maintenance boundary.
// It recovers the sole enrolled Founder without resetting its state.
func (s *Store) RecoverFounderOffline(ctx context.Context, req domain.RecoverIdentityRequest) (domain.AgentIdentity, error) {
	return s.identityMutation(ctx, "identity-founder-offline", "identity.founder-recover", req.Command, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM agents WHERE json_extract(data,'$.founder')=1 ORDER BY id")
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return domain.AgentIdentity{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if len(ids) != 1 {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "offline recovery requires exactly one existing Founder")
		}
		return s.recoverIdentity(ctx, tx, "identity-founder-offline", ids[0], req)
	})
}

func (s *Store) recoverIdentity(ctx context.Context, tx *sql.Tx, actor, id string, req domain.RecoverIdentityRequest) (domain.AgentIdentity, error) {
	if err := domain.CheckText(req.Reason, "identity recovery reason", 2048, true); err != nil {
		return domain.AgentIdentity{}, err
	}
	agent, err := get[domain.Agent](ctx, tx, "agents", id)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	if req.ExpectedRevision != 0 {
		if err := expect(agent.Revision, req.ExpectedRevision); err != nil {
			return domain.AgentIdentity{}, err
		}
	}
	proposal := identityProposal{Kind: "recover", Target: id, Reason: req.Reason, Identity: req.Identity}
	if identity, found, err := replayIdentityRequest(ctx, tx, proposal); found || err != nil {
		return identity, err
	}
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", id)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	identity, err = s.replaceIdentityKeys(ctx, tx, identity, req.Identity, actor)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := retireFounderDevices(ctx, tx, id, "revoked"); err != nil {
		return domain.AgentIdentity{}, err
	}
	agent.Active, agent.Banned, agent.BanReason = false, false, ""
	agent.Screens = nil
	agent.Revision++
	if err := saveIdentityAgent(ctx, tx, agent); err != nil {
		return domain.AgentIdentity{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges WHERE agent_id=?", id); err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := s.stopIdentityWork(ctx, tx, id, actor, "identity-recovered"); err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := recordIdentityRequest(ctx, tx, proposal, identity); err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := s.history(ctx, tx, id, actor, "identity-recovered", agent, req.Reason); err != nil {
		return domain.AgentIdentity{}, err
	}
	return identity, s.event(ctx, tx, "agent", id, "", "")
}
