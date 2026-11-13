package upgrade

import (
	"encoding/json"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func ValidateRestartPermit(p domain.AgentRestartPermit) error {
	for _, id := range []string{p.ID, p.InstanceID, p.RequesterID, p.RequesterBindingID, p.TargetID} {
		if !domain.ValidOperation(id) {
			return domain.Fail(domain.Invalid, "native restart requires exact enrolled identity references")
		}
	}
	if p.RequesterGeneration <= 0 || p.TargetGeneration <= 0 || p.ModeRevision <= 0 ||
		p.CreatedAt <= 0 || p.ExpiresAt-p.CreatedAt != domain.AgentOperationTTL {
		return domain.Fail(domain.Invalid, "native restart requires current generations and a fixed lifespan")
	}
	return domain.CheckText(p.Reason, "nonsecret native restart reason", 8192, true)
}

func restartPermitBytes(p domain.AgentRestartPermit) ([]byte, error) {
	p.Signature = ""
	data, err := json.Marshal(p)
	return append([]byte("foundation.native-service-restart.authorization.v1\x00"), data...), err
}

func SignRestartPermit(p domain.AgentRestartPermit, key []byte) (string, error) {
	data, err := restartPermitBytes(p)
	if err != nil {
		return "", err
	}
	return signAuthorization(data, key)
}

func VerifyRestartPermit(p domain.AgentRestartPermit, ca []byte, now time.Time) error {
	if err := ValidateRestartPermit(p); err != nil {
		return err
	}
	if p.CreatedAt > now.Unix()+30 || p.ExpiresAt <= now.Unix() {
		return domain.Fail(domain.Denied, "native restart permit is not currently valid")
	}
	data, err := restartPermitBytes(p)
	if err != nil {
		return err
	}
	return verifyAuthorization(data, p.Signature, ca, now)
}

func restartLeaseBytes(lease domain.UpgradeLease) ([]byte, error) {
	lease.Signature = ""
	data, err := json.Marshal(lease)
	return append([]byte("foundation.native-service-restart.start-lease.v1\x00"), data...), err
}

func SignRestartLease(lease domain.UpgradeLease, key []byte) (string, error) {
	data, err := restartLeaseBytes(lease)
	if err != nil {
		return "", err
	}
	return signAuthorization(data, key)
}

func VerifyRestartLease(lease domain.UpgradeLease, permit domain.AgentRestartPermit, nonce string, ca []byte, now time.Time) error {
	if lease.PermitSHA256 != domain.Hash(permit.Signature) || lease.Nonce != nonce || len(nonce) != 43 ||
		lease.IssuedAt > now.Unix()+5 || lease.ExpiresAt <= now.Unix() ||
		lease.ExpiresAt-lease.IssuedAt != 30 || lease.ExpiresAt > permit.ExpiresAt {
		return domain.Fail(domain.Denied, "native restart requires its fresh exact nonce-bound authority")
	}
	data, err := restartLeaseBytes(lease)
	if err != nil {
		return err
	}
	return verifyAuthorization(data, lease.Signature, ca, now)
}
