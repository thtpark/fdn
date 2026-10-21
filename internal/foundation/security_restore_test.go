package foundation

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

func securityTableHash(t *testing.T, q queryer, table string) string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), "SELECT * FROM "+table+" ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		data, err := encode(values)
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(data)
		contents.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return domain.Hash(contents.String())
}

func TestSecurityLegacyFounderAdoptionAfterOrderedMigration(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("sql-version-%d", version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(securityDirectory(t), "private", "legacy.sqlite")
			db, err := database.Open(ctx, path, []string{schema, autonomousSchema}[:version])
			if err != nil {
				t.Fatal(err)
			}
			founder := domain.Agent{ID: domain.NewID("agent"), Name: "original coordinator", Founder: true, Active: true,
				CreatedAt: 120, LastSeen: 130, Revision: 7, Mode: "manual"}
			worker := domain.Agent{ID: domain.NewID("agent"), Name: "Founder", Active: true, CreatedAt: 121, Revision: 3, Mode: "ai"}
			legacyToken := domain.NewToken()
			for _, agent := range []domain.Agent{founder, worker} {
				data, _ := encode(agent)
				credential := domain.Hash(domain.NewToken())
				if agent.Founder {
					credential = domain.Hash(legacyToken)
				}
				if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, credential); err != nil {
					t.Fatal(err)
				}
			}
			oldOperation := op().OperationID
			receipt, _ := resultReceipt(founder)
			if _, err := db.Exec("INSERT INTO operations(actor_id,operation_id,fingerprint,result) VALUES(?,?,?,?)", founder.ID, oldOperation, domain.Hash("retained original operation"), receipt); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var currentVersion int
			if err := store.db.QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil || currentVersion != 3 {
				t.Fatal("legacy schema did not migrate in order", currentVersion, err)
			}
			_, err = store.Identity(ctx, founder.ID)
			assertCode(t, err, domain.NotFound)
			keys := securityPublic(t, "fresh root-local adoption")
			bad := keys.public
			bad.AuthProof = "missing proof"
			_, err = store.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "root-local first-upgrade adoption", Identity: bad})
			assertCode(t, err, domain.Invalid)
			_, err = store.Identity(ctx, founder.ID)
			assertCode(t, err, domain.NotFound)
			req := domain.RecoverIdentityRequest{Command: op(), Reason: "root-local first-upgrade adoption", Identity: keys.public}
			identity, err := store.RecoverFounderOffline(ctx, req)
			if err != nil || identity.AgentID != founder.ID || identity.State != "pending" || identity.UID != -1 || identity.Generation != 1 {
				t.Fatal("root adoption did not bind the designated existing Founder", identity, err)
			}
			_, err = store.Authenticate(ctx, legacyToken)
			assertCode(t, err, domain.Denied)
			replay, err := store.RecoverFounderOffline(ctx, req)
			if err != nil || replay != identity {
				t.Fatal("root adoption replay changed identity", err)
			}
			identity, _ = securityActivate(t, store, identity, keys, 5100)
			active, err := store.Me(ctx, founder.ID)
			if err != nil || !active.Founder || !active.Active || active.Name != founder.Name || active.CreatedAt != founder.CreatedAt || active.Mode != "manual" {
				t.Fatal("root adoption replaced identity or role/history", active, err)
			}
			var count int
			if err := store.db.QueryRow("SELECT count(*) FROM agents WHERE json_extract(data,'$.founder')=1").Scan(&count); err != nil || count != 1 {
				t.Fatal("root adoption created a second Founder", count, err)
			}
			var fingerprint string
			if err := store.db.QueryRow("SELECT fingerprint FROM operations WHERE actor_id=? AND operation_id=?", founder.ID, oldOperation).Scan(&fingerprint); err != nil || fingerprint != domain.Hash("retained original operation") {
				t.Fatal("root adoption erased immutable operations", err)
			}
			if _, err := store.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "device"), keys.auth.Public()); err != nil {
				t.Fatal("fresh adopted Founder cannot authenticate", err)
			}
		})
	}
}

