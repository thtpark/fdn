package foundation

import (
	"context"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestSecurityBanRecoveryAndRetiredGenerations(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	identity, keys, activation := f.register(t, "worker", 4101)
	owner := securityPublic(t, "owner")
	identity, err := f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	if err != nil {
		t.Fatal(err)
	}
	deviceCert, ownerCert := securityCertificate(identity, "device"), securityCertificate(identity, "owner")
	for _, generations := range [][2]int64{{identity.Generation + 1, identity.OwnerGeneration}, {identity.Generation, identity.OwnerGeneration + 1}} {
		err := f.s.BanDuplicateIdentity(ctx, identity.AgentID, generations[0], generations[1], "confirmed independent controllers")
		assertCode(t, err, domain.Conflict)
	}
	active, err := f.s.Me(ctx, identity.AgentID)
	if err != nil || active.Banned {
		t.Fatal("stale duplicate evidence banned identity", err)
	}
	if err := f.s.BanDuplicateIdentity(ctx, identity.AgentID, identity.Generation, identity.OwnerGeneration, "confirmed independent controllers"); err != nil {
		t.Fatal(err)
	}
	banned, err := f.s.Identity(ctx, identity.AgentID)
	if err != nil || banned.State != "banned" || banned.Generation != identity.Generation+1 || banned.OwnerGeneration != identity.OwnerGeneration+1 || banned.OwnerKey != "" {
		t.Fatal("identity ban did not fence all epochs", banned, err)
	}
	for _, certificate := range []struct {
		ci  domain.CertificateIdentity
		key any
	}{{deviceCert, keys.auth.Public()}, {ownerCert, owner.auth.Public()}} {
		_, err := f.s.ValidateCertificate(ctx, identity.UID, certificate.ci, certificate.key)
		assertCode(t, err, domain.Denied)
	}
	_, err = f.s.Me(ctx, identity.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.ChallengeIdentity(ctx, identity.UID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.ActivateIdentity(ctx, identity.UID, activation)
	assertCode(t, err, domain.Denied)
	_, err = f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RecoverIdentity(ctx, identity.AgentID, identity.AgentID, domain.RecoverIdentityRequest{Command: op(), Reason: "unauthorized self recovery", Identity: securityPublic(t, "bypass").public})
	assertCode(t, err, domain.Denied)
	if err := f.s.BanIdentity(ctx, identity.AgentID, "a retry must not change the original ban"); err != nil {
		t.Fatal(err)
	}
	repeated, _ := f.s.Identity(ctx, identity.AgentID)
	if repeated != banned {
		t.Fatal("ban retry changed an identity epoch or reason")
	}
	inbox, err := f.s.Inbox(ctx, f.founder.AgentID, "", 20, true)
	if err != nil || len(inbox.Items) != 1 || inbox.Items[0].Origin != "service" || inbox.Items[0].Kind != "authentication" || inbox.Items[0].ResponseRequest || !strings.Contains(inbox.Items[0].Body, identity.AgentID) || strings.Contains(inbox.Items[0].Body, "KEY") {
		t.Fatal("private Founder security notification", inbox, err)
	}
	var accepted int
	if err := f.s.db.QueryRow("SELECT count(*) FROM audit WHERE actor_id=? AND action='identity.ban' AND outcome='accepted'", identity.AgentID).Scan(&accepted); err != nil || accepted != 1 {
		t.Fatal("ban did not commit independently of subsequent denial", accepted, err)
	}
	fresh := securityPublic(t, "replacement worker")
	req := domain.RecoverIdentityRequest{Command: op(), Reason: "explicit recovery after investigation", Identity: fresh.public}
	_, err = f.s.RecoverIdentity(ctx, identity.AgentID, identity.AgentID, req)
	assertCode(t, err, domain.Denied)
	recovered, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, identity.AgentID, req)
	if err != nil || recovered.AgentID != identity.AgentID || recovered.State != "pending" || recovered.UID != -1 || recovered.Generation != banned.Generation+1 || recovered.OwnerKey != "" || recovered.BanReason != "" {
		t.Fatal("explicit recovery did not preserve identity and require reprovisioning", recovered, err)
	}
	replay, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, identity.AgentID, req)
	if err != nil || replay != recovered {
		t.Fatal("recovery replay advanced generation", err)
	}
	changed := req
	changed.Reason = "different"
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, identity.AgentID, changed)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, 4102, recovered.SSHFingerprint)
	assertCode(t, err, domain.Conflict)
	otherKeys := securityPublic(t, "other")
	other, err := f.s.RegisterIdentity(ctx, f.founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: otherKeys.public})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.BindIdentityAccount(ctx, other.AgentID, other.SSHUser, identity.UID, other.SSHFingerprint)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.ChallengeIdentity(ctx, identity.UID)
	assertCode(t, err, domain.Denied)
	recovered, _ = securityActivate(t, f.s, recovered, fresh, identity.UID)
	_, err = f.s.ValidateCertificate(ctx, identity.UID, deviceCert, keys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ValidateCertificate(ctx, identity.UID, ownerCert, owner.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ActivateIdentity(ctx, identity.UID, activation)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	assertCode(t, err, domain.Denied)
	if err := f.s.BanDuplicateIdentity(ctx, identity.AgentID, identity.Generation, identity.OwnerGeneration, "stale pre-recovery duplicate evidence"); err == nil {
		t.Fatal("stale duplicate evidence banned the recovered identity")
	} else {
		assertCode(t, err, domain.Conflict)
	}
	if _, err := f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(recovered, "device"), fresh.auth.Public()); err != nil {
		t.Fatal(err)
	}
	var retired, current int
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE agent_id=? AND retired=1", identity.AgentID).Scan(&retired); err != nil || retired != 2 {
		t.Fatal("old key tombstones disappeared", retired, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE agent_id=? AND retired=0", identity.AgentID).Scan(&current); err != nil || current != 2 {
		t.Fatal("fresh identity keys not reserved", current, err)
	}
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, identity.AgentID, domain.RecoverIdentityRequest{Command: op(), Reason: "cannot reuse original request and keys", Identity: keys.public})
	assertCode(t, err, domain.Conflict)
	retained, _ := f.s.Identity(ctx, identity.AgentID)
	if retained != recovered {
		t.Fatal("failed recovery changed current identity")
	}
}

