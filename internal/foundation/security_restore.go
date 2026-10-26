package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

const identityRestorePending = "restore-pending"

// QuarantineRestoredIdentities runs on a validated, isolated restore before it
// replaces service state. Only explicit fresh-key recovery can restore access.
func (s *Store) QuarantineRestoredIdentities(ctx context.Context) error {
	tx, finish, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	rows, err := tx.QueryContext(ctx, "SELECT data FROM agents ORDER BY id")
	if err != nil {
		return err
	}
	agents, err := scanJSON[domain.Agent](rows)
	if err != nil {
		return err
	}
	changed := false
	for _, agent := range agents {
		identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", agent.ID)
		if err != nil {
			if domain.AsFault(err).Code != domain.NotFound {
				return err
			}
			instance, err := identityInstance(ctx, tx)
			if err != nil {
				return err
			}
			user, err := identitySSHUser(agent.ID)
			if err != nil {
				return err
			}
			identity = domain.AgentIdentity{AgentID: agent.ID, InstanceID: instance, SSHUser: user, UID: -1,
				Generation: 1, OwnerGeneration: 1, State: identityRestorePending, Assurance: "software", BanReason: agent.BanReason}
		} else {
			if !agent.Active && identity.State == identityRestorePending && identity.OwnerKey == "" {
				continue
			}
			identity.Generation++
			identity.OwnerGeneration++
			identity.State, identity.OwnerKey = identityRestorePending, ""
		}
		agent.Active = false
		agent.Screens = nil
		agent.Revision++
		if err := saveIdentityAgent(ctx, tx, agent); err != nil {
			return err
		}
		if err := saveIdentity(ctx, tx, identity); err != nil {
			return err
		}
		if err := s.event(ctx, tx, "agent", agent.ID, "", ""); err != nil {
			return err
		}
		changed = true
	}
	if _, err := tx.ExecContext(ctx, "UPDATE identity_keys SET retired=1"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM identity_challenges"); err != nil {
		return err
	}
	if changed {
		if err := s.audit(ctx, tx, "restore-maintenance", "identity.restore-quarantine", "accepted"); err != nil {
			return err
		}
	}
	return s.commit(ctx, tx)
}

func (s *Store) replaceIdentityKeys(ctx context.Context, tx *sql.Tx, identity domain.AgentIdentity, public domain.PublicIdentity, actor string) (domain.AgentIdentity, error) {
	sshFingerprint, authFingerprint, canonicalSSH, err := validateIdentityKeys(public)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	identity.Generation++
	identity.OwnerGeneration++
	identity.OwnerKey, identity.BanReason, identity.State, identity.UID = "", "", "pending", -1
	identity.RequestID, identity.ApprovedBy = public.RequestID, actor
	identity.SSHKey, identity.AuthKey = public.SSHKey, public.AuthKey
	identity.SSHFingerprint, identity.AuthFingerprint = sshFingerprint, authFingerprint
	if _, err := tx.ExecContext(ctx, "UPDATE identity_keys SET retired=1 WHERE agent_id=?", identity.AgentID); err != nil {
		return domain.AgentIdentity{}, err
	}
	if err := reserveIdentityKeys(ctx, tx, identity, canonicalSSH); err != nil {
		return domain.AgentIdentity{}, err
	}
	return identity, saveIdentity(ctx, tx, identity)
}
