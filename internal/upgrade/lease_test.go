package upgrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
)

func TestUpgradeStartLeaseBindsTheExactFreshNonceAndAuthorization(t *testing.T) {
	now := time.Now().UTC()
	ca, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.UpgradeLeaseRequest{PermitSHA256: strings.Repeat("a", 64), Nonce: domain.NewToken()}
	lease := domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce,
		IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 30}
	lease.Signature, err = SignLease(lease, ca.CAKey)
	if err != nil || VerifyLease(lease, request, ca.CA, now) != nil {
		t.Fatal("valid native start proof failed", err)
	}
	other := request
	other.Nonce = domain.NewToken()
	if err := VerifyLease(lease, other, ca.CA, now); err == nil {
		t.Fatal("cached authority proof answered a new native nonce")
	}
	if err := VerifyLease(lease, request, ca.CA, now.Add(30*time.Second)); err == nil {
		t.Fatal("expired native start proof was accepted")
	}
}

func TestNativeUpgradeRevokedStartAuthorityNeverStopsServices(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	native.Lease = func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		return domain.UpgradeLease{}, domain.Fail(domain.Denied, "Founder identity was retired before native start")
	}
	_, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{
		Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit,
	})
	if domain.AsFault(err).Code != domain.Denied {
		t.Fatal("retired Founder authority started a native upgrade", err)
	}
	for _, call := range recorder.calls {
		if strings.Contains(strings.Join(call.Args, " "), "stop ") || call.Path == "/bin/sh" {
			t.Fatal("native services stopped without current server start authority")
		}
	}
}
