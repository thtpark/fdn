package software

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/upgrade"
)

func signatureFixture(t *testing.T) (domain.SoftwarePermit, []byte, []byte, []byte, []byte, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	private, public := distributionKeys(t)
	ca, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.SoftwareManifest{FormatVersion: 1, InstanceID: "instance_fixture", ProductID: "product_fixture",
		ProjectID: "project_fixture", ReleaseID: "release_fixture", Kind: "tool", Version: "1.2.3", Revision: 4, CreatedAt: now.Unix(),
		Source: domain.TaskSource{RepositoryID: "repository_fixture", Commit: strings.Repeat("a", 40)},
		Platforms: []domain.SoftwarePlatform{{Platform: "linux-amd64", ArtifactID: "artifact_fixture", SHA256: domain.Hash("bundle"), Size: 100,
			Profile: "archive", Payloads: []domain.SoftwarePayload{{Path: "bin/tool", Mode: 0755, Size: 4, SHA256: domain.Hash("tool")}}}}}
	keyID, signature, err := SignRelease(manifest, private)
	if err != nil {
		t.Fatal(err)
	}
	release := domain.SoftwareRelease{ID: manifest.ReleaseID, Manifest: manifest, KeyID: keyID, Signature: signature}
	permit := domain.SoftwarePermit{ID: "permit_fixture", ActorID: "founder_fixture", ActorBindingID: "device_founder",
		ActorGeneration: 2, IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 900,
		Proposal: domain.SoftwareProposal{Release: release, Reason: "Explicit native fixture", BackupRecipient: recovery.Recipient().String(),
			Challenge: domain.SoftwareChallenge{ID: "challenge_fixture", Nonce: domain.NewToken(), InstanceID: manifest.InstanceID,
				ProductID: manifest.ProductID, TargetID: "agent_fixture", BindingID: "device_fixture", Generation: 3,
				Platform: "linux-amd64", State: "absent", PolicySHA256: domain.Hash("policy"), ExpiresAt: now.Unix() + 900}}}
	permit.Signature, err = SignPermit(permit, ca.CAKey)
	if err != nil {
		t.Fatal(err)
	}
	return permit, private, public, ca.CAKey, ca.CA, now
}

