package foundation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestFounderDeviceMigrationPreservesSQL6BindingsAndReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "devices.sqlite")
	previous := []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, notesSchema}
	db, err := database.Open(ctx, path, previous, 5)
	if err != nil {
		t.Fatal(err)
	}
	keys, owner := securityPublic(t, "Retained Founder"), securityPublic(t, "Retained owner")
	agent := domain.Agent{ID: domain.NewID("agent"), Name: "Retained Founder", Founder: true, Active: true, Mode: "manual", Revision: 9}
	data, _ := encode(agent)
	if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, domain.Hash(domain.NewToken())); err != nil {
		t.Fatal(err)
	}
	sshFingerprint, authFingerprint, canonicalSSH, err := validateIdentityKeys(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.AgentIdentity{AgentID: agent.ID, InstanceID: domain.NewID("instance"), RequestID: keys.public.RequestID,
		SSHUser: "retained-founder", UID: 4100, SSHKey: keys.public.SSHKey, SSHFingerprint: sshFingerprint,
		AuthKey: keys.public.AuthKey, AuthFingerprint: authFingerprint, Generation: 4, State: "active",
		OwnerGeneration: 7, OwnerKey: owner.public.AuthKey, ApprovedBy: agent.ID, Assurance: "software"}
	data, _ = encode(identity)
	if _, err := db.Exec("INSERT INTO agent_identities(id,data,ssh_user,uid) VALUES(?,?,?,?)", agent.ID, data, identity.SSHUser, identity.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO settings(key,value) VALUES('instance_id',?)", identity.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO identity_accounts(uid,ssh_user,agent_id) VALUES(?,?,?)", identity.UID, identity.SSHUser, agent.ID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []struct{ fingerprint, kind string }{{canonicalSSH, "ssh"}, {authFingerprint, "auth"}} {
		if _, err := db.Exec("INSERT INTO identity_keys(fingerprint,agent_id,generation,kind) VALUES(?,?,?,?)", key.fingerprint, agent.ID, identity.Generation, key.kind); err != nil {
			t.Fatal(err)
		}
	}
	proposalHash, err := identityProposalHash(identityProposal{Kind: "bootstrap", Identity: keys.public})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO identity_requests(request_id,fingerprint,agent_id,generation) VALUES(?,?,?,?)", keys.public.RequestID, proposalHash, agent.ID, identity.Generation); err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := encode(struct {
		Action string `json:"action"`
		Input  any    `json:"input"`
	}{"identity.bootstrap", keys.public})
	receipt, err := resultReceipt(agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO operations(actor_id,operation_id,fingerprint,result) VALUES('identity-bootstrap','retained-bootstrap',?,?)", domain.Hash(string(fingerprint)), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO identity_receipts(actor_id,operation_id,agent_id,generation) VALUES('identity-bootstrap','retained-bootstrap',?,?)", agent.ID, identity.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO identity_challenges(agent_id,nonce_hash,generation,expires_at) VALUES(?,?,?,?)", agent.ID, domain.Hash(domain.NewToken()), identity.Generation, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM update_changes"); err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{
		"agent_identities":  "SELECT id,data,ssh_user,uid FROM agent_identities ORDER BY rowid",
		"identity_keys":     "SELECT fingerprint,agent_id,generation,kind,retired FROM identity_keys ORDER BY rowid",
		"identity_requests": "SELECT request_id,fingerprint,agent_id,generation FROM identity_requests ORDER BY rowid",
		"identity_receipts": "SELECT actor_id,operation_id,agent_id,generation FROM identity_receipts ORDER BY rowid",
	}
	for _, table := range []string{"agents", "identity_accounts", "identity_challenges", "operations", "settings", "messages", "histories", "events", "update_state", "update_feed", "update_recipients", "update_changes"} {
		queries[table] = "SELECT * FROM " + table + " ORDER BY rowid"
	}
	hashes := map[string]string{}
	for table, query := range queries {
		hashes[table] = securityQueryHash(t, db, query)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for table, query := range queries {
		if hash := securityQueryHash(t, s.db, query); hash != hashes[table] {
			s.Close()
			t.Fatal("device migration changed retained records", table)
		}
	}
	replayed, err := s.BootstrapIdentity(ctx, keys.public, "retained-bootstrap")
	if err != nil || replayed != identity {
		s.Close()
		t.Fatal("migration lost a generation-bound receipt", replayed, err)
	}
	if _, err := s.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "owner"), owner.auth.Public()); err != nil {
		s.Close()
		t.Fatal("migration lost the retained Founder owner binding", err)
	}
	if _, err := s.RegisterFounderDevice(ctx, agent.ID, domain.RegisterIdentityRequest{Command: op(), Identity: securityPublic(t, "New Founder Phone").public}); err != nil {
		s.Close()
		t.Fatal("migrated registry cannot enroll a Founder device", err)
	}
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	invalid := rows.Next()
	rows.Close()
	if invalid {
		s.Close()
		t.Fatal("migration broke a retained foreign key")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if old, err := database.Open(ctx, path, previous, 5); err == nil {
		old.Close()
		t.Fatal("SQL6 binary accepted a SQL7 identity registry")
	} else {
		assertCode(t, err, domain.Prerequisite)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if replayed, err := reopened.BootstrapIdentity(ctx, keys.public, "retained-bootstrap"); err != nil || replayed != identity {
		t.Fatal("reopen changed retained identity authority", replayed, err)
	}
}
