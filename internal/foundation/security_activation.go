package foundation

import (
	"context"
	"crypto"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

const identityChallengeLifetime = 120

func (s *Store) ChallengeIdentity(ctx context.Context, uid int64) (domain.IdentityChallenge, error) {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return domain.IdentityChallenge{}, err
	}
	defer finish()
	identity, err := identityByUID(ctx, tx, uid)
	if err != nil {
		return domain.IdentityChallenge{}, err
	}
	agent, err := get[domain.Agent](ctx, tx, "agents", identity.AgentID)
	if err != nil {
		return domain.IdentityChallenge{}, err
	}
	if err := identityAllowed(identity, agent, false); err != nil {
		return domain.IdentityChallenge{}, err
	}
	if identity.State != "provisioned" && identity.State != "active" {
		return domain.IdentityChallenge{}, domain.Fail(domain.Denied, "identity account has not been provisioned")
	}
	challenge := domain.IdentityChallenge{Nonce: domain.NewToken(), AgentID: identity.AgentID, DeviceID: identity.DeviceID,
		InstanceID: identity.InstanceID, Generation: identity.Generation, ExpiresAt: s.timestamp() + identityChallengeLifetime}
	_, err = tx.ExecContext(ctx, `INSERT INTO identity_challenges(agent_id,nonce_hash,generation,expires_at) VALUES(?,?,?,?)
ON CONFLICT(agent_id) DO UPDATE SET nonce_hash=excluded.nonce_hash,generation=excluded.generation,expires_at=excluded.expires_at`,
		identity.BindingID(), domain.Hash(challenge.Nonce), challenge.Generation, challenge.ExpiresAt)
	if err != nil {
		return domain.IdentityChallenge{}, err
	}
	return challenge, s.commit(ctx, tx)
}

func (s *Store) ActivateIdentity(ctx context.Context, uid int64, req domain.ActivateIdentityRequest) (domain.AgentIdentity, error) {
	actor := "identity-uid:" + strconv.FormatInt(uid, 10)
	return s.identityMutation(ctx, actor, "identity.activate", req.Command, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		identity, err := identityByUID(ctx, tx, uid)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		agent, err := get[domain.Agent](ctx, tx, "agents", identity.AgentID)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if err := identityAllowed(identity, agent, false); err != nil {
			return domain.AgentIdentity{}, err
		}
		if identity.State != "provisioned" && identity.State != "active" {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "identity account has not been provisioned")
		}
		if len(req.Nonce) != 43 || len(req.Proof) == 0 || len(req.Proof) > 8192 {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "a fresh identity challenge proof is required")
		}
		var nonceHash string
		var generation, expiresAt int64
		err = tx.QueryRowContext(ctx, "SELECT nonce_hash,generation,expires_at FROM identity_challenges WHERE agent_id=?", identity.BindingID()).Scan(&nonceHash, &generation, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "identity challenge is missing or consumed")
		}
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if nonceHash != domain.Hash(req.Nonce) || generation != identity.Generation || expiresAt <= s.timestamp() {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "identity challenge is stale or expired")
		}
		challenge := domain.IdentityChallenge{Nonce: req.Nonce, AgentID: identity.AgentID, DeviceID: identity.DeviceID, InstanceID: identity.InstanceID,
			Generation: generation, ExpiresAt: expiresAt}
		public, err := device.PublicKey(identity.AuthKey)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if err := device.VerifyChallenge(public, challenge, req.Proof); err != nil {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "identity challenge proof is invalid")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges WHERE agent_id=?", identity.BindingID()); err != nil {
			return domain.AgentIdentity{}, err
		}
		identity.State = "active"
		agent.Active = true
		agent.LastSeen = s.timestamp()
		agent.Revision++
		if err := saveIdentityAgent(ctx, tx, agent); err != nil {
			return domain.AgentIdentity{}, err
		}
		if err := saveIdentity(ctx, tx, identity); err != nil {
			return domain.AgentIdentity{}, err
		}
		return identity, s.event(ctx, tx, "agent", identity.AgentID, "", "")
	})
}

