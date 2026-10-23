package foundation

import (
	"context"
	"crypto"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

const identitySecuritySchema = `
CREATE TABLE agent_identities (
 id TEXT PRIMARY KEY REFERENCES agents(id), data BLOB NOT NULL,
 ssh_user TEXT NOT NULL UNIQUE, uid INTEGER NOT NULL CHECK(uid >= -1)
);
CREATE UNIQUE INDEX identity_uid ON agent_identities(uid) WHERE uid >= 0;
CREATE TABLE identity_accounts (
 uid INTEGER PRIMARY KEY CHECK(uid > 0), ssh_user TEXT NOT NULL UNIQUE,
 agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id)
);
CREATE TABLE identity_keys (
 fingerprint TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id),
 generation INTEGER NOT NULL, kind TEXT NOT NULL, retired INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX identity_keys_agent ON identity_keys(agent_id,generation);
CREATE TABLE identity_requests (
 request_id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL,
 agent_id TEXT NOT NULL REFERENCES agents(id), generation INTEGER NOT NULL
);
CREATE TABLE identity_challenges (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id), nonce_hash TEXT NOT NULL UNIQUE,
 generation INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE TABLE identity_receipts (
 actor_id TEXT NOT NULL, operation_id TEXT NOT NULL,
 agent_id TEXT NOT NULL REFERENCES agents(id), generation INTEGER NOT NULL,
 PRIMARY KEY(actor_id,operation_id),
 FOREIGN KEY(actor_id,operation_id) REFERENCES operations(actor_id,operation_id) DEFERRABLE INITIALLY DEFERRED
);
`

func (s *Store) InstanceID(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := identityInstance(ctx, tx)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func identityInstance(ctx context.Context, tx *sql.Tx) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'instance_id'").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = domain.NewID("instance")
		_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('instance_id',?)", id)
	}
	if err == nil && id == "" {
		err = domain.Fail(domain.Internal, "Foundation instance identity is missing")
	}
	return id, err
}

func (s *Store) Identity(ctx context.Context, agentID string) (domain.AgentIdentity, error) {
	return get[domain.AgentIdentity](ctx, s.db, "agent_identities", agentID)
}

// ManagedIdentities is for trusted local account reconciliation, not network listing.
func (s *Store) ManagedIdentities(ctx context.Context) ([]domain.AgentIdentity, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM agent_identities ORDER BY id")
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.AgentIdentity](rows)
}

func identityByUID(ctx context.Context, q queryer, uid int64) (domain.AgentIdentity, error) {
	if uid <= 0 {
		return domain.AgentIdentity{}, domain.Fail(domain.Denied, "a provisioned dedicated account is required")
	}
	var data []byte
	if err := q.QueryRowContext(ctx, "SELECT data FROM agent_identities WHERE uid = ?", uid).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "account is not bound to an identity")
		}
		return domain.AgentIdentity{}, err
	}
	var identity domain.AgentIdentity
	err := json.Unmarshal(data, &identity)
	return identity, err
}

func (s *Store) IdentityByUID(ctx context.Context, uid int64) (domain.AgentIdentity, error) {
	return identityByUID(ctx, s.db, uid)
}

func identitySSHUser(agentID string) (string, error) {
	value := strings.TrimPrefix(agentID, "agent_")
	if value == agentID || len(value) < 20 || value != strings.ToLower(value) {
		return "", domain.Fail(domain.Invalid, "agent ID cannot name a dedicated account")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", domain.Fail(domain.Invalid, "agent ID cannot name a dedicated account")
	}
	return "fdna_" + value[:20], nil
}

func identityConstraint(err error) error {
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) && sqliteError.Code()&255 == 19 {
		return domain.Fail(domain.Conflict, "identity key, request, or account is already bound")
	}
	return err
}

