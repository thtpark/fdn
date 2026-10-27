package foundation

import (
	"context"
	"crypto"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

type securityKeys struct {
	public domain.PublicIdentity
	auth   crypto.Signer
}

func securityPublic(t *testing.T, name string) securityKeys {
	t.Helper()
	public, _, private, err := device.Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := device.ParseSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	return securityKeys{public, signer}
}

func securityDirectory(t *testing.T) string {
	t.Helper()
	path := ".security-fixture-" + domain.NewID("state")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	return path
}

type securityFixture struct {
	s       *Store
	path    string
	now     time.Time
	founder domain.AgentIdentity
	keys    securityKeys
}

func securityEmptyStore(t *testing.T) *securityFixture {
	t.Helper()
	f := &securityFixture{path: filepath.Join(securityDirectory(t), "private", "foundation.sqlite"),
		now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	var err error
	f.s, err = Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	t.Cleanup(func() {
		if err := f.s.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func newSecurityFixture(t *testing.T) *securityFixture {
	t.Helper()
	f := securityEmptyStore(t)
	f.keys = securityPublic(t, "Founder")
	var err error
	f.founder, err = f.s.BootstrapIdentity(context.Background(), f.keys.public, op().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	f.founder, _ = securityActivate(t, f.s, f.founder, f.keys, 4100)
	return f
}

func securityActivate(t *testing.T, store *Store, identity domain.AgentIdentity, keys securityKeys, uid int64) (domain.AgentIdentity, domain.ActivateIdentityRequest) {
	t.Helper()
	ctx := context.Background()
	identity, err := store.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, uid, identity.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := store.ChallengeIdentity(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(keys.auth, challenge)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: proof}
	identity, err = store.ActivateIdentity(ctx, uid, req)
	if err != nil {
		t.Fatal(err)
	}
	return identity, req
}

func (f *securityFixture) register(t *testing.T, name string, uid int64) (domain.AgentIdentity, securityKeys, domain.ActivateIdentityRequest) {
	t.Helper()
	keys := securityPublic(t, name)
	identity, err := f.s.RegisterIdentity(context.Background(), f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: keys.public})
	if err != nil {
		t.Fatal(err)
	}
	identity, activation := securityActivate(t, f.s, identity, keys, uid)
	return identity, keys, activation
}

func securityCertificate(identity domain.AgentIdentity, purpose string) domain.CertificateIdentity {
	ci := domain.CertificateIdentity{InstanceID: identity.InstanceID, AgentID: identity.AgentID, Generation: identity.Generation, Purpose: purpose}
	if purpose == "owner" {
		ci.OwnerGeneration = identity.OwnerGeneration
	}
	return ci
}

func TestSecurityBootstrapRegistrationAndFounderOnlyPolicy(t *testing.T) {
	ctx := context.Background()
	f := securityEmptyStore(t)
	instance, err := f.s.InstanceID(ctx)
	if err != nil || instance == "" {
		t.Fatal("missing instance", err)
	}
	keys := securityPublic(t, "Founder")
	operation := op().OperationID
	founder, err := f.s.BootstrapIdentity(ctx, keys.public, operation)
	if err != nil || founder.UID != -1 || founder.State != "pending" || founder.Assurance != "software" || founder.InstanceID != instance {
		t.Fatal("invalid pending bootstrap", founder, err)
	}
	if founder.SSHUser != "founder" {
		t.Fatal("unexpected dedicated account name", founder.SSHUser)
	}
	for _, replayOp := range []string{operation, op().OperationID} {
		replay, err := f.s.BootstrapIdentity(ctx, keys.public, replayOp)
		if err != nil || replay != founder {
			t.Fatal("bootstrap did not replay exact original identity", replay, err)
		}
	}
	changed := keys.public
	changed.Name = "different"
	_, err = f.s.BootstrapIdentity(ctx, changed, operation)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.BootstrapIdentity(ctx, securityPublic(t, "second").public, op().OperationID)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.Me(ctx, founder.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.IdentityByUID(ctx, -1)
	assertCode(t, err, domain.Denied)
	for _, bad := range []struct {
		user string
		uid  int64
		key  string
	}{{"root", 4100, founder.SSHFingerprint}, {founder.SSHUser, 0, founder.SSHFingerprint}, {founder.SSHUser, 4100, "wrong"}} {
		_, err = f.s.BindIdentityAccount(ctx, founder.AgentID, bad.user, bad.uid, bad.key)
		assertCode(t, err, domain.Invalid)
	}
	founder, _ = securityActivate(t, f.s, founder, keys, 4100)
	founderAgent, err := f.s.Me(ctx, founder.AgentID)
	if err != nil || !founderAgent.Founder || !founderAgent.Active {
		t.Fatal("Founder did not become active", err)
	}
	var placeholder string
	if err := f.s.db.QueryRow("SELECT credential_hash FROM agents WHERE id=?", founder.AgentID).Scan(&placeholder); err != nil || !domain.ValidHash(placeholder) {
		t.Fatal("missing unique unusable placeholder", err)
	}
	_, err = f.s.Authenticate(ctx, placeholder)
	assertCode(t, err, domain.Denied)
	workerKeys := securityPublic(t, "worker")
	req := domain.RegisterIdentityRequest{Command: op(), Identity: workerKeys.public}
	worker, err := f.s.RegisterIdentity(ctx, founder.AgentID, req)
	if err != nil || worker.UID != -1 {
		t.Fatal("pending identity registration", err)
	}
	replay, err := f.s.RegisterIdentity(ctx, founder.AgentID, req)
	if err != nil || replay.AgentID != worker.AgentID {
		t.Fatal("registration operation replay", err)
	}
	req.Command = op()
	replay, err = f.s.RegisterIdentity(ctx, founder.AgentID, req)
	if err != nil || replay.AgentID != worker.AgentID {
		t.Fatal("public request replay", err)
	}
	req.Identity.SSHKey = "  " + strings.TrimSpace(req.Identity.SSHKey) + " local alias\n"
	_, err = f.s.RegisterIdentity(ctx, founder.AgentID, req)
	assertCode(t, err, domain.Conflict)
	req.Command = op()
	_, err = f.s.RegisterIdentity(ctx, founder.AgentID, req)
	assertCode(t, err, domain.Conflict)
	worker, _ = securityActivate(t, f.s, worker, workerKeys, 4101)
	project, err := f.s.CreateProject(ctx, founder.AgentID, domain.ProjectRequest{Command: op(), Name: "delegated"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Delegate(ctx, founder.AgentID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: worker.AgentID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RegisterIdentity(ctx, worker.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: securityPublic(t, "unauthorized").public})
	assertCode(t, err, domain.Denied)
	_, err = f.s.Invite(ctx, worker.AgentID, domain.InvitationRequest{Command: op(), TokenHash: domain.Hash(domain.NewToken()), ExpiresAt: f.now.Unix() + 60})
	assertCode(t, err, domain.Denied)
	pendingKeys := securityPublic(t, "another pending")
	if _, err := f.s.RegisterIdentity(ctx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: pendingKeys.public}); err != nil {
		t.Fatal("pending UID sentinel was not repeatable", err)
	}
	managed, err := f.s.ManagedIdentities(ctx)
	if err != nil || len(managed) != 3 {
		t.Fatal("local reconciliation registry", len(managed), err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := f.s.InstanceID(ctx)
	if err != nil || reopened != instance {
		t.Fatal("instance changed on reopen", reopened, err)
	}
	replay, err = f.s.BootstrapIdentity(ctx, keys.public, operation)
	if err != nil || replay.AgentID != founder.AgentID || replay.State != "active" {
		t.Fatal("bootstrap replay lost across reopen", err)
	}
	var largest int
	if err := f.s.db.QueryRow("SELECT max(length(result)) FROM operations").Scan(&largest); err != nil || largest > 512 {
		t.Fatal("identity operation receipts copied payloads", largest, err)
	}
}

func TestSecurityChallengeProofFreshnessAndExactActivationReplay(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	keys := securityPublic(t, "worker")
	identity, err := f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: keys.public})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ChallengeIdentity(ctx, 4101)
	assertCode(t, err, domain.Denied)
	identity, err = f.s.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, 4101, identity.SSHFingerprint)
	if err != nil || identity.State != "provisioned" {
		t.Fatal(err)
	}
	old, err := f.s.ChallengeIdentity(ctx, identity.UID)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := f.s.ChallengeIdentity(ctx, identity.UID)
	if err != nil || old.Nonce == challenge.Nonce {
		t.Fatal("challenge was not fresh", err)
	}
	oldProof, _ := device.SignChallenge(keys.auth, old)
	_, err = f.s.ActivateIdentity(ctx, identity.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: old.Nonce, Proof: oldProof})
	assertCode(t, err, domain.Denied)
	for _, field := range []string{"instance", "agent", "generation", "nonce", "expiry", "key"} {
		altered, signer := challenge, keys.auth
		switch field {
		case "instance":
			altered.InstanceID = domain.NewID("instance")
		case "agent":
			altered.AgentID = domain.NewID("agent")
		case "generation":
			altered.Generation++
		case "nonce":
			altered.Nonce = domain.NewToken()
		case "expiry":
			altered.ExpiresAt++
		case "key":
			signer = f.keys.auth
		}
		proof, err := device.SignChallenge(signer, altered)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.s.ActivateIdentity(ctx, identity.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: proof})
		assertCode(t, err, domain.Denied)
	}
	proof, _ := device.SignChallenge(keys.auth, challenge)
	req := domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: proof}
	active, err := f.s.ActivateIdentity(ctx, identity.UID, req)
	if err != nil || active.State != "active" || active.Generation != identity.Generation {
		t.Fatal("valid proof did not activate", err)
	}
	f.now = f.now.Add(time.Hour)
	replay, err := f.s.ActivateIdentity(ctx, identity.UID, req)
	if err != nil || replay != active {
		t.Fatal("exact activation replay did not survive consumed/expired challenge", err)
	}
	other := req
	other.Command = op()
	_, err = f.s.ActivateIdentity(ctx, identity.UID, other)
	assertCode(t, err, domain.Denied)
	other = req
	other.Proof = "different"
	_, err = f.s.ActivateIdentity(ctx, identity.UID, other)
	assertCode(t, err, domain.Conflict)
	challenge, err = f.s.ChallengeIdentity(ctx, identity.UID)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ = device.SignChallenge(keys.auth, challenge)
	f.now = f.now.Add(time.Duration(identityChallengeLifetime) * time.Second)
	_, err = f.s.ActivateIdentity(ctx, identity.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: proof})
	assertCode(t, err, domain.Denied)
	renewed, _ := securityActivate(t, f.s, active, keys, identity.UID)
	if renewed.Generation != active.Generation || renewed.State != "active" {
		t.Fatal("active identity renewal changed generation")
	}
	agent, err := f.s.Me(ctx, identity.AgentID)
	if err != nil || agent.Banned {
		t.Fatal("invalid proofs or retries caused a ban", err)
	}
}

func TestSecurityCertificatePurposeAndOwnerGeneration(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	identity, keys, _ := f.register(t, "worker", 4101)
	deviceCert := securityCertificate(identity, "device")
	if _, err := f.s.ValidateCertificate(ctx, identity.UID, deviceCert, keys.auth.Public()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instance", "agent", "generation", "purpose", "owner", "account", "key"} {
		ci, uid, public := deviceCert, identity.UID, keys.auth.Public()
		switch field {
		case "instance":
			ci.InstanceID = domain.NewID("instance")
		case "agent":
			ci.AgentID = f.founder.AgentID
		case "generation":
			ci.Generation++
		case "purpose":
			ci.Purpose = "unknown"
		case "owner":
			ci.OwnerGeneration = 1
		case "account":
			uid = f.founder.UID
		case "key":
			public = f.keys.auth.Public()
		}
		_, err := f.s.ValidateCertificate(ctx, uid, ci, public)
		assertCode(t, err, domain.Denied)
	}
	owner := securityPublic(t, "ephemeral owner")
	identity, err := f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	if err != nil || identity.OwnerGeneration != 1 {
		t.Fatal(err)
	}
	firstCert := securityCertificate(identity, "owner")
	if _, err := f.s.ValidateCertificate(ctx, identity.UID, firstCert, owner.auth.Public()); err != nil {
		t.Fatal(err)
	}
	reconnected, err := f.s.SetIdentityOwner(ctx, identity.AgentID, " \n"+owner.public.AuthKey+"\t", identity.Generation)
	if err != nil || reconnected.OwnerGeneration != identity.OwnerGeneration {
		t.Fatal("same-key reconnect fenced itself", err)
	}
	secondOwner := securityPublic(t, "replacement owner")
	replaced, err := f.s.SetIdentityOwner(ctx, identity.AgentID, secondOwner.public.AuthKey, identity.Generation)
	if err != nil || replaced.OwnerGeneration != identity.OwnerGeneration+1 {
		t.Fatal("new owner did not advance generation", err)
	}
	_, err = f.s.ValidateCertificate(ctx, identity.UID, firstCert, owner.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(replaced, "owner"), keys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation+1)
	assertCode(t, err, domain.Denied)
	_, err = f.s.SetIdentityOwner(ctx, identity.AgentID, keys.public.AuthKey, identity.Generation)
	assertCode(t, err, domain.Invalid)
	if _, err := f.s.ValidateCertificate(ctx, identity.UID, deviceCert, keys.auth.Public()); err != nil {
		t.Fatal("owner replacement retired device authentication", err)
	}
}

func TestSecurityUIDRequestAndCanonicalKeyRaces(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	second, err := Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.now = f.s.now
	keys := securityPublic(t, "one public request")
	type registered struct {
		identity domain.AgentIdentity
		err      error
	}
	registrations := make(chan registered, 8)
	var writers sync.WaitGroup
	for i := range 8 {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			store := []*Store{f.s, second}[i%2]
			identity, err := store.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: keys.public})
			registrations <- registered{identity, err}
		}(i)
	}
	writers.Wait()
	close(registrations)
	var first domain.AgentIdentity
	for result := range registrations {
		if result.err != nil {
			t.Fatal("concurrent exact public request failed", result.err)
		}
		if first.AgentID == "" {
			first = result.identity
		}
		if result.identity.AgentID != first.AgentID {
			t.Fatal("same public request created two identities")
		}
	}
	otherKeys := securityPublic(t, "another account")
	other, err := f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: otherKeys.public})
	if err != nil {
		t.Fatal(err)
	}
	bindings := make(chan error, 2)
	for i, identity := range []domain.AgentIdentity{first, other} {
		writers.Add(1)
		go func(store *Store, identity domain.AgentIdentity) {
			defer writers.Done()
			_, err := store.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, 4200, identity.SSHFingerprint)
			bindings <- err
		}([]*Store{f.s, second}[i], identity)
	}
	writers.Wait()
	close(bindings)
	accepted, rejected := 0, 0
	for err := range bindings {
		if err == nil {
			accepted++
		} else {
			assertCode(t, err, domain.Conflict)
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatal("UID collision did not serialize", accepted, rejected)
	}
	collision := securityPublic(t, "retired cross-purpose key")
	authPublic, err := device.PublicKey(collision.public.AuthKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := device.Fingerprint(authPublic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec("INSERT INTO identity_keys(fingerprint,agent_id,generation,kind,retired) VALUES(?,?,1,'ssh',1)", fingerprint, first.AgentID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: collision.public})
	assertCode(t, err, domain.Conflict)
	collision = securityPublic(t, "retired SSH key alias")
	sshPublic, err := device.SSHPublicKey(collision.public.SSHKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err = device.Fingerprint(sshPublic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec("INSERT INTO identity_keys(fingerprint,agent_id,generation,kind,retired) VALUES(?,?,1,'auth',1)", fingerprint, first.AgentID); err != nil {
		t.Fatal(err)
	}
	collision.public.SSHKey = "  " + strings.TrimSpace(collision.public.SSHKey) + " alias comment\n"
	_, err = f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: collision.public})
	assertCode(t, err, domain.Conflict)
	for _, identity := range []domain.AgentIdentity{f.founder, first, other} {
		agent, err := get[domain.Agent](ctx, f.s.db, "agents", identity.AgentID)
		if err != nil || agent.Banned {
			t.Fatal("key/UID collision caused a ban", err)
		}
	}
	var pending int
	if err := f.s.db.QueryRow("SELECT count(*) FROM agent_identities WHERE uid=-1").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("failed collisions left partially registered agents", pending, err)
	}
	_, err = f.s.db.Exec("UPDATE agent_identities SET uid=4200 WHERE uid=-1")
	if err == nil {
		t.Fatal("registry lacked a database-level partial unique UID index")
	}
}