func (s *Store) ValidateCertificate(ctx context.Context, uid int64, ci domain.CertificateIdentity, public crypto.PublicKey) (domain.Agent, error) {
	if uid <= 0 || public == nil {
		return domain.Agent{}, domain.Fail(domain.Denied, "invalid identity certificate account or key")
	}
	var identityData, agentData []byte
	var instance string
	err := s.db.QueryRowContext(ctx, `SELECT i.data,a.data,settings.value FROM agent_identities i
JOIN agents a ON a.id=i.agent_id JOIN settings ON settings.key='instance_id' WHERE i.uid=?`, uid).Scan(&identityData, &agentData, &instance)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, domain.Fail(domain.Denied, "certificate account is not enrolled")
	}
	if err != nil {
		return domain.Agent{}, err
	}
	var identity domain.AgentIdentity
	var agent domain.Agent
	if err := json.Unmarshal(identityData, &identity); err != nil {
		return agent, err
	}
	if err := json.Unmarshal(agentData, &agent); err != nil {
		return agent, err
	}
	if err := identityAllowed(identity, agent, true); err != nil {
		return domain.Agent{}, err
	}
	if instance != identity.InstanceID || ci.InstanceID != instance || ci.AgentID != identity.AgentID || ci.DeviceID != identity.DeviceID || ci.Generation != identity.Generation || ci.Generation <= 0 {
		return domain.Agent{}, domain.Fail(domain.Denied, "certificate identity or key generation is stale")
	}
	var key string
	switch ci.Purpose {
	case "device":
		if ci.OwnerGeneration != 0 {
			return domain.Agent{}, domain.Fail(domain.Denied, "device certificate has an owner generation")
		}
		key = identity.AuthKey
	case "owner":
		key, err = identityOwnerKey(ctx, s.db, identity, agent, ci.OwnerGeneration)
		if err != nil {
			return domain.Agent{}, err
		}
	default:
		return domain.Agent{}, domain.Fail(domain.Denied, "certificate purpose is invalid")
	}
	enrolled, err := device.PublicKey(key)
	if err != nil {
		return domain.Agent{}, err
	}
	if !equalIdentityKeys(enrolled, public) {
		return domain.Agent{}, domain.Fail(domain.Denied, "certificate public key is not enrolled for this purpose")
	}
	return agent, nil
}

func (s *Store) SetIdentityOwner(ctx context.Context, agentID, ownerKey string, expectedGeneration int64) (domain.AgentIdentity, error) {
	public, err := device.PublicKey(ownerKey)
	if err != nil {
		return domain.AgentIdentity{}, domain.Fail(domain.Invalid, "a valid owner public key is required")
	}
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	defer finish()
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", agentID)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	agent, err := s.actor(ctx, tx, identity.AgentID)
	if err != nil {
		return identity, err
	}
	if err := identityAllowed(identity, agent, true); err != nil {
		return domain.AgentIdentity{}, err
	}
	if expectedGeneration <= 0 || expectedGeneration != identity.Generation {
		return domain.AgentIdentity{}, domain.Fail(domain.Denied, "owner request belongs to a retired device key generation")
	}
	deviceKey, err := device.PublicKey(identity.AuthKey)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	if equalIdentityKeys(deviceKey, public) {
		return domain.AgentIdentity{}, domain.Fail(domain.Invalid, "owner key must be distinct from the device authentication key")
	}
	if agent.Founder {
		var generation int64
		err := tx.QueryRowContext(ctx, "SELECT generation FROM identity_owners WHERE identity_id=? AND owner_key=?", agentID, ownerKey).Scan(&generation)
		if err == nil {
			identity.OwnerKey, identity.OwnerGeneration = ownerKey, generation
			return identity, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return identity, err
		}
	} else if identity.OwnerKey != "" {
		previous, err := device.PublicKey(identity.OwnerKey)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if equalIdentityKeys(previous, public) {
			return identity, nil
		}
	}
	identity.OwnerKey = ownerKey
	identity.OwnerGeneration++
	if err := saveIdentity(ctx, tx, identity); err != nil {
		return identity, err
	}
	if agent.Founder {
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_owners(identity_id,generation,owner_key) VALUES(?,?,?)", agentID, identity.OwnerGeneration, ownerKey); err != nil {
			return identity, err
		}
	}
	if err := s.audit(ctx, tx, identity.AgentID, "identity.owner", "accepted"); err != nil {
		return identity, err
	}
	if err := s.event(ctx, tx, "agent", identity.AgentID, "", ""); err != nil {
		return identity, err
	}
	return identity, s.commit(ctx, tx)
}