func TestSecurityFounderBanRequiresExplicitOfflineRecovery(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	worker, _, _ := f.register(t, "worker", 4101)
	project, err := f.s.CreateProject(ctx, f.founder.AgentID, domain.ProjectRequest{Command: op(), Name: "retained project"})
	if err != nil {
		t.Fatal(err)
	}
	owner := securityPublic(t, "Founder owner")
	founder, err := f.s.SetIdentityOwner(ctx, f.founder.AgentID, owner.public.AuthKey, f.founder.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.BanDuplicateIdentity(ctx, founder.AgentID, founder.Generation, founder.OwnerGeneration, "confirmed duplicate Founder controllers"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RegisterIdentity(ctx, founder.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: securityPublic(t, "forbidden").public})
	assertCode(t, err, domain.Denied)
	_, err = f.s.BootstrapIdentity(ctx, f.keys.public, op().OperationID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.BootstrapIdentity(ctx, securityPublic(t, "second Founder").public, op().OperationID)
	assertCode(t, err, domain.Conflict)
	fresh := securityPublic(t, "Founder recovery")
	req := domain.RecoverIdentityRequest{Command: op(), Reason: "explicit local maintenance recovery", Identity: fresh.public}
	_, err = f.s.RecoverIdentity(ctx, founder.AgentID, founder.AgentID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RecoverIdentity(ctx, worker.AgentID, founder.AgentID, req)
	assertCode(t, err, domain.Denied)
	recovered, err := f.s.RecoverFounderOffline(ctx, req)
	if err != nil || recovered.AgentID != founder.AgentID || recovered.Generation != founder.Generation+2 || recovered.State != "pending" || recovered.UID != -1 {
		t.Fatal("offline recovery reset or replaced Founder", recovered, err)
	}
	var founders, events, audits int
	if err := f.s.db.QueryRow("SELECT count(*) FROM agents WHERE json_extract(data,'$.founder')=1").Scan(&founders); err != nil || founders != 1 {
		t.Fatal("offline recovery created another Founder", founders, err)
	}
	if _, err := f.s.Project(ctx, worker.AgentID, project.ID); err != nil {
		t.Fatal("offline recovery reset project history", err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM audit WHERE action='identity.founder-offline-recovery-required'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("Founder ban missing local diagnostic", audits, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM events WHERE source_id=?", founder.AgentID).Scan(&events); err != nil || events < 4 {
		t.Fatal("Founder event history was reset", events, err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, founder.UID)
	replay, err := f.s.RecoverFounderOffline(ctx, req)
	if err != nil || replay != recovered {
		t.Fatal("offline recovery replay did not retain activation", err)
	}
	active, err := f.s.Me(ctx, recovered.AgentID)
	if err != nil || !active.Founder || !active.Active || active.Banned || active.Name != "Founder" {
		t.Fatal("recovered Founder role/history changed", active, err)
	}
	_, err = f.s.ValidateCertificate(ctx, founder.UID, securityCertificate(founder, "device"), f.keys.auth.Public())
	assertCode(t, err, domain.Denied)
	empty := securityEmptyStore(t)
	_, err = empty.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "must not bootstrap", Identity: securityPublic(t, "not a Founder yet").public})
	assertCode(t, err, domain.Denied)
}

func TestSecurityBanPreservesFencedWorkAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	identity, err := f.s.Identity(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	task := f.ready(t)
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.BanIdentity(ctx, identity.AgentID, "explicit confirmed duplicate"); err != nil {
		t.Fatal(err)
	}
	stopped, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil || stopped.ActiveRun == nil || stopped.ActiveRun.ID != run.ID || !stopped.ActiveRun.StopRequested || stopped.Readiness != nil {
		t.Fatal("ban released or failed to stop existing work ownership", stopped.ActiveRun, err)
	}
	_, err = f.s.ChangeTask(ctx, f.worker.ID, task.ID, domain.TaskChange{Command: op(), Kind: "edit", Reason: "denied after independent ban"})
	assertCode(t, err, domain.Denied)
	var banned int
	if err := f.s.db.QueryRow("SELECT json_extract(data,'$.banned') FROM agents WHERE id=?", identity.AgentID).Scan(&banned); err != nil || banned != 1 {
		t.Fatal("typed denial rolled back the independent ban", banned, err)
	}
	var mainFile string
	if err := f.s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&mainFile); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, mainFile)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Identity(ctx, identity.AgentID)
	if err != nil || got.State != "banned" {
		t.Fatal("ban did not persist across independent store reopen", got, err)
	}
}

func TestSecurityTrustedRequestFenceRejectsStaleAndWrongActors(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	worker, _, _ := f.register(t, "worker", 4101)
	owner := securityPublic(t, "Founder owner")
	founder, err := f.s.SetIdentityOwner(ctx, f.founder.AgentID, owner.public.AuthKey, f.founder.Generation)
	if err != nil {
		t.Fatal(err)
	}
	fenced := WithIdentityFence(ctx, securityCertificate(founder, "owner"))
	_, err = f.s.Me(fenced, worker.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.CreateProject(fenced, worker.AgentID, domain.ProjectRequest{Command: op(), Name: "wrong actor"})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.SendMessage(fenced, founder.AgentID, domain.MessageRequest{Command: op(), RecipientID: worker.AgentID, Body: "legitimate multi-party policy", Origin: "service", Kind: "direct"}); err != nil {
		t.Fatal("actor fence prevented an active subject lookup", err)
	}
	newOwner := securityPublic(t, "Founder restarted owner")
	founder, err = f.s.SetIdentityOwner(ctx, founder.AgentID, newOwner.public.AuthKey, founder.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateProject(fenced, founder.AgentID, domain.ProjectRequest{Command: op(), Name: "stale owner"})
	assertCode(t, err, domain.Denied)
	newFence := WithIdentityFence(ctx, securityCertificate(founder, "owner"))
	if _, err := f.s.CreateProject(newFence, founder.AgentID, domain.ProjectRequest{Command: op(), Name: "current owner"}); err != nil {
		t.Fatal(err)
	}
	writer, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	captured := WithIdentityFence(ctx, securityCertificate(founder, "owner"))
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := f.s.CreateProject(captured, founder.AgentID, domain.ProjectRequest{Command: op(), Name: "must not commit after rotation"})
		result <- err
	}()
	<-started
	founder.Generation++
	if err := saveIdentity(ctx, writer, founder); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCode(t, <-result, domain.Denied)
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM projects WHERE json_extract(data,'$.name')='must not commit after rotation'").Scan(&count); err != nil || count != 0 {
		t.Fatal("request authenticated before rotation mutated after it", count, err)
	}
}