func TestSecurityMigrationPreservesVersionTwoRecordsAndExplicitMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "foundation.sqlite")
	db, err := database.Open(ctx, path, []string{schema, autonomousSchema})
	if err != nil {
		t.Fatal(err)
	}
	founder := domain.Agent{ID: domain.NewID("agent"), Name: "retained Founder", Founder: true, Active: true, Revision: 17, Mode: "manual", CreatedAt: 123, LastSeen: 456}
	worker := domain.Agent{ID: domain.NewID("agent"), Name: "retained worker", Active: true, Revision: 13, Mode: "ai", CreatedAt: 124, LastSeen: 457}
	founderToken, workerToken := domain.NewToken(), domain.NewToken()
	oldData := map[string]string{}
	for _, row := range []struct {
		agent domain.Agent
		token string
	}{{founder, founderToken}, {worker, workerToken}} {
		data, _ := json.Marshal(row.agent)
		oldData[row.agent.ID] = string(data)
		if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", row.agent.ID, data, domain.Hash(row.token)); err != nil {
			t.Fatal(err)
		}
	}
	project := domain.Project{ID: domain.NewID("project"), Name: "preserved project", OwnerID: founder.ID, Delegates: []string{founder.ID}, Revision: 4, Epoch: 9}
	projectData, _ := json.Marshal(project)
	if _, err := db.Exec("INSERT INTO projects(id,data) VALUES(?,?)", project.ID, projectData); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO histories(source_id,actor_id,action,reason,created_at,snapshot) VALUES(?,?,'old','retained rationale',123,?)", project.ID, founder.ID, projectData); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO settings(key,value) VALUES('retained-setting','retained-value')"); err != nil {
		t.Fatal(err)
	}
	originalOp := op().OperationID
	receipt, _ := resultReceipt(founder)
	if _, err := db.Exec("INSERT INTO operations(actor_id,operation_id,fingerprint,result) VALUES(?,?,?,?)", founder.ID, originalOp, domain.Hash("old immutable proposal"), receipt); err != nil {
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
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("server did not append migrations three and four", version, err)
	}
	for id, previous := range oldData {
		var data string
		if err := store.db.QueryRow("SELECT data FROM agents WHERE id=?", id).Scan(&data); err != nil || data != previous {
			t.Fatal("migration rewrote operational identity", id, err)
		}
	}
	var retained, fingerprint string
	if err := store.db.QueryRow("SELECT value FROM settings WHERE key='retained-setting'").Scan(&retained); err != nil || retained != "retained-value" {
		t.Fatal("migration erased service settings", err)
	}
	if err := store.db.QueryRow("SELECT fingerprint FROM operations WHERE actor_id=? AND operation_id=?", founder.ID, originalOp).Scan(&fingerprint); err != nil || fingerprint != domain.Hash("old immutable proposal") {
		t.Fatal("migration erased operation fingerprints", err)
	}
	if _, err := store.Authenticate(ctx, workerToken); err != nil {
		t.Fatal("legacy fixture helper records did not survive", err)
	}
	_, err = store.BootstrapIdentity(ctx, securityPublic(t, "new Founder").public, op().OperationID)
	assertCode(t, err, domain.Conflict)
	keys := securityPublic(t, "verified migration request")
	migrated, err := store.RegisterIdentity(ctx, founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: worker.ID, Identity: keys.public})
	if err != nil || migrated.AgentID != worker.ID || migrated.State != "pending" || migrated.UID != -1 {
		t.Fatal("explicit verified migration did not bind retained agent", migrated, err)
	}
	var credential string
	if err := store.db.QueryRow("SELECT credential_hash FROM agents WHERE id=?", worker.ID).Scan(&credential); err != nil || credential != domain.Hash(workerToken) {
		t.Fatal("explicit migration replaced legacy fixture credentials", err)
	}
	migrated, _ = securityActivate(t, store, migrated, keys, 4301)
	got, err := store.Me(ctx, worker.ID)
	if err != nil || got.Name != worker.Name || got.CreatedAt != worker.CreatedAt || got.Mode != worker.Mode || got.Founder {
		t.Fatal("migration changed retained agent properties", got, err)
	}
	if _, err := store.Project(ctx, worker.ID, project.ID); err != nil {
		t.Fatal("migration lost application state", err)
	}
	_, err = store.RegisterIdentity(ctx, founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: worker.ID, Identity: securityPublic(t, "migration is not recovery").public})
	assertCode(t, err, domain.Conflict)
	var historyReason string
	if err := store.db.QueryRow("SELECT reason FROM histories WHERE source_id=?", project.ID).Scan(&historyReason); err != nil || historyReason != "retained rationale" {
		t.Fatal("migration erased history", err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityOfflineRecoveryOfLegacyFounderAndFutureSchema(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if err := f.s.BanIdentity(ctx, f.founder.ID, "explicit legacy Founder ban"); err != nil {
		t.Fatal(err)
	}
	keys := securityPublic(t, "offline migration")
	identity, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "explicit offline recovery of retained Founder", Identity: keys.public})
	if err != nil || identity.AgentID != f.founder.ID || identity.State != "pending" {
		t.Fatal("legacy Founder recovery created another identity", identity, err)
	}
	identity, _ = securityActivate(t, f.s, identity, keys, 4400)
	agent, err := f.s.Me(ctx, identity.AgentID)
	if err != nil || agent.CreatedAt != f.founder.CreatedAt || agent.Name != f.founder.Name || !agent.Founder {
		t.Fatal("legacy Founder recovery lost provenance", agent, err)
	}
	path := filepath.Join(securityDirectory(t), "private", "future.sqlite")
	db, err := database.Open(ctx, path, []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, "SELECT 1;"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if store != nil {
		store.Close()
		t.Fatal("future schema was opened")
	}
	assertCode(t, err, domain.Prerequisite)
	probe, err := database.Open(ctx, path, []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, "SELECT 1;"})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	var version int
	if err := probe.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatal("future schema refusal modified retained database", version, err)
	}
}
