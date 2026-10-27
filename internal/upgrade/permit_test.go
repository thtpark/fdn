package upgrade

import (
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
)

func testProposal(t *testing.T, now time.Time) domain.UpgradeProposal {
	t.Helper()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	release, _ := fixtureBundle(t, "linux-amd64", nil)
	return domain.UpgradeProposal{
		Challenge: domain.UpgradeChallenge{ID: "upgrade.fixture.0", Nonce: domain.NewToken(), InstanceID: "instance_fixture",
			TargetID: "agent_fixture", Role: "agent", Generation: 1, CurrentVersion: "0.3.2",
			CurrentCommit: strings.Repeat("b", 40), PolicySHA256: strings.Repeat("d", 64), ExpiresAt: now.Unix() + 900},
		Release: release, BackupRecipient: key.Recipient().String(), Reason: "Explicit exact-source native upgrade",
	}
}

func TestUpgradePermitBindsEveryNativeBoundaryAndNeverRenewsOnReplay(t *testing.T) {
	now := time.Now().UTC()
	ca, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}

	permit := domain.UpgradePermit{Proposal: testProposal(t, now), ActorID: "agent_founder",
		ActorGeneration: 2, IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 900}
	permit.Signature, err = SignPermit(permit, ca.CAKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, ca.CA, now); err != nil {
		t.Fatal(err)
	}
	for name, modify := range map[string]func(*domain.UpgradePermit){
		"target":     func(p *domain.UpgradePermit) { p.Proposal.Challenge.TargetID = "agent_other" },
		"generation": func(p *domain.UpgradePermit) { p.Proposal.Challenge.Generation++ },
		"source":     func(p *domain.UpgradePermit) { p.Proposal.Release.Commit = strings.Repeat("c", 40) },
		"payload":    func(p *domain.UpgradePermit) { p.Proposal.Release.BundleSHA256 = strings.Repeat("0", 64) },
		"recipient":  func(p *domain.UpgradePermit) { p.Proposal.BackupRecipient = "not-a-public-age-recipient" },
		"actor":      func(p *domain.UpgradePermit) { p.ActorID = "agent_delegate" },
		"lifetime":   func(p *domain.UpgradePermit) { p.ExpiresAt++ },
		"nonce":      func(p *domain.UpgradePermit) { p.Proposal.Challenge.Nonce = domain.NewToken() },
	} {
		t.Run(name, func(t *testing.T) {
			changed := permit
			modify(&changed)
			if err := VerifyPermit(changed, ca.CA, now); err == nil {
				t.Fatal("altered native permit was accepted")
			}
		})
	}
	if err := VerifyPermit(permit, ca.CA, now.Add(900*time.Second)); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("expired permit was accepted", err)
	}
	other, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, other.CA, now); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another Foundation CA authorized this upgrade", err)
	}
}
