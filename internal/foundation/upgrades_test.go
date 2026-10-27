package foundation

import (
	"context"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func upgradeFixtureRequest(t *testing.T, f *securityFixture, target domain.AgentIdentity) domain.UpgradeAuthorizationRequest {
	t.Helper()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return domain.UpgradeAuthorizationRequest{Command: op(), Proposal: domain.UpgradeProposal{
		Challenge: domain.UpgradeChallenge{ID: "upgrade.fixture.0", Nonce: domain.NewToken(),
			InstanceID: f.founder.InstanceID, TargetID: target.AgentID, Role: "agent", Generation: target.Generation,
			CurrentVersion: "0.3.2", CurrentCommit: strings.Repeat("a", 40), PolicySHA256: strings.Repeat("d", 64), ExpiresAt: f.now.Unix() + 900},
		Release: domain.UpgradeRelease{Version: "0.3.3", Commit: strings.Repeat("b", 40), Platform: "linux-amd64",
			BundleURL:    "https://github.com/thtpark/fdn/releases/download/v0.3.3/fdn-0.3.3-linux-amd64.zip",
			BundleSHA256: strings.Repeat("c", 64), BundleBytes: 12345},
		BackupRecipient: key.Recipient().String(), Reason: "Exact approved software maintenance",
	}}
}

func TestFounderUpgradeAuthorizationIsTransactionalScopedAndExactReplay(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Agent1", 4101)
	req := upgradeFixtureRequest(t, f, target)
	_, err := f.s.UpgradeContext(ctx, target.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.AuthorizeUpgrade(ctx, target.AgentID, req, func(domain.UpgradePermit) (string, error) { t.Fatal("delegate reached signing"); return "", nil })
	assertCode(t, err, domain.Denied)
	agent, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, func(domain.UpgradePermit) (string, error) { return "", nil })
	assertCode(t, err, domain.Conflict)
	_, err = f.s.SetMode(ctx, f.founder.AgentID, target.AgentID, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: agent.Revision},
		Mode:    "manual", Reason: "Authorized upgrade maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sign := func(permit domain.UpgradePermit) (string, error) {
		calls++
		if permit.ActorID != f.founder.AgentID || permit.ActorGeneration != f.founder.Generation {
			t.Fatal("upgrade lost the actual Founder identity/key generation")
		}
		return "fixture-signature", nil
	}
	permit, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, sign)
	if err != nil || permit.ExpiresAt-permit.IssuedAt != 900 || calls != 1 {
		t.Fatal(permit, err)
	}
	f.now = f.now.Add(20 * time.Minute)
	replay, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, sign)
	if err != nil || replay != permit || calls != 1 {
		t.Fatal("exact authorization replay renewed or changed the original permit", replay, err)
	}
	req.Proposal.Release.Commit = strings.Repeat("d", 40)
	_, err = f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, sign)
	assertCode(t, err, domain.Conflict)
	if calls != 1 {
		t.Fatal("changed proposal reused an old authorization operation")
	}
}

func TestUpgradeAuthorizationRejectsExpiredForeignAndStaleGeneration(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Agent1", 4101)
	a, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SetMode(ctx, f.founder.AgentID, a.ID, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: a.Revision}, Mode: "manual", Reason: "Maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*domain.UpgradeAuthorizationRequest){
		"expired":          func(r *domain.UpgradeAuthorizationRequest) { r.Proposal.Challenge.ExpiresAt = f.now.Unix() },
		"future":           func(r *domain.UpgradeAuthorizationRequest) { r.Proposal.Challenge.ExpiresAt += 1 },
		"foreign-instance": func(r *domain.UpgradeAuthorizationRequest) { r.Proposal.Challenge.InstanceID = "instance_other" },
		"stale-generation": func(r *domain.UpgradeAuthorizationRequest) { r.Proposal.Challenge.Generation++ },
		"unknown-agent":    func(r *domain.UpgradeAuthorizationRequest) { r.Proposal.Challenge.TargetID = domain.NewID("agent") },
	} {
		t.Run(name, func(t *testing.T) {
			req := upgradeFixtureRequest(t, f, target)
			change(&req)
			_, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, func(domain.UpgradePermit) (string, error) {
				t.Fatal("unapproved native target reached the signer")
				return "", nil
			})
			if err == nil {
				t.Fatal("invalid/stale native upgrade was authorized")
			}
		})
	}
}

func TestFoundationUpgradeRequiresEveryActiveAgentPaused(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Agent1", 4101)
	req := upgradeFixtureRequest(t, f, target)
	req.Proposal.Challenge.Role, req.Proposal.Challenge.TargetID, req.Proposal.Challenge.Generation =
		"foundation", f.founder.InstanceID, 1
	_, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, req, func(domain.UpgradePermit) (string, error) {
		t.Fatal("Foundation upgrade ignored an unpaused worker")
		return "", nil
	})
	assertCode(t, err, domain.Prerequisite)
	if err := upgrade.ValidateProposal(req.Proposal); err != nil {
		t.Fatal("fixture is not a valid exact Foundation proposal", err)
	}
}
