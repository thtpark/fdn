package software

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
)

const (
	MaxPayloads      = 4096
	MaxPlatforms     = 16
	MaxManifestBytes = 1 << 20
	AuthorizationTTL = 900
)

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+:_~-]{0,127}$`)
	commitPattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

func validPlatform(value string) bool {
	return slices.Contains([]string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"}, value)
}

func validProfile(value string) bool {
	return value == "archive" || value == "user-service" || value == "debian"
}

func validateDependencies(dependencies []domain.SoftwareDependency, platform string) error {
	if len(dependencies) > 64 {
		return domain.Fail(domain.Invalid, "software dependencies exceed their count limit")
	}
	seen := make(map[string]bool)
	for _, dependency := range dependencies {
		if !domain.ValidOperation(dependency.ProductID) || !domain.ValidOperation(dependency.ReleaseID) ||
			dependency.Platform != platform || !domain.ValidHash(dependency.SHA256) || seen[dependency.ProductID] {
			return domain.Fail(domain.Invalid, "software dependencies require distinct exact product/release/platform/digest references")
		}
		if err := domain.CheckText(dependency.Purpose, "dependency purpose", 1024, true); err != nil {
			return err
		}
		seen[dependency.ProductID] = true
	}
	return nil
}

func validatePayloads(payloads []domain.SoftwarePayload) error {
	if len(payloads) == 0 || len(payloads) > MaxPayloads {
		return domain.Fail(domain.Invalid, "software payloads require a nonempty bounded regular-file inventory")
	}
	names := newNames()
	var total int64
	for _, payload := range payloads {
		if err := names.add(payload.Path); err != nil {
			return err
		}
		if !validMode(payload.Mode) || payload.Size < 0 || payload.Size > domain.MaxSoftwareBundleBytes-total ||
			!domain.ValidHash(payload.SHA256) {
			return domain.Fail(domain.Invalid, "software payloads require explicit safe modes, bounded sizes and SHA-256 digests")
		}
		total += payload.Size
	}
	return nil
}

// ValidateManifest validates signed content, not project or spending authority.
func ValidateManifest(manifest domain.SoftwareManifest) error {
	if manifest.FormatVersion != domain.SoftwareManifestVersion ||
		!domain.ValidOperation(manifest.InstanceID) || !domain.ValidOperation(manifest.ReleaseID) ||
		!domain.ValidOperation(manifest.ProductID) || !domain.ValidOperation(manifest.ProjectID) ||
		!domain.ValidOperation(manifest.Kind) || !versionPattern.MatchString(manifest.Version) ||
		manifest.Revision < 1 || manifest.CreatedAt <= 0 ||
		!domain.ValidOperation(manifest.Source.RepositoryID) || !commitPattern.MatchString(manifest.Source.Commit) ||
		manifest.Source.ProductID != "" && manifest.Source.ProductID != manifest.ProductID {
		return domain.Fail(domain.Invalid, "software manifest requires an exact instance, product, revision and immutable source")
	}
	if len(manifest.Source.Ref) > 256 || !utf8.ValidString(manifest.Source.Ref) || strings.ContainsAny(manifest.Source.Ref, "\x00\r\n\t\\ ") ||
		manifest.Source.Ref != "" && !strings.HasPrefix(manifest.Source.Ref, "refs/") {
		return domain.Fail(domain.Invalid, "software source ref must be an optional bounded full ref")
	}
	if len(manifest.Platforms) == 0 || len(manifest.Platforms) > MaxPlatforms {
		return domain.Fail(domain.Invalid, "software manifest requires bounded explicit platform artifacts")
	}
	seen := make(map[string]bool)
	totalPayloads := 0
	for _, platform := range manifest.Platforms {
		if !validPlatform(platform.Platform) || seen[platform.Platform] || !validProfile(platform.Profile) ||
			platform.Profile == "debian" && !strings.HasPrefix(platform.Platform, "linux-") ||
			!domain.ValidOperation(platform.ArtifactID) || !domain.ValidHash(platform.SHA256) ||
			platform.Size <= 0 || platform.Size > domain.MaxSoftwareBundleBytes {
			return domain.Fail(domain.Invalid, "software platforms require distinct supported profiles and exact bounded artifact bytes")
		}
		if err := validatePayloads(platform.Payloads); err != nil {
			return err
		}
		if err := validateDependencies(platform.Dependencies, platform.Platform); err != nil {
			return err
		}
		totalPayloads += len(platform.Payloads)
		if totalPayloads > MaxPayloads {
			return domain.Fail(domain.Invalid, "software manifest exceeds its total payload count limit")
		}
		seen[platform.Platform] = true
	}
	return nil
}

func sortedDependencies(values []domain.SoftwareDependency) []domain.SoftwareDependency {
	values = slices.Clone(values)
	slices.SortFunc(values, func(a, b domain.SoftwareDependency) int {
		return strings.Compare(a.ProductID, b.ProductID)
	})
	if values == nil {
		values = []domain.SoftwareDependency{}
	}
	return values
}

func sortedPayloads(values []domain.SoftwarePayload) []domain.SoftwarePayload {
	values = slices.Clone(values)
	slices.SortFunc(values, func(a, b domain.SoftwarePayload) int { return strings.Compare(a.Path, b.Path) })
	return values
}

// CanonicalManifest returns bounded canonical JSON, without a signing context.
// It sorts set-like inventories without modifying the caller's slices.
func CanonicalManifest(manifest domain.SoftwareManifest) ([]byte, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	manifest.Platforms = slices.Clone(manifest.Platforms)
	for i := range manifest.Platforms {
		manifest.Platforms[i].Payloads = sortedPayloads(manifest.Platforms[i].Payloads)
		manifest.Platforms[i].Dependencies = sortedDependencies(manifest.Platforms[i].Dependencies)
	}
	slices.SortFunc(manifest.Platforms, func(a, b domain.SoftwarePlatform) int {
		return strings.Compare(a.Platform, b.Platform)
	})
	data, err := json.Marshal(manifest)
	if len(data) > MaxManifestBytes {
		return nil, domain.Fail(domain.Invalid, "software manifest exceeds its canonical byte limit")
	}
	return data, err
}

func pemBlock(data []byte, kind string) (*pem.Block, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != kind || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, domain.Fail(domain.Denied, "software trust requires exactly one correctly typed PEM block")
	}
	return block, nil
}

func privateKey(data []byte) (ed25519.PrivateKey, error) {
	block, err := pemBlock(data, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Denied, "software signer requires a valid PKCS8 Ed25519 key")
	}
	key, ok := value.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, domain.Fail(domain.Denied, "software signer requires Ed25519")
	}
	return key, nil
}

func publicKey(data []byte) (ed25519.PublicKey, error) {
	block, err := pemBlock(data, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	value, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Denied, "distribution trust requires a valid PKIX Ed25519 public key")
	}
	key, ok := value.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, domain.Fail(domain.Denied, "distribution trust requires Ed25519")
	}
	return key, nil
}

func caKey(data []byte, now time.Time) (ed25519.PublicKey, error) {
	block, err := pemBlock(data, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Denied, "software authorization requires the pinned Foundation CA certificate")
	}
	key, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || !certificate.IsCA || !certificate.BasicConstraintsValid ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, domain.Fail(domain.Denied, "software authorization CA is not currently valid")
	}
	return key, nil
}

func keyID(key ed25519.PublicKey) string {
	data, _ := x509.MarshalPKIXPublicKey(key)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func verifySignature(key ed25519.PublicKey, data []byte, text string) error {
	signature, err := base64.RawStdEncoding.DecodeString(text)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawStdEncoding.EncodeToString(signature) != text || !ed25519.Verify(key, data, signature) {
		return domain.Fail(domain.Denied, "software signature does not match its independently pinned authority")
	}
	return nil
}

// SignRelease signs only the canonical manifest with a distribution key, never
// with an implicit Foundation authorization key.
func SignRelease(manifest domain.SoftwareManifest, privatePEM []byte) (string, string, error) {
	data, err := CanonicalManifest(manifest)
	if err != nil {
		return "", "", err
	}
	key, err := privateKey(privatePEM)
	if err != nil {
		return "", "", err
	}
	data = append([]byte("foundation.software.distribution-release.v1\x00"), data...)
	return keyID(key.Public().(ed25519.PublicKey)), base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, data)), nil
}

func VerifyRelease(release domain.SoftwareRelease, publicPEM []byte) error {
	if release.Revoked || release.ID != release.Manifest.ReleaseID {
		return domain.Fail(domain.Denied, "software release is revoked or does not identify its signed manifest")
	}
	data, err := CanonicalManifest(release.Manifest)
	if err != nil {
		return err
	}
	key, err := publicKey(publicPEM)
	if err != nil {
		return err
	}
	if release.KeyID != keyID(key) {
		return domain.Fail(domain.Denied, "software release does not use the pinned distribution key")
	}
	return verifySignature(key, append([]byte("foundation.software.distribution-release.v1\x00"), data...), release.Signature)
}

func platformFor(release domain.SoftwareRelease, name string) (domain.SoftwarePlatform, error) {
	for _, platform := range release.Manifest.Platforms {
		if platform.Platform == name {
			return platform, nil
		}
	}
	return domain.SoftwarePlatform{}, domain.Fail(domain.Denied, "software release has no artifact for the exact native platform")
}

func validNonce(value string) bool {
	data, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(data) == 32 && base64.RawURLEncoding.EncodeToString(data) == value
}

func ValidateProposal(proposal domain.SoftwareProposal) error {
	challenge := proposal.Challenge
	foundation := challenge.TargetID == challenge.InstanceID
	if !domain.ValidOperation(challenge.ID) || !validNonce(challenge.Nonce) ||
		!domain.ValidOperation(challenge.InstanceID) || !domain.ValidOperation(challenge.TargetID) ||
		(!foundation && (!domain.ValidOperation(challenge.BindingID) || challenge.Generation < 1)) ||
		(foundation && (proposal.Release.Manifest.Kind != "foundation" || challenge.BindingID != "" || challenge.Generation != 1)) ||
		!domain.ValidOperation(challenge.ProductID) || !validPlatform(challenge.Platform) ||
		!domain.ValidHash(challenge.PolicySHA256) || challenge.ExpiresAt <= 0 ||
		challenge.InstanceID != proposal.Release.Manifest.InstanceID || challenge.ProductID != proposal.Release.Manifest.ProductID {
		return domain.Fail(domain.Invalid, "software proposal requires an exact native challenge, identity and product")
	}
	switch challenge.State {
	case "absent":
		if challenge.CurrentSHA256 != "" || challenge.CurrentRelease != "" || challenge.CurrentVersion != "" {
			return domain.Fail(domain.Invalid, "a first-install challenge must prove absence, not an unknown installed source")
		}
	case "managed", "adoptable":
		if !domain.ValidHash(challenge.CurrentSHA256) || !domain.ValidOperation(challenge.CurrentRelease) ||
			!versionPattern.MatchString(challenge.CurrentVersion) {
			return domain.Fail(domain.Invalid, "software replacement requires an exact verified baseline")
		}
	default:
		return domain.Fail(domain.Invalid, "unknown software state cannot authorize installation")
	}
	if err := ValidateManifest(proposal.Release.Manifest); err != nil {
		return err
	}
	if proposal.Release.Revoked || proposal.Release.ID != proposal.Release.Manifest.ReleaseID {
		return domain.Fail(domain.Denied, "software proposal requires an unrevoked source-bound release")
	}
	if _, err := platformFor(proposal.Release, challenge.Platform); err != nil {
		return err
	}
	if _, err := age.ParseX25519Recipient(proposal.BackupRecipient); err != nil {
		return domain.Fail(domain.Invalid, "software installation requires a public off-host age recovery recipient")
	}
	return domain.CheckText(proposal.Reason, "software installation reason", 2048, true)
}

// VerifyPayloads compares a fully inspected bundle with its signed file list.
// Artifact digest and compressed size must be checked separately by its custodian.
func VerifyPayloads(payloads []domain.SoftwarePayload, platform domain.SoftwarePlatform) error {
	if err := validatePayloads(platform.Payloads); err != nil {
		return err
	}
	if err := validatePayloads(payloads); err != nil {
		return err
	}
	if !slices.Equal(sortedPayloads(payloads), sortedPayloads(platform.Payloads)) {
		return domain.Fail(domain.Conflict, "software bundle inventory differs from its signed platform")
	}
	return nil
}

func permitBytes(permit domain.SoftwarePermit) ([]byte, error) {
	permit.Signature = ""
	data, err := json.Marshal(permit)
	if len(data) > 2*MaxManifestBytes {
		return nil, domain.Fail(domain.Invalid, "software authorization exceeds its byte limit")
	}
	return append([]byte("foundation.native-software.authorization.v1\x00"), data...), err
}

func validatePermitFields(permit domain.SoftwarePermit) error {
	if err := ValidateProposal(permit.Proposal); err != nil {
		return err
	}
	if !domain.ValidOperation(permit.ID) || !domain.ValidOperation(permit.ActorID) ||
		!domain.ValidOperation(permit.ActorBindingID) || permit.ActorGeneration < 1 || permit.IssuedAt <= 0 ||
		permit.ExpiresAt <= permit.IssuedAt || permit.ExpiresAt-permit.IssuedAt > AuthorizationTTL {
		return domain.Fail(domain.Denied, "software authorization requires exact issuer identity and a bounded nonrenewable lifetime")
	}
	return nil
}

func SignPermit(permit domain.SoftwarePermit, caPrivatePEM []byte) (string, error) {
	if err := validatePermitFields(permit); err != nil {
		return "", err
	}
	data, err := permitBytes(permit)
	if err != nil {
		return "", err
	}
	key, err := privateKey(caPrivatePEM)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, data)), nil
}

func VerifyPermit(permit domain.SoftwarePermit, caPEM []byte, now time.Time) error {
	return verifyPermit(permit, caPEM, now, false)
}

// VerifyAdmittedPermit requires the caller's exact durable native admission proof.
func VerifyAdmittedPermit(permit domain.SoftwarePermit, caPEM []byte, now time.Time) error {
	return verifyPermit(permit, caPEM, now, true)
}

func verifyPermit(permit domain.SoftwarePermit, caPEM []byte, now time.Time, admitted bool) error {
	if err := validatePermitFields(permit); err != nil {
		return err
	}
	if permit.IssuedAt > now.Unix()+30 || !admitted && (permit.ExpiresAt <= now.Unix() ||
		permit.Proposal.Challenge.ExpiresAt <= now.Unix()) || permit.Proposal.Challenge.ExpiresAt < permit.IssuedAt {
		return domain.Fail(domain.Denied, "software authorization or its original native challenge is expired or not yet valid")
	}
	key, err := caKey(caPEM, now)
	if err != nil {
		return err
	}
	data, err := permitBytes(permit)
	if err != nil {
		return err
	}
	return verifySignature(key, data, permit.Signature)
}

func ValidateResult(permit domain.SoftwarePermit, result domain.SoftwareResult) error {
	platform, err := platformFor(permit.Proposal.Release, permit.Proposal.Challenge.Platform)
	if err != nil {
		return err
	}
	if result.PermitID != permit.ID || result.PermitSHA256 != domain.Hash(permit.Signature) ||
		result.ProductID != permit.Proposal.Release.Manifest.ProductID || result.ReleaseID != permit.Proposal.Release.ID ||
		result.ArtifactID != platform.ArtifactID || result.SHA256 != platform.SHA256 ||
		!slices.Contains([]string{"installed", "failed", "operator-required"}, result.State) ||
		result.State == "installed" && (!domain.ValidHash(result.BackupSHA256) || result.Code != "") {
		return domain.Fail(domain.Conflict, "software result does not bind the exact authorized source and native outcome")
	}
	if result.BackupSHA256 != "" && !domain.ValidHash(result.BackupSHA256) {
		return domain.Fail(domain.Invalid, "software backup receipt requires a SHA-256 digest")
	}
	return nil
}
