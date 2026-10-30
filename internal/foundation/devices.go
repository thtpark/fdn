package foundation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) RegisterFounderDevice(ctx context.Context, actor string, req domain.RegisterIdentityRequest) (domain.AgentIdentity, error) {
	proposal := identityProposal{Kind: "founder-device", Target: actor, Identity: req.Identity}
	return s.identityMutation(ctx, actor, "identity.device-register", req.Command, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		founder, err := s.actor(ctx, tx, actor)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if !founder.Founder {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "only active Founder may enroll additional computers")
		}
		if identity, found, err := replayIdentityRequest(ctx, tx, proposal); found || err != nil {
			return identity, err
		}
		sshFingerprint, authFingerprint, canonicalSSH, err := validateIdentityKeys(req.Identity)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		user, err := domain.AgentUsername(req.Identity.Name)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		identity := domain.AgentIdentity{AgentID: actor, DeviceID: domain.NewID("device"), DeviceName: req.Identity.Name,
			InstanceID: instance, RequestID: req.Identity.RequestID, SSHUser: user, UID: -1,
			SSHKey: req.Identity.SSHKey, SSHFingerprint: sshFingerprint, AuthKey: req.Identity.AuthKey,
			AuthFingerprint: authFingerprint, Generation: 1, State: "pending", Assurance: "software", ApprovedBy: actor}
		if err := reserveIdentityKeys(ctx, tx, identity, canonicalSSH); err != nil {
			return identity, err
		}
		if err := saveIdentity(ctx, tx, identity); err != nil {
			return identity, err
		}
		if err := recordIdentityRequest(ctx, tx, proposal, identity); err != nil {
			return identity, err
		}
		return identity, s.event(ctx, tx, "agent", actor, "", "")
	})
}

func (s *Store) FounderDevices(ctx context.Context, actor string) ([]domain.AgentIdentity, error) {
	founder, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	if !founder.Founder {
		return nil, domain.Fail(domain.Denied, "only Founder may inspect Founder devices")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM agent_identities WHERE agent_id=? ORDER BY id", actor)
	if err != nil {
		return nil, err
	}
	devices, err := scanJSON[domain.AgentIdentity](rows)
	for i := range devices {
		devices[i].OwnerKey, devices[i].OwnerGeneration = "", 0
		if devices[i].DeviceName == "" {
			devices[i].DeviceName = founder.Name
		}
	}
	return devices, err
}

func (s *Store) RevokeFounderDevice(ctx context.Context, actor, id string, req domain.RevokeDeviceRequest) (domain.AgentIdentity, error) {
	return transact(ctx, s, actor, "identity.device-revoke:"+id, req.OperationID, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		founder, err := s.actor(ctx, tx, actor)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if !founder.Founder {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "only Founder may revoke a Founder device")
		}
		if err := domain.CheckText(req.Reason, "device revocation reason", 2048, true); err != nil {
			return domain.AgentIdentity{}, err
		}
		identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", id)
		if err != nil {
			return identity, err
		}
		if identity.AgentID != actor {
			return identity, domain.Fail(domain.Denied, "device does not belong to Founder")
		}
		if ci, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok && ci.BindingID() == id {
			return identity, domain.Fail(domain.Prerequisite, "revoke this computer from another enrolled Founder device")
		}
		if identity.State == "revoked" {
			return identity, nil
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_identities WHERE agent_id=? AND id!=? AND json_extract(data,'$.state')='active'", actor, id).Scan(&remaining); err != nil {
			return identity, err
		}
		if remaining == 0 {
			return identity, domain.Fail(domain.Prerequisite, "activate another Founder device before revoking the last active computer")
		}
		identity.State, identity.OwnerKey = "revoked", ""
		identity.Generation++
		identity.OwnerGeneration++
		if err := saveIdentity(ctx, tx, identity); err != nil {
			return identity, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_keys SET retired=1 WHERE agent_id=? AND device_id=?", actor, identity.DeviceID); err != nil {
			return identity, err
		}
		if err := retireBindingOwners(ctx, tx, id); err != nil {
			return identity, err
		}
		return identity, s.event(ctx, tx, "agent", actor, "", "")
	})
}

func retireBindingOwners(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_owners WHERE identity_id=?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges WHERE agent_id=?", id)
	return err
}

func retireFounderDevices(ctx context.Context, tx *sql.Tx, actor, state string) error {
	rows, err := tx.QueryContext(ctx, "SELECT data FROM agent_identities WHERE agent_id=? AND id!=?", actor, actor)
	if err != nil {
		return err
	}
	devices, err := scanJSON[domain.AgentIdentity](rows)
	if err != nil {
		return err
	}
	for _, identity := range devices {
		if identity.State != state {
			identity.State, identity.OwnerKey = state, ""
			identity.Generation++
			identity.OwnerGeneration++
			if err := saveIdentity(ctx, tx, identity); err != nil {
				return err
			}
		}
		if err := retireBindingOwners(ctx, tx, identity.BindingID()); err != nil {
			return err
		}
	}
	return retireBindingOwners(ctx, tx, actor)
}

func identityOwnerKey(ctx context.Context, q queryer, identity domain.AgentIdentity, agent domain.Agent, generation int64) (string, error) {
	if generation <= 0 {
		return "", domain.Fail(domain.Denied, "owner certificate is stale")
	}
	if !agent.Founder {
		if identity.OwnerKey == "" || identity.OwnerGeneration != generation {
			return "", domain.Fail(domain.Denied, "owner certificate is stale")
		}
		return identity.OwnerKey, nil
	}
	var key string
	err := q.QueryRowContext(ctx, "SELECT owner_key FROM identity_owners WHERE identity_id=? AND generation=?", identity.BindingID(), generation).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.Fail(domain.Denied, "Founder owner connection is retired")
	}
	return key, err
}

func (s *Store) IdentityOwner(ctx context.Context, id string, generation int64) (domain.AgentIdentity, error) {
	identity, err := s.Identity(ctx, id)
	if err != nil {
		return identity, err
	}
	agent, err := get[domain.Agent](ctx, s.db, "agents", identity.AgentID)
	if err != nil {
		return identity, err
	}
	if err := identityAllowed(identity, agent, true); err != nil {
		return identity, err
	}
	key, err := identityOwnerKey(ctx, s.db, identity, agent, generation)
	if err != nil {
		return identity, err
	}
	identity.OwnerKey, identity.OwnerGeneration = key, generation
	return identity, nil
}

func (s *Store) ReleaseIdentityOwner(ctx context.Context, id string, generation int64, key string) error {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_owners WHERE identity_id=? AND generation=? AND owner_key=?", id, generation, key); err != nil {
		return err
	}
	return s.commit(ctx, tx)
}