func saveIdentity(ctx context.Context, tx *sql.Tx, identity domain.AgentIdentity) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key=? AND json_extract(value,'$.generation')!=?", agentStatusKey+identity.AgentID, identity.Generation); err != nil {
		return err
	}
	data, err := encode(identity)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_identities(id,data,ssh_user,uid) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data,ssh_user=excluded.ssh_user,uid=excluded.uid`,
		identity.AgentID, data, identity.SSHUser, identity.UID)
	return identityConstraint(err)
}

func saveIdentityAgent(ctx context.Context, tx *sql.Tx, agent domain.Agent) error {
	agent.Status = nil
	data, err := encode(agent)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE agents SET data=?,active=? WHERE id=?", data, agent.Active, agent.ID)
	return err
}

func identityAllowed(identity domain.AgentIdentity, agent domain.Agent, active bool) error {
	if agent.Banned || identity.State == "banned" {
		return domain.Fail(domain.Denied, "identity is banned; explicit recovery with fresh keys is required")
	}
	if identity.State == identityRestorePending {
		return domain.Fail(domain.Denied, "restored identity requires explicit recovery with fresh keys")
	}
	if active && (!agent.Active || identity.State != "active") {
		return domain.Fail(domain.Denied, "identity is not active")
	}
	if identity.State == "revoked" || (!active && identity.State == "active" && !agent.Active) {
		return domain.Fail(domain.Denied, "identity is revoked")
	}
	return nil
}

// Identity receipts add a generation fence to the shared bounded agent reference.
// Replaying an old activation must never activate a subsequently recovered keypair.
func (s *Store) identityMutation(ctx context.Context, actor, action string, command domain.Command, request any,
	fn func(*sql.Tx) (domain.AgentIdentity, error),
) (domain.AgentIdentity, error) {
	agent, err := transact(ctx, s, actor, action, command.OperationID, request, func(tx *sql.Tx) (domain.Agent, error) {
		identity, err := fn(tx)
		if err != nil {
			return domain.Agent{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_receipts(actor_id,operation_id,agent_id,generation) VALUES(?,?,?,?)",
			actor, command.OperationID, identity.AgentID, identity.Generation); err != nil {
			return domain.Agent{}, err
		}
		return get[domain.Agent](ctx, tx, "agents", identity.AgentID)
	})
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	var data, agentData []byte
	var generation int64
	err = s.db.QueryRowContext(ctx, `SELECT i.data,a.data,r.generation FROM identity_receipts r
