package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestUpgradeStartLeaseBindsCallerCertificateNotOtherPermitSubjects(t *testing.T) {
	f, target, permit := authorizedUpgradeFixture(t, false)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	targetCtx := securityOwnerContext(t, f.s, target)
	request := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	calls := 0
	sign := func(lease domain.UpgradeLease) (string, error) {
		calls++
		return domain.Hash(lease.Nonce), nil
	}
	for _, caller := range []struct {
		name, actor string
		ctx         context.Context
	}{
		{"issuer", f.founder.AgentID, founderCtx},
		{"target", target.AgentID, targetCtx},
	} {
		t.Run(caller.name, func(t *testing.T) {
			lease, err := f.s.LeaseUpgrade(caller.ctx, caller.actor, request, false, sign)
			if err != nil || lease.Nonce != request.Nonce || lease.ExpiresAt-lease.IssuedAt != 30 {
				t.Fatal("current certificate-bound caller could not lease its exact permit", lease, err)
			}
		})
	}
	if calls != 2 {
		t.Fatal("valid current parties did not each receive a signed lease", calls)
	}
	for _, forged := range []struct {
		actor string
		ctx   context.Context
	}{{target.AgentID, founderCtx}, {f.founder.AgentID, targetCtx}} {
		_, err := f.s.LeaseUpgrade(forged.ctx, forged.actor, request, false, sign)
		assertCode(t, err, domain.Denied)
	}
	securityOwnerContext(t, f.s, target)
	_, err := f.s.LeaseUpgrade(targetCtx, target.AgentID, request, false, sign)
	assertCode(t, err, domain.Denied)
	if calls != 2 {
		t.Fatal("forged or stale caller reached the lease signer", calls)
	}
}
