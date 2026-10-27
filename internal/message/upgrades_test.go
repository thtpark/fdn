package message

import (
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestUpgradePayloadContractsPreserveExactPresenceAndSource(t *testing.T) {
	release := domain.UpgradeRelease{Version: "0.3.4", Commit: strings.Repeat("a", 40), Platform: "linux-amd64",
		BundleURL:    "https://github.com/thtpark/fdn/releases/download/v0.3.4/fdn-0.3.4-linux-amd64.zip",
		BundleSHA256: strings.Repeat("b", 64), BundleBytes: 12345}
	challenge := domain.UpgradeChallenge{ID: "upgrade.fixture.0", Nonce: domain.NewToken(), Role: "agent",
		InstanceID: "instance_fixture", TargetID: "agent_fixture", Generation: 7,
		CurrentVersion: "0.3.3", CurrentCommit: strings.Repeat("c", 40), PolicySHA256: strings.Repeat("d", 64), ExpiresAt: 1800000900}
	proposal := domain.UpgradeProposal{Challenge: challenge, Release: release, BackupRecipient: "public-fixture", Reason: "Explicit maintenance"}
	permit := domain.UpgradePermit{Proposal: proposal, ActorID: "agent_founder", ActorGeneration: 3,
		IssuedAt: 1800000000, ExpiresAt: 1800000900, Signature: "public-signature-fixture"}
	lease := domain.UpgradeLease{PermitSHA256: strings.Repeat("e", 64), Nonce: domain.NewToken(),
		IssuedAt: 1800000000, ExpiresAt: 1800000030, Signature: "public-start-proof"}
	for _, value := range []any{
		release, challenge, proposal, permit,
		domain.UpgradeAuthorizationRequest{Command: domain.Command{OperationID: "upgrade.authorize.fixture"}, Proposal: proposal},
		domain.UpgradeContext{InstanceID: "instance_fixture", Version: "0.3.3", Commit: strings.Repeat("c", 40), Agents: []domain.Agent{}},
		domain.UpgradeNativeRequest{Action: "inspect", ID: "upgrade.fixture.0"},
		domain.UpgradeNativeRequest{Action: "prepare", ID: "upgrade.fixture.0", Permit: &permit},
		domain.UpgradeNativeReply{State: "planned", Challenge: &challenge},
		domain.UpgradeNativeReply{State: "operator-required", Code: "installing", Version: "0.3.4", Commit: release.Commit},
		domain.UpgradeNativeReply{State: "failed", Error: domain.AsFault(domain.Fail(domain.Denied, "revoked"))},
		domain.UpgradeLeaseRequest{PermitSHA256: lease.PermitSHA256, Nonce: lease.Nonce}, lease,
		domain.UpgradeNativeReply{State: "start-authorized", Lease: &lease},
	} {
		roundTrip(t, value)
	}
}
