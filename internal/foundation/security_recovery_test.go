package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

func TestSecurityRecoveryExpectedRevisionFencesPendingReplacement(t *testing.T) {
	f := newSecurityFixture(t)
	worker, _, _ := f.register(t, "worker", 6101)
	ctx := securityOwnerContext(t, f.s, f.founder)
	before, err := f.s.Agent(ctx, f.founder.AgentID, worker.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	firstKeys, secondKeys := securityPublic(t, "first approved recovery"), securityPublic(t, "second approved recovery")
	firstReq := domain.RecoverIdentityRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: before.Revision},
		Reason: "first approved key intent", Identity: firstKeys.public}
	pending, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, firstReq)
	if err != nil || pending.State != "pending" || pending.Generation != worker.Generation+1 {
		t.Fatal("current revision recovery failed", pending, err)
	}
	replay, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, firstReq)
	if err != nil || replay != pending {
		t.Fatal("exact replay incorrectly rechecked its pre-mutation revision", replay, err)
	}
	newOperation := firstReq
	newOperation.OperationID = op().OperationID
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, newOperation)
	assertCode(t, err, domain.Conflict)
	secondReq := domain.RecoverIdentityRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: before.Revision},
		Reason: "different stale approved key intent", Identity: secondKeys.public}
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, secondReq)
	assertCode(t, err, domain.Conflict)
	retained, err := f.s.Identity(context.Background(), worker.AgentID)
	if err != nil || retained != pending {
		t.Fatal("stale approved intent replaced newer pending keys", retained, err)
	}
	var reserved int
	_, secondFingerprint, err := device.Validate(secondKeys.public)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE fingerprint=?", secondFingerprint).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("stale recovery reserved unapproved replacement keys", reserved, err)
	}
	current, err := f.s.Agent(ctx, f.founder.AgentID, worker.AgentID)
	if err != nil || current.Revision != before.Revision+1 {
		t.Fatal("stale recovery changed target revision", current.Revision, err)
	}
	invalidRevision := secondReq
	invalidRevision.OperationID, invalidRevision.ExpectedRevision = op().OperationID, -1
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, invalidRevision)
	assertCode(t, err, domain.Invalid)
	newOperation.OperationID, newOperation.ExpectedRevision = op().OperationID, current.Revision
	replay, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, newOperation)
	if err != nil || replay != pending {
		t.Fatal("fresh revision could not reconcile an already accepted public request", replay, err)
	}
	secondReq.OperationID, secondReq.ExpectedRevision = op().OperationID, current.Revision
	newPending, err := f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, secondReq)
	if err != nil || newPending.Generation != pending.Generation+1 || newPending.AuthKey != secondKeys.public.AuthKey || newPending.State != "pending" {
		t.Fatal("fresh explicit intent could not replace pending recovery", newPending, err)
	}
	_, err = f.s.RecoverIdentity(ctx, f.founder.AgentID, worker.AgentID, firstReq)
	assertCode(t, err, domain.Conflict)
	retained, err = f.s.Identity(context.Background(), worker.AgentID)
	if err != nil || retained != newPending {
		t.Fatal("retired recovery receipt replaced newer keys", retained, err)
	}
}

func TestSecurityRootRotationOfActiveRegisteredFounder(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	owner := securityPublic(t, "active Founder owner")
	original, err := f.s.SetIdentityOwner(ctx, f.founder.AgentID, owner.public.AuthKey, f.founder.Generation)
	if err != nil {
		t.Fatal(err)
	}
	founder, err := f.s.Me(ctx, original.AgentID)
	if err != nil || !founder.Active || !founder.Founder || founder.Banned {
		t.Fatal("fixture did not start with an active registered Founder", founder, err)
	}
	founder, err = f.s.SetMode(ctx, founder.ID, founder.ID, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: founder.Revision}, Mode: "manual", Reason: "retained mode attribution"})
	if err != nil {
		t.Fatal(err)
	}
	projectReq := domain.ProjectRequest{Command: op(), Name: "retained project during root rotation"}
	project, err := f.s.CreateProject(ctx, founder.ID, projectReq)
	if err != nil {
		t.Fatal(err)
	}
	var originalOperation, originalHistory string
	if err := f.s.db.QueryRow("SELECT fingerprint FROM operations WHERE actor_id=? AND operation_id=?", founder.ID, projectReq.OperationID).Scan(&originalOperation); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow("SELECT snapshot FROM histories WHERE source_id=?", project.ID).Scan(&originalHistory); err != nil {
		t.Fatal(err)
	}
	challenge, err := f.s.ChallengeIdentity(ctx, original.UID)
	if err != nil {
		t.Fatal(err)
	}
	oldProof, err := device.SignChallenge(f.keys.auth, challenge)
	if err != nil {
		t.Fatal(err)
	}
	fresh := securityPublic(t, "fresh root-local Founder rotation")
	req := domain.RecoverIdentityRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: founder.Revision},
		Reason: "explicit root-maintenance rotation of active Founder", Identity: fresh.public}
	rotated, err := f.s.RecoverFounderOffline(ctx, req)
	if err != nil || rotated.AgentID != original.AgentID || rotated.Generation != original.Generation+1 || rotated.OwnerGeneration != original.OwnerGeneration+1 ||
		rotated.State != "pending" || rotated.UID != -1 || rotated.OwnerKey != "" {
		t.Fatal("root maintenance rejected active Founder or reset its identity", rotated, err)
	}
	_, err = f.s.Me(ctx, original.AgentID)
	assertCode(t, err, domain.Denied)
	replay, err := f.s.RecoverFounderOffline(ctx, req)
	if err != nil || replay != rotated {
		t.Fatal("exact root rotation replay advanced epochs", replay, err)
	}
	rotated, _ = securityActivate(t, f.s, rotated, fresh, original.UID)
	after, err := f.s.Me(ctx, original.AgentID)
	if err != nil || !after.Active || !after.Founder || after.Banned || after.Name != founder.Name || after.CreatedAt != founder.CreatedAt ||
		after.Mode != founder.Mode || after.ModeRequestedBy != founder.ModeRequestedBy || after.ModeReason != founder.ModeReason || after.ModeChangedAt != founder.ModeChangedAt {
		t.Fatal("root rotation changed role or retained operational identity", after, err)
	}
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM agents WHERE json_extract(data,'$.founder')=1").Scan(&count); err != nil || count != 1 {
		t.Fatal("root rotation created another Founder", count, err)
	}
	var operation, history string
	if err := f.s.db.QueryRow("SELECT fingerprint FROM operations WHERE actor_id=? AND operation_id=?", founder.ID, projectReq.OperationID).Scan(&operation); err != nil || operation != originalOperation {
		t.Fatal("root rotation replaced prior operation evidence", err)
	}
	if err := f.s.db.QueryRow("SELECT snapshot FROM histories WHERE source_id=?", project.ID).Scan(&history); err != nil || history != originalHistory {
		t.Fatal("root rotation replaced project history", err)
	}
	_, err = f.s.ValidateCertificate(ctx, original.UID, securityCertificate(original, "device"), f.keys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ValidateCertificate(ctx, original.UID, securityCertificate(original, "owner"), owner.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ActivateIdentity(ctx, original.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: challenge.Nonce, Proof: oldProof})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.ValidateCertificate(ctx, rotated.UID, securityCertificate(rotated, "device"), fresh.auth.Public()); err != nil {
		t.Fatal("freshly rotated Founder could not authenticate", err)
	}
	replay, err = f.s.RecoverFounderOffline(ctx, req)
	if err != nil || replay != rotated {
		t.Fatal("root rotation replay lost successful activation", replay, err)
	}
}
