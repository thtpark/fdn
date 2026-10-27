package upgrade

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
)

func ValidateProposal(p domain.UpgradeProposal) error {
	c := p.Challenge
	if !domain.ValidOperation(c.ID) || len(c.Nonce) != 43 || c.InstanceID == "" || len(c.InstanceID) > 128 ||
		c.TargetID == "" || len(c.TargetID) > 128 ||
		(c.Role != "foundation" && c.Role != "agent") || c.Generation < 1 || c.ExpiresAt <= 0 ||
		!sourceCommit.MatchString(c.CurrentCommit) || !domain.ValidHash(c.PolicySHA256) {
		return domain.Fail(domain.Invalid, "upgrade requires an exact native target challenge and current identity/source")
	}
	if _, err := age.ParseX25519Recipient(p.BackupRecipient); err != nil {
		return domain.Fail(domain.Invalid, "upgrade requires the public age recipient of an off-host recovery key")
	}
	if err := domain.CheckText(p.Reason, "upgrade reason", 2048, true); err != nil {
		return err
	}
	if err := ValidateRelease(p.Release); err != nil {
		return err
	}
	order, err := CompareVersion(c.CurrentVersion, p.Release.Version)
	if err != nil {
		return err
	}
	if order >= 0 {
		return domain.Fail(domain.Conflict, "upgrade must advance VERSION; same-version replacement and automatic rollback are forbidden")
	}
	return nil
}

func permitBytes(p domain.UpgradePermit) ([]byte, error) {
	p.Signature = ""
	data, err := json.Marshal(p)
	return append([]byte("foundation.native-upgrade.authorization.v1\x00"), data...), err
}

func SignPermit(p domain.UpgradePermit, keyPEM []byte) (string, error) {
	data, err := permitBytes(p)
	if err != nil {
		return "", err
	}
	return signAuthorization(data, keyPEM)
}

func signAuthorization(data, keyPEM []byte) (string, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return "", domain.Fail(domain.Prerequisite, "Foundation upgrade authorization signer is unavailable")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return "", domain.Fail(domain.Prerequisite, "Foundation upgrade authorization requires its enrolled Ed25519 CA signer")
	}
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, data)), nil
}

func VerifyPermit(p domain.UpgradePermit, caPEM []byte, now time.Time) error {
	if err := ValidateProposal(p.Proposal); err != nil {
		return err
	}
	if p.ActorID == "" || p.ActorGeneration < 1 || p.IssuedAt > now.Unix()+30 || p.ExpiresAt <= now.Unix() ||
		p.ExpiresAt-p.IssuedAt != 900 {
		return domain.Fail(domain.Denied, "upgrade authorization is missing, expired or has an invalid fixed lifetime")
	}
	data, err := permitBytes(p)
	if err != nil {
		return err
	}
	return verifyAuthorization(data, p.Signature, caPEM, now)
}

func verifyAuthorization(data []byte, signatureText string, caPEM []byte, now time.Time) error {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return domain.Fail(domain.Denied, "updater policy requires the independently pinned Foundation CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	public, ok := ca.PublicKey.(ed25519.PublicKey)
	if !ok || !ca.IsCA || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return domain.Fail(domain.Denied, "updater policy CA is not currently valid")
	}
	signature, err := base64.RawStdEncoding.DecodeString(signatureText)
	if err != nil {
		return domain.Fail(domain.Denied, "invalid native upgrade authorization signature")
	}
	if !ed25519.Verify(public, data, signature) {
		return domain.Fail(domain.Denied, "native upgrade was not authorized by the pinned Foundation")
	}
	return nil
}

func leaseBytes(lease domain.UpgradeLease) ([]byte, error) {
	lease.Signature = ""
	data, err := json.Marshal(lease)
	return append([]byte("foundation.native-upgrade.start-lease.v1\x00"), data...), err
}

func SignLease(lease domain.UpgradeLease, key []byte) (string, error) {
	data, err := leaseBytes(lease)
	if err != nil {
		return "", err
	}
	return signAuthorization(data, key)
}

func VerifyLease(lease domain.UpgradeLease, request domain.UpgradeLeaseRequest, ca []byte, now time.Time) error {
	if lease.Nonce != request.Nonce || lease.PermitSHA256 != request.PermitSHA256 || !domain.ValidHash(lease.PermitSHA256) ||
		len(lease.Nonce) != 43 || lease.ExpiresAt-lease.IssuedAt != 30 || lease.ExpiresAt <= now.Unix() || lease.IssuedAt > now.Unix()+5 {
		return domain.Fail(domain.Denied, "native upgrade start requires its fresh nonce-bound server authority lease")
	}
	data, err := leaseBytes(lease)
	if err != nil {
		return err
	}
	return verifyAuthorization(data, lease.Signature, ca, now)
}