func TestSeparateReleaseAndPermitSignaturesCanonicalAndTamperBound(t *testing.T) {
	permit, private, public, caPrivate, ca, now := signatureFixture(t)
	if err := VerifyRelease(permit.Proposal.Release, public); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, ca, now); err != nil {
		t.Fatal(err)
	}
	before, err := CanonicalManifest(permit.Proposal.Release.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(before, []byte("{")) {
		t.Fatal("canonical manifest unexpectedly contains signing context")
	}
	keyID, signature, err := SignRelease(permit.Proposal.Release.Manifest, private)
	if err != nil || keyID != permit.Proposal.Release.KeyID || signature != permit.Proposal.Release.Signature {
		t.Fatal("release signing is not deterministic", err)
	}
	for name, change := range map[string]func(*domain.SoftwarePermit){
		"source":  func(p *domain.SoftwarePermit) { p.Proposal.Release.Manifest.Source.Commit = strings.Repeat("b", 40) },
		"product": func(p *domain.SoftwarePermit) { p.Proposal.Release.Manifest.ProductID = "product_other" },
		"target":  func(p *domain.SoftwarePermit) { p.Proposal.Challenge.TargetID = "agent_other" },
		"binding": func(p *domain.SoftwarePermit) { p.Proposal.Challenge.BindingID = "device_other" },
		"keygen":  func(p *domain.SoftwarePermit) { p.Proposal.Challenge.Generation++ },
		"issuer":  func(p *domain.SoftwarePermit) { p.ActorGeneration++ },
		"policy":  func(p *domain.SoftwarePermit) { p.Proposal.Challenge.PolicySHA256 = domain.Hash("new policy") },
		"nonce":   func(p *domain.SoftwarePermit) { p.Proposal.Challenge.Nonce = domain.NewToken() },
		"digest": func(p *domain.SoftwarePermit) {
			p.Proposal.Release.Manifest.Platforms[0].SHA256 = domain.Hash("new bundle")
		},
		"platform":  func(p *domain.SoftwarePermit) { p.Proposal.Challenge.Platform = "darwin-arm64" },
		"signature": func(p *domain.SoftwarePermit) { p.Signature = strings.Repeat("A", 86) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := clonePermit(t, permit)
			change(&changed)
			if err := VerifyPermit(changed, ca, now); err == nil {
				t.Fatal("changed authorization was accepted")
			}
		})
	}
	changed := clonePermit(t, permit).Proposal.Release
	changed.Manifest.Source.Commit = strings.Repeat("b", 40)
	if err := VerifyRelease(changed, public); err == nil {
		t.Fatal("changed distribution source was accepted")
	}
	otherPrivate, otherPublic := distributionKeys(t)
	if err := VerifyRelease(permit.Proposal.Release, otherPublic); err == nil {
		t.Fatal("wrong distribution pin was accepted")
	}
	if _, _, err := SignRelease(permit.Proposal.Release.Manifest, otherPrivate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SignRelease(permit.Proposal.Release.Manifest, public); err == nil {
		t.Fatal("public key was admitted as distribution signer")
	}
	leaseRequest := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	lease := domain.UpgradeLease{PermitSHA256: leaseRequest.PermitSHA256, Nonce: leaseRequest.Nonce,
		IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 30, Signature: permit.Signature}
	if err := upgrade.VerifyLease(lease, leaseRequest, ca, now); err == nil {
		t.Fatal("software permit signature was reused as start-lease authority")
	}
	lease.Signature, err = upgrade.SignLease(lease, caPrivate)
	if err != nil || upgrade.VerifyLease(lease, leaseRequest, ca, now) != nil {
		t.Fatal("reused narrow lease helper failed", err)
	}
}

func TestCanonicalManifestSortsWithoutMutatingAndRejectsAmbiguity(t *testing.T) {
	permit, _, _, _, _, _ := signatureFixture(t)
	manifest := clonePermit(t, permit).Proposal.Release.Manifest
	manifest.Platforms[0].Payloads = append(manifest.Platforms[0].Payloads,
		domain.SoftwarePayload{Path: "a", Mode: 0644, Size: 0, SHA256: domain.Hash("")})
	original := manifest.Platforms[0].Payloads[0].Path
	first, err := CanonicalManifest(manifest)
	if err != nil || manifest.Platforms[0].Payloads[0].Path != original {
		t.Fatal("canonicalization mutated caller", err)
	}
	manifest.Platforms[0].Payloads[0], manifest.Platforms[0].Payloads[1] =
		manifest.Platforms[0].Payloads[1], manifest.Platforms[0].Payloads[0]
	second, err := CanonicalManifest(manifest)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("inventory ordering changed canonical bytes", err)
	}
	manifest.Platforms = append(manifest.Platforms, manifest.Platforms[0])
	if _, err := CanonicalManifest(manifest); err == nil {
		t.Fatal("duplicate platform was canonicalized")
	}
	invalid := clonePermit(t, permit).Proposal.Release.Manifest
	invalid.Source.Ref = "refs/heads/" + string([]byte{255})
	if _, err := CanonicalManifest(invalid); err == nil {
		t.Fatal("invalid UTF-8 source identity was canonicalized into an alias")
	}
}