func TestSecurityRestoredSnapshotQuarantineRequiresFreshRecovery(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	founderKeys := securityPublic(t, "Founder fixture keys")
	founder, err := f.s.RegisterIdentity(ctx, f.founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: f.founder.ID, Identity: founderKeys.public})
	if err != nil {
		t.Fatal(err)
	}
	founder, _ = securityActivate(t, f.s, founder, founderKeys, 5100)
	workerKeys := securityPublic(t, "worker fixture keys")
	worker, err := f.s.RegisterIdentity(ctx, f.founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: f.worker.ID, Identity: workerKeys.public})
	if err != nil {
		t.Fatal(err)
	}
	worker, activation := securityActivate(t, f.s, worker, workerKeys, 5101)
	owner := securityPublic(t, "worker owner")
	worker, err = f.s.SetIdentityOwner(ctx, worker.AgentID, owner.public.AuthKey, worker.Generation)
	if err != nil {
		t.Fatal(err)
	}
	task := f.ready(t)
	if _, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	challenge, err := f.s.ChallengeIdentity(ctx, worker.UID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(workerKeys.auth, challenge)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(securityDirectory(t), "snapshot.sqlite")
	if err := f.s.Snapshot(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BanIdentity(ctx, f.founder.ID, "a post-backup ban must not be silently bypassed by restore"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	restored.now = f.s.now
	t.Cleanup(func() { restored.Close() })
	preserved := map[string]string{}
	for _, table := range []string{"tasks", "runs", "projects", "histories", "operations", "invitations", "identity_requests", "identity_receipts", "identity_accounts"} {
		preserved[table] = securityTableHash(t, restored.db, table)
	}
	if err := restored.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	for table, before := range preserved {
		if after := securityTableHash(t, restored.db, table); after != before {
			t.Fatal("restore quarantine changed retained evidence or work fences", table)
		}
	}
	managed, err := restored.ManagedIdentities(ctx)
	if err != nil || len(managed) != 3 {
		t.Fatal("restored legacy and registered identities were not all quarantined", managed, err)
	}
	for _, identity := range managed {
		agent, err := get[domain.Agent](ctx, restored.db, "agents", identity.AgentID)
		if err != nil || agent.Active || identity.State != identityRestorePending || identity.OwnerKey != "" {
			t.Fatal("restored authority remained provisionable or active", identity.AgentID, err)
		}
		if identity.AgentID == f.other.ID && (identity.UID != -1 || identity.SSHKey != "" || identity.AuthKey != "") {
			t.Fatal("legacy quarantine manufactured approved keys")
		}
	}
	quarantined, err := restored.Identity(ctx, worker.AgentID)
	if err != nil || quarantined.Generation != worker.Generation+1 || quarantined.OwnerGeneration != worker.OwnerGeneration+1 || quarantined.UID != worker.UID {
		t.Fatal("restore did not retire all epochs and retain account reservation", quarantined, err)
	}
	var retired, challenges int
	if err := restored.db.QueryRow("SELECT count(*) FROM identity_keys WHERE retired=1").Scan(&retired); err != nil || retired != 4 {
		t.Fatal("restored key tombstones disappeared", retired, err)
	}
	if err := restored.db.QueryRow("SELECT count(*) FROM identity_challenges").Scan(&challenges); err != nil || challenges != 0 {
		t.Fatal("restored activation challenges survived", challenges, err)
	}
	if err := restored.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	repeated, _ := restored.ManagedIdentities(ctx)
	for i := range managed {
		if managed[i] != repeated[i] {
			t.Fatal("repeated quarantine changed an already retired epoch")
		}
	}
	_, err = restored.Authenticate(ctx, f.founderToken)
	assertCode(t, err, domain.Denied)
	_, err = restored.Authenticate(ctx, f.workerToken)
	assertCode(t, err, domain.Denied)
	_, err = restored.ValidateCertificate(ctx, founder.UID, securityCertificate(founder, "device"), founderKeys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = restored.ValidateCertificate(ctx, worker.UID, securityCertificate(worker, "device"), workerKeys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = restored.ValidateCertificate(ctx, worker.UID, securityCertificate(worker, "owner"), owner.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = restored.Me(WithIdentityFence(ctx, securityCertificate(worker, "owner")), worker.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = restored.ChallengeIdentity(ctx, worker.UID)
	assertCode(t, err, domain.Denied)
	_, err = restored.ActivateIdentity(ctx, worker.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: proof})
	assertCode(t, err, domain.Denied)
	_, err = restored.ActivateIdentity(ctx, worker.UID, activation)
	assertCode(t, err, domain.Denied)
	_, err = restored.BindIdentityAccount(ctx, worker.AgentID, worker.SSHUser, worker.UID, worker.SSHFingerprint)
	assertCode(t, err, domain.Denied)
	_, err = restored.SetIdentityOwner(ctx, worker.AgentID, owner.public.AuthKey, worker.Generation)
	assertCode(t, err, domain.Denied)
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	restored.now = f.s.now
	persisted, err := restored.Identity(ctx, worker.AgentID)
	if err != nil || persisted != quarantined {
		t.Fatal("quarantine did not survive reopening", persisted, err)
	}
	freshFounder := securityPublic(t, "fresh restored Founder")
	_, err = restored.RecoverIdentity(ctx, founder.AgentID, worker.AgentID, domain.RecoverIdentityRequest{Command: op(), Reason: "inactive Founder cannot approve restore recovery", Identity: freshFounder.public})
	assertCode(t, err, domain.Denied)
	_, err = restored.BootstrapIdentity(ctx, freshFounder.public, op().OperationID)
	assertCode(t, err, domain.Conflict)
	req := domain.RecoverIdentityRequest{Command: op(), Reason: "root-local explicit recovery after restore", Identity: freshFounder.public}
	recoveredFounder, err := restored.RecoverFounderOffline(ctx, req)
	if err != nil || recoveredFounder.AgentID != founder.AgentID || recoveredFounder.Generation != founder.Generation+2 || recoveredFounder.State != "pending" || recoveredFounder.UID != -1 {
		t.Fatal("restore recovery reset or automatically activated Founder", recoveredFounder, err)
	}
	recoveredFounder, _ = securityActivate(t, restored, recoveredFounder, freshFounder, founder.UID)
	founderCtx := securityOwnerContext(t, restored, recoveredFounder)
	freshWorker := securityPublic(t, "fresh restored worker")
	recoveredWorker, err := restored.RecoverIdentity(founderCtx, founder.AgentID, worker.AgentID, domain.RecoverIdentityRequest{Command: op(), Reason: "Founder-approved fresh-key restore recovery", Identity: freshWorker.public})
	if err != nil || recoveredWorker.AgentID != worker.AgentID || recoveredWorker.State != "pending" || recoveredWorker.UID != -1 {
		t.Fatal("Founder could not recover quarantined worker", recoveredWorker, err)
	}
	recoveredWorker, _ = securityActivate(t, restored, recoveredWorker, freshWorker, worker.UID)
	freshLegacy := securityPublic(t, "fresh legacy migration")
	migrated, err := restored.RegisterIdentity(founderCtx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: f.other.ID, Identity: freshLegacy.public})
	if err != nil || migrated.AgentID != f.other.ID || migrated.State != "pending" || migrated.UID != -1 {
		t.Fatal("explicit Founder migration rejected quarantined legacy agent", migrated, err)
	}
	migrated, _ = securityActivate(t, restored, migrated, freshLegacy, 5102)
	for _, identity := range []domain.AgentIdentity{recoveredFounder, recoveredWorker, migrated} {
		agent, err := restored.Me(ctx, identity.AgentID)
		if err != nil || !agent.Active || agent.Founder != (identity.AgentID == founder.AgentID) {
			t.Fatal("fresh restore recovery changed role or failed to activate", err)
		}
	}
	_, err = restored.ValidateCertificate(ctx, worker.UID, securityCertificate(worker, "device"), workerKeys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = restored.ValidateCertificate(ctx, founder.UID, securityCertificate(founder, "device"), founderKeys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = restored.ActivateIdentity(ctx, worker.UID, activation)
	assertCode(t, err, domain.Conflict)
	if err := restored.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityRestoredRegisteredMigrationPreservesBansAndRetiredKeys(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	worker, keys, _ := f.register(t, "worker", 5101)
	banned, _, _ := f.register(t, "banned worker", 5102)
	if err := f.s.BanIdentity(ctx, banned.AgentID, "retained explicit ban"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	freshFounder := securityPublic(t, "fresh Founder")
	founder, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "root-local restore recovery", Identity: freshFounder.public})
	if err != nil {
		t.Fatal(err)
	}
	founder, _ = securityActivate(t, f.s, founder, freshFounder, f.founder.UID)
	founderCtx := securityOwnerContext(t, f.s, founder)
	_, err = f.s.RegisterIdentity(founderCtx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: worker.AgentID, Identity: keys.public})
	assertCode(t, err, domain.Conflict)
	fresh := securityPublic(t, "fresh worker migration")
	worker, err = f.s.RegisterIdentity(founderCtx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: worker.AgentID, Identity: fresh.public})
	if err != nil || worker.State != "pending" || worker.UID != -1 || worker.Generation != 3 {
		t.Fatal("registered restore migration did not require fresh pending keys", worker, err)
	}
	_, err = f.s.RegisterIdentity(founderCtx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: banned.AgentID, Identity: securityPublic(t, "ban bypass").public})
	assertCode(t, err, domain.Denied)
	retained, err := get[domain.Agent](ctx, f.s.db, "agents", banned.AgentID)
	if err != nil || !retained.Banned || retained.Active || retained.BanReason != "retained explicit ban" {
		t.Fatal("restore or migration cleared an explicit ban", retained, err)
	}
}

func TestSecurityRestoreQuarantineRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	worker, keys, _ := f.register(t, "worker", 5101)
	if _, err := f.s.ChallengeIdentity(ctx, worker.UID); err != nil {
		t.Fatal(err)
	}
	preserved := map[string]string{}
	for _, table := range []string{"agents", "agent_identities", "identity_keys", "identity_challenges", "events", "audit", "operations"} {
		preserved[table] = securityTableHash(t, f.s.db, table)
	}
	if _, err := f.s.db.Exec(`CREATE TRIGGER fixture_refuse_key_quarantine BEFORE UPDATE ON identity_keys
BEGIN SELECT RAISE(ABORT,'fixture quarantine failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.QuarantineRestoredIdentities(ctx); err == nil {
		t.Fatal("quarantine ignored a failed key retirement")
	}
	for table, before := range preserved {
		if securityTableHash(t, f.s.db, table) != before {
			t.Fatal("failed restore quarantine partially changed authoritative state", table)
		}
	}
	if _, err := f.s.ValidateCertificate(ctx, worker.UID, securityCertificate(worker, "device"), keys.auth.Public()); err != nil {
		t.Fatal("failed quarantine changed certificate authority", err)
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_refuse_key_quarantine"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal("quarantine could not be retried after storage failure", err)
	}
}

func TestSecurityLegacySnapshotQuarantineRequiresFounderApprovedMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "legacy-snapshot.sqlite")
	db, err := database.Open(ctx, path, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	founder := domain.Agent{ID: domain.NewID("agent"), Name: "retained Founder", Founder: true, Active: true, CreatedAt: 101}
	worker := domain.Agent{ID: domain.NewID("agent"), Name: "retained worker", Active: true, CreatedAt: 102}
	tokens := map[string]string{}
	for _, agent := range []domain.Agent{founder, worker} {
		data, _ := encode(agent)
		tokens[agent.ID] = domain.NewToken()
		if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, domain.Hash(tokens[agent.ID])); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	managed, err := store.ManagedIdentities(ctx)
	if err != nil || len(managed) != 2 {
		t.Fatal("legacy restored agents have no explicit quarantine state", err)
	}
	for _, identity := range managed {
		if identity.State != identityRestorePending || identity.UID != -1 || identity.SSHKey != "" || identity.AuthKey != "" {
			t.Fatal("keyless legacy snapshot became automatically provisionable", identity.AgentID)
		}
		_, err := store.Authenticate(ctx, tokens[identity.AgentID])
		assertCode(t, err, domain.Denied)
	}
	keys := securityPublic(t, "fresh root-maintenance keys")
	identity, err := store.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "explicit legacy restore adoption", Identity: keys.public})
	if err != nil || identity.AgentID != founder.ID || identity.State != "pending" || identity.UID != -1 {
		t.Fatal("quarantined keyless Founder could not be adopted", identity, err)
	}
	identity, _ = securityActivate(t, store, identity, keys, 5200)
	founderCtx := securityOwnerContext(t, store, identity)
	_, err = store.Authenticate(ctx, tokens[worker.ID])
	assertCode(t, err, domain.Denied)
	freshWorker := securityPublic(t, "Founder-approved restored worker")
	migrated, err := store.RegisterIdentity(founderCtx, founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: worker.ID, Identity: freshWorker.public})
	if err != nil || migrated.AgentID != worker.ID || migrated.State != "pending" || migrated.UID != -1 {
		t.Fatal("explicit legacy migration did not remain pending", migrated, err)
	}
	_, err = store.Authenticate(ctx, tokens[worker.ID])
	assertCode(t, err, domain.Denied)
	migrated, _ = securityActivate(t, store, migrated, freshWorker, 5201)
	active, err := store.Me(ctx, worker.ID)
	if err != nil || !active.Active || active.Founder || active.Name != worker.Name || active.CreatedAt != worker.CreatedAt {
		t.Fatal("approved migration replaced a legacy identity or its role/history", active, err)
	}
	if _, err := store.ValidateCertificate(ctx, migrated.UID, securityCertificate(migrated, "device"), freshWorker.auth.Public()); err != nil {
		t.Fatal(err)
	}
}