JOIN agent_identities i ON i.id=r.agent_id JOIN agents a ON a.id=i.id
WHERE r.actor_id=? AND r.operation_id=? AND r.agent_id=?`, actor, command.OperationID, agent.ID).Scan(&data, &agentData, &generation)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	var identity domain.AgentIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return identity, err
	}
	if err := json.Unmarshal(agentData, &agent); err != nil {
		return identity, err
	}
	if err := identityAllowed(identity, agent, false); err != nil {
		return domain.AgentIdentity{}, err
	}
	if generation != identity.Generation {
		return domain.AgentIdentity{}, domain.Fail(domain.Conflict, "identity operation belongs to a retired key generation")
	}
	return identity, nil
}

type identityProposal struct {
	Kind     string                `json:"kind"`
	Target   string                `json:"target"`
	Reason   string                `json:"reason,omitempty"`
	Identity domain.PublicIdentity `json:"identity"`
}

func identityProposalHash(proposal identityProposal) (string, error) {
	data, err := encode(proposal)
	return domain.Hash(string(data)), err
}

func replayIdentityRequest(ctx context.Context, tx *sql.Tx, proposal identityProposal) (domain.AgentIdentity, bool, error) {
	var id, fingerprint string
	var generation int64
	err := tx.QueryRowContext(ctx, "SELECT agent_id,fingerprint,generation FROM identity_requests WHERE request_id=?", proposal.Identity.RequestID).Scan(&id, &fingerprint, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentIdentity{}, false, nil
	}
	if err != nil {
		return domain.AgentIdentity{}, false, err
	}
	hash, err := identityProposalHash(proposal)
	if err != nil {
		return domain.AgentIdentity{}, true, err
	}
	if fingerprint != hash {
		return domain.AgentIdentity{}, true, domain.Fail(domain.Conflict, "identity request ID was already used for a different proposal")
	}
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", id)
	if err != nil {
		return identity, true, err
	}
	agent, err := get[domain.Agent](ctx, tx, "agents", id)
	if err != nil {
		return identity, true, err
	}
	if err := identityAllowed(identity, agent, false); err != nil {
		return identity, true, err
	}
	if generation != identity.Generation {
		return identity, true, domain.Fail(domain.Conflict, "identity request belongs to a retired key generation")
	}
	return identity, true, nil
}

func recordIdentityRequest(ctx context.Context, tx *sql.Tx, proposal identityProposal, identity domain.AgentIdentity) error {
	fingerprint, err := identityProposalHash(proposal)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO identity_requests(request_id,fingerprint,agent_id,generation) VALUES(?,?,?,?)",
		proposal.Identity.RequestID, fingerprint, identity.AgentID, identity.Generation)
	return identityConstraint(err)
}

func validateIdentityKeys(public domain.PublicIdentity) (sshFingerprint, authFingerprint, canonicalSSH string, err error) {
	sshFingerprint, authFingerprint, err = device.Validate(public)
	if err != nil {
		return "", "", "", domain.Fail(domain.Invalid, "identity requires valid distinct keys and both ownership proofs")
	}
	sshKey, err := device.SSHPublicKey(public.SSHKey)
	if err != nil {
		return "", "", "", domain.Fail(domain.Invalid, "invalid SSH identity key")
	}
	canonicalSSH, err = device.Fingerprint(sshKey)
	if err != nil {
		return "", "", "", err
	}
	if canonicalSSH == authFingerprint {
		return "", "", "", domain.Fail(domain.Invalid, "SSH and authentication keys must be distinct")
	}
	return sshFingerprint, authFingerprint, canonicalSSH, nil
}

func reserveIdentityKeys(ctx context.Context, tx *sql.Tx, identity domain.AgentIdentity, canonicalSSH string) error {
	for _, key := range []struct{ fingerprint, kind string }{{canonicalSSH, "ssh"}, {identity.AuthFingerprint, "auth"}} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_keys(fingerprint,agent_id,generation,kind) VALUES(?,?,?,?)",
			key.fingerprint, identity.AgentID, identity.Generation, key.kind); err != nil {
			return identityConstraint(err)
		}
	}
	return nil
}

func (s *Store) newIdentity(ctx context.Context, tx *sql.Tx, agent domain.Agent, public domain.PublicIdentity, approver string) (domain.AgentIdentity, error) {
	sshFingerprint, authFingerprint, canonicalSSH, err := validateIdentityKeys(public)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	instance, err := identityInstance(ctx, tx)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	user, err := identitySSHUser(agent.ID)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	identity := domain.AgentIdentity{AgentID: agent.ID, InstanceID: instance, RequestID: public.RequestID,
		SSHUser: user, UID: -1, SSHKey: public.SSHKey, SSHFingerprint: sshFingerprint,
		AuthKey: public.AuthKey, AuthFingerprint: authFingerprint, Generation: 1,
		State: "pending", Assurance: "software", ApprovedBy: approver}
	if err := reserveIdentityKeys(ctx, tx, identity, canonicalSSH); err != nil {
		return identity, err
	}
	return identity, saveIdentity(ctx, tx, identity)
}

func (s *Store) BootstrapIdentity(ctx context.Context, public domain.PublicIdentity, op string) (domain.AgentIdentity, error) {
	proposal := identityProposal{Kind: "bootstrap", Identity: public}
	return s.identityMutation(ctx, "identity-bootstrap", "identity.bootstrap", domain.Command{OperationID: op}, public, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		if identity, found, err := replayIdentityRequest(ctx, tx, proposal); found || err != nil {
			return identity, err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents").Scan(&count); err != nil {
			return domain.AgentIdentity{}, err
		}
		if count != 0 {
			return domain.AgentIdentity{}, domain.Fail(domain.Conflict, "Foundation already has an identity; explicit migration or recovery is required")
		}
		agent := domain.Agent{ID: domain.NewID("agent"), Name: public.Name, Founder: true, Revision: 1,
			Mode: "manual", CreatedAt: s.timestamp()}
		data, err := encode(agent)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,0)", agent.ID, data, domain.Hash(domain.NewToken())); err != nil {
			return domain.AgentIdentity{}, err
		}
		identity, err := s.newIdentity(ctx, tx, agent, public, agent.ID)
		if err != nil {
			return identity, err
		}
		if err := recordIdentityRequest(ctx, tx, proposal, identity); err != nil {
			return identity, err
		}
		return identity, s.event(ctx, tx, "agent", agent.ID, "", "")
	})
}

func (s *Store) RegisterIdentity(ctx context.Context, actor string, req domain.RegisterIdentityRequest) (domain.AgentIdentity, error) {
	proposal := identityProposal{Kind: "register", Target: req.ExistingAgentID, Identity: req.Identity}
	return s.identityMutation(ctx, actor, "identity.register", req.Command, req, func(tx *sql.Tx) (domain.AgentIdentity, error) {
		founder, err := s.actor(ctx, tx, actor)
		if err != nil {
			return domain.AgentIdentity{}, err
		}
		if !founder.Founder || founder.Banned {
			return domain.AgentIdentity{}, domain.Fail(domain.Denied, "only active Founder may register identities")
		}
		if identity, found, err := replayIdentityRequest(ctx, tx, proposal); found || err != nil {
			return identity, err
		}
		agent := domain.Agent{ID: domain.NewID("agent"), Name: req.Identity.Name, SponsorID: actor,
			CreatedAt: s.timestamp(), Revision: 1, Mode: "ai"}
		var quarantined *domain.AgentIdentity
		if req.ExistingAgentID != "" {
			agent, err = get[domain.Agent](ctx, tx, "agents", req.ExistingAgentID)
			if err != nil {
				return domain.AgentIdentity{}, err
			}
			if agent.Banned {
				return domain.AgentIdentity{}, domain.Fail(domain.Denied, "migration cannot restore a banned agent")
			}
			existing, identityErr := get[domain.AgentIdentity](ctx, tx, "agent_identities", agent.ID)
			if identityErr != nil && domain.AsFault(identityErr).Code != domain.NotFound {
				return domain.AgentIdentity{}, identityErr
			}
			if identityErr == nil && existing.State == identityRestorePending {
				quarantined = &existing
			}
			if !agent.Active && quarantined == nil {
				return domain.AgentIdentity{}, domain.Fail(domain.Denied, "migration cannot restore a revoked agent")
			}
			if identityErr == nil && quarantined == nil {
				return domain.AgentIdentity{}, domain.Fail(domain.Conflict, "agent already has an identity; use explicit recovery")
			}
			agent.Active = false
			agent.Revision++
			if err := saveIdentityAgent(ctx, tx, agent); err != nil {
				return domain.AgentIdentity{}, err
			}
			if err := s.stopIdentityWork(ctx, tx, agent.ID, actor, "identity-migrated"); err != nil {
				return domain.AgentIdentity{}, err
			}
		} else {
			data, err := encode(agent)
			if err != nil {
				return domain.AgentIdentity{}, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,0)", agent.ID, data, domain.Hash(domain.NewToken())); err != nil {
				return domain.AgentIdentity{}, err
			}
		}
		var identity domain.AgentIdentity
		if quarantined != nil {
			identity, err = s.replaceIdentityKeys(ctx, tx, *quarantined, req.Identity, actor)
		} else {
			identity, err = s.newIdentity(ctx, tx, agent, req.Identity, actor)
		}
		if err != nil {
			return identity, err
		}
		if err := recordIdentityRequest(ctx, tx, proposal, identity); err != nil {
			return identity, err
		}
		return identity, s.event(ctx, tx, "agent", agent.ID, "", "")
	})
}

// BindIdentityAccount accepts only the trusted local provisioner's result.
func (s *Store) BindIdentityAccount(ctx context.Context, agentID, user string, uid int64, sshFingerprint string) (domain.AgentIdentity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	defer tx.Rollback()
	identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", agentID)
	if err != nil {
		return identity, err
	}
	agent, err := get[domain.Agent](ctx, tx, "agents", agentID)
	if err != nil {
		return identity, err
	}
	if err := identityAllowed(identity, agent, false); err != nil {
		return domain.AgentIdentity{}, err
	}
	expected, err := identitySSHUser(agentID)
	if err != nil {
		return identity, err
	}
	if user != expected || user != identity.SSHUser || uid <= 0 || uid >= 1<<32-1 || sshFingerprint != identity.SSHFingerprint {
		return domain.AgentIdentity{}, domain.Fail(domain.Invalid, "provisioned account or SSH key does not match the approved identity")
	}
	var previousUID int64
	var previousUser string
	err = tx.QueryRowContext(ctx, "SELECT uid,ssh_user FROM identity_accounts WHERE agent_id=?", agentID).Scan(&previousUID, &previousUser)
	if err == nil {
		if previousUID != uid || previousUser != user {
			return domain.AgentIdentity{}, domain.Fail(domain.Conflict, "identity account binding is immutable")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_accounts(uid,ssh_user,agent_id) VALUES(?,?,?)", uid, user, agentID); err != nil {
			return domain.AgentIdentity{}, identityConstraint(err)
		}
	} else {
		return domain.AgentIdentity{}, err
	}
	if identity.UID == uid && (identity.State == "provisioned" || identity.State == "active") {
		return identity, tx.Commit()
	}
	if identity.State != "pending" || identity.UID != -1 {
		return domain.AgentIdentity{}, domain.Fail(domain.Conflict, "identity is not awaiting account provisioning")
	}
	identity.UID, identity.State = uid, "provisioned"
	if err := saveIdentity(ctx, tx, identity); err != nil {
		return identity, err
	}
	if err := s.audit(ctx, tx, "local-provisioner", "identity.bind-account", "accepted"); err != nil {
		return identity, err
	}
	if err := s.event(ctx, tx, "agent", agentID, "", ""); err != nil {
		return identity, err
	}
	return identity, tx.Commit()
}

func equalIdentityKeys(left, right crypto.PublicKey) bool {
	equal, ok := left.(interface{ Equal(crypto.PublicKey) bool })
	return ok && right != nil && equal.Equal(right)
}