func TestPermitExpiryPinnedCAAndFoundationBindingFailClosed(t *testing.T) {
	permit, _, _, caKeyPEM, caPEM, now := signatureFixture(t)
	if err := VerifyPermit(permit, caPEM, time.Unix(permit.ExpiresAt, 0)); err == nil {
		t.Fatal("expired permit was accepted")
	}
	changed := clonePermit(t, permit)
	changed.ExpiresAt = changed.IssuedAt + 901
	if _, err := SignPermit(changed, caKeyPEM); err == nil {
		t.Fatal("unbounded authorization lifetime was signed")
	}
	changed = clonePermit(t, permit)
	changed.Proposal.Challenge.ExpiresAt = now.Unix()
	changed.Signature, _ = SignPermit(changed, caKeyPEM)
	if err := VerifyPermit(changed, caPEM, now); err == nil {
		t.Fatal("expired challenge was accepted")
	}
	otherCA, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, otherCA.CA, now); err == nil {
		t.Fatal("foreign authorization CA was accepted")
	}
	if err := VerifyPermit(permit, append(append([]byte{}, caPEM...), caPEM...), now); err == nil {
		t.Fatal("ambiguous CA pin was accepted")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(permit.Signature)
	if err != nil {
		t.Fatal(err)
	}
	changed = clonePermit(t, permit)
	changed.Signature = base64.StdEncoding.EncodeToString(decoded)
	if err := VerifyPermit(changed, caPEM, now); err == nil {
		t.Fatal("noncanonical signature representation was accepted")
	}
	foundation := clonePermit(t, permit)
	foundation.Proposal.Release.Manifest.Kind = "foundation"
	foundation.Proposal.Challenge.TargetID = foundation.Proposal.Challenge.InstanceID
	foundation.Proposal.Challenge.BindingID = ""
	foundation.Proposal.Challenge.Generation = 1
	foundation.Signature, err = SignPermit(foundation, caKeyPEM)
	if err != nil || VerifyPermit(foundation, caPEM, now) != nil {
		t.Fatal("Foundation authorization binding is not recognized", err)
	}
	foundation.Proposal.Challenge.TargetID = "another_target"
	if _, err := SignPermit(foundation, caKeyPEM); err == nil {
		t.Fatal("foreign Foundation target was signed")
	}
	key, err := privateKey(caKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), now); err == nil {
		t.Fatal("authorization without a CA certificate was accepted")
	}
}

func TestRecipeValidationIsBoundedAndNeverExecutes(t *testing.T) {
	recipe := domain.BuildRecipe{FormatVersion: 1, Version: "1.0.0", Platform: "linux-amd64", Profile: "archive",
		TimeoutSeconds: 300, Steps: []domain.BuildStep{{Args: []string{"go", "build", "-o", "bin/tool", "./cmd/tool"}}},
		Outputs: []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}}}
	if err := ValidateRecipe(recipe); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*domain.BuildRecipe){
		"format":   func(r *domain.BuildRecipe) { r.FormatVersion = 2 },
		"timeout":  func(r *domain.BuildRecipe) { r.TimeoutSeconds = 3601 },
		"platform": func(r *domain.BuildRecipe) { r.Platform = "windows-amd64" },
		"profile":  func(r *domain.BuildRecipe) { r.Profile = "shell-hook" },
		"mode":     func(r *domain.BuildRecipe) { r.Outputs = []domain.BuildOutput{{Path: "bin/tool", Mode: 04755}} },
		"path":     func(r *domain.BuildRecipe) { r.Outputs = []domain.BuildOutput{{Path: "../tool", Mode: 0755}} },
		"args":     func(r *domain.BuildRecipe) { r.Steps = []domain.BuildStep{{Args: []string{"go", "build\x00"}}} },
		"duplicate": func(r *domain.BuildRecipe) {
			r.Outputs = append([]domain.BuildOutput{}, r.Outputs[0], r.Outputs[0])
		},
		"dependency": func(r *domain.BuildRecipe) {
			r.Dependencies = []domain.SoftwareDependency{{ProductID: "dependency", ReleaseID: "release_dep", Platform: "darwin-arm64",
				SHA256: domain.Hash("dependency"), Purpose: "wrong platform"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := recipe
			change(&changed)
			if err := ValidateRecipe(changed); err == nil {
				t.Fatal("invalid recipe was accepted")
			}
		})
	}
}
