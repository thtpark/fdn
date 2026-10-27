package foundation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func authorizedUpgradeFixture(t *testing.T, foundation bool) (*securityFixture, domain.AgentIdentity, domain.UpgradePermit) {
	t.Helper()
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Agent1", 4101)
	a, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetMode(ctx, f.founder.AgentID, a.ID, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: a.Revision}, Mode: "manual", Reason: "Explicit maintenance",
	}); err != nil {
		t.Fatal(err)
	}
	req := upgradeFixtureRequest(t, f, target)
	if foundation {
		req.Proposal.Challenge.Role, req.Proposal.Challenge.TargetID, req.Proposal.Challenge.Generation =
			"foundation", f.founder.InstanceID, 1
	}
	permit, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, func(permit domain.UpgradePermit) (string, error) {
		data, err := json.Marshal(permit)
		return domain.Hash(string(data)), err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, target, permit
}

func TestUpgradeStartLeaseRechecksCurrentPartiesAndKeyGenerations(t *testing.T) {
	ctx := context.Background()
	f, target, permit := authorizedUpgradeFixture(t, false)
	request := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	sign := func(lease domain.UpgradeLease) (string, error) { return domain.Hash(lease.Nonce), nil }
	lease, err := f.s.LeaseUpgrade(ctx, target.AgentID, request, false, sign)
	if err != nil || lease.Nonce != request.Nonce || lease.ExpiresAt-lease.IssuedAt != 30 {
		t.Fatal("current target did not receive its exact short start proof", lease, err)
	}
	other, _, _ := f.register(t, "Other", 4102)
	_, err = f.s.LeaseUpgrade(ctx, other.AgentID, request, false, sign)
	assertCode(t, err, domain.Denied)
	_, err = f.s.LeaseUpgrade(ctx, "", request, true, sign)
	assertCode(t, err, domain.Denied)
	fresh := securityPublic(t, "Founder")
	recovered, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{
		Command: op(), Identity: fresh.public, Reason: "Explicit key-generation change invalidates pending native starts",
	})
	if err != nil {
		t.Fatal(err)
	}
	securityActivate(t, f.s, recovered, fresh, f.founder.UID)
	_, err = f.s.LeaseUpgrade(ctx, target.AgentID, request, false, sign)
	assertCode(t, err, domain.Denied)
}

func TestFoundationStartLeaseIsPrivateMaintenanceOrIssuerAndStillRequiresPause(t *testing.T) {
	ctx := context.Background()
	f, target, permit := authorizedUpgradeFixture(t, true)
	request := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	sign := func(domain.UpgradeLease) (string, error) { return "fixture-lease", nil }
	if _, err := f.s.LeaseUpgrade(ctx, "", request, true, sign); err != nil {
		t.Fatal("private Foundation maintenance could not validate the approved service upgrade", err)
	}
	_, err := f.s.LeaseUpgrade(ctx, target.AgentID, request, false, sign)
	assertCode(t, err, domain.Denied)
	a, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetMode(ctx, f.founder.AgentID, a.ID, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: a.Revision}, Mode: "ai", Reason: "Explicit resumption invalidates native start",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.LeaseUpgrade(ctx, "", request, true, sign)
	assertCode(t, err, domain.Prerequisite)
}

func TestUpgradeStartLeaseRejectsNewExecutionOwnershipAfterAuthorization(t *testing.T) {
	ctx := context.Background()
	f, target, permit := authorizedUpgradeFixture(t, false)
	project, err := f.s.CreateProject(ctx, f.founder.AgentID, domain.ProjectRequest{Command: op(), Name: "Upgrade ownership fixture"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.s.CreateTask(ctx, f.founder.AgentID, domain.TaskRequest{Command: op(), ProjectID: project.ID,
		Title: "Independent active verifier", Description: "Fixture state", AcceptanceCriteria: "No upgrade during ownership"})
	if err != nil {
		t.Fatal(err)
	}
	active, _ := json.Marshal(domain.ActiveRun{ID: domain.NewID("run"), AgentID: target.AgentID, Kind: domain.VerificationRun})
	if _, err := f.s.db.ExecContext(ctx, "UPDATE tasks SET data=json_set(data,'$.active_run',json(?)) WHERE id=?", string(active), task.ID); err != nil {
		t.Fatal(err)
	}
	request := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	_, err = f.s.LeaseUpgrade(ctx, target.AgentID, request, false, func(domain.UpgradeLease) (string, error) {
		t.Fatal("active independent verification reached the native start signer")
		return "", nil
	})
	assertCode(t, err, domain.Prerequisite)
}
