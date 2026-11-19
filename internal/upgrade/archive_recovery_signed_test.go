package upgrade_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/upgrade"
)

func TestInternalSoftwareArchiveRecoveryRealSignatures(t *testing.T) {
	for _, change := range []string{"none", "self-founder-original-binding", "instance", "product", "platform", "source", "payload-hash", "revoked", "tampered", "wrong-key", "ca", "permit", "policy"} {
		t.Run(change, func(t *testing.T) {
			b, req, path := upgrade.ArchiveRecoveryTestFixture(t, change == "self-founder-original-binding")
			var job upgrade.ArchiveRecoveryTestJournal
			var baseline domain.SoftwareRelease
			if err := config.Read(path, &job); err != nil {
				t.Fatal(err)
			}
			if err := config.Read(req.BaselineReleaseFile, &baseline); err != nil {
				t.Fatal(err)
			}
			ca, err := pki.Generate(b.Native.Now())
			if err != nil {
				t.Fatal(err)
			}
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			privateDER, err := x509.MarshalPKCS8PrivateKey(private)
			if err != nil {
				t.Fatal(err)
			}
			publicDER, err := x509.MarshalPKIXPublicKey(public)
			if err != nil {
				t.Fatal(err)
			}
			privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
			publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
			if err := os.WriteFile(b.Native.Policy.CAFile, ca.CA, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.Native.Policy.DistributionPublicKeyFile, publicPEM, 0600); err != nil {
				t.Fatal(err)
			}
			sign := func(release *domain.SoftwareRelease) {
				t.Helper()
				m := &release.Manifest
				m.FormatVersion, m.InstanceID, m.ReleaseID = 1, b.Native.Policy.InstanceID, release.ID
				m.ProjectID, m.Revision, m.CreatedAt = "project_fixture", 1, b.Native.Now().Unix()
				m.Source.RepositoryID = "repository_fixture"
				release.KeyID, release.Signature, err = software.SignRelease(*m, privatePEM)
				if err != nil {
					t.Fatal(err)
				}
			}
			sign(&baseline)
			sign(&job.Permit.Proposal.Release)
			switch change {
			case "instance":
				baseline.Manifest.InstanceID = "instance_foreign"
			case "product":
				baseline.Manifest.ProductID = "product_foreign"
			case "platform":
				other := "darwin-arm64"
				if baseline.Manifest.Platforms[0].Platform == other {
					other = "linux-amd64"
				}
				baseline.Manifest.Platforms[0].Platform = other
			case "source":
				baseline.Manifest.Source.Commit = strings.Repeat("c", 40)
			case "payload-hash":
				baseline.Manifest.Platforms[0].Payloads[0].SHA256 = domain.Hash("foreign")
			case "revoked":
				baseline.Revoked = true
			}
			if change == "instance" || change == "product" || change == "platform" || change == "source" || change == "payload-hash" {
				baseline.KeyID, baseline.Signature, err = software.SignRelease(baseline.Manifest, privatePEM)
				if err != nil {
					t.Fatal(err)
				}
			}
			if change == "tampered" {
				baseline.Manifest.Version = "0.3.1"
			}
			if change == "wrong-key" {
				baseline.KeyID = domain.Hash("wrong key")
			}
			job.Permit.Signature, err = software.SignPermit(job.Permit, ca.CAKey)
			if err != nil {
				t.Fatal(err)
			}
			if change == "permit" {
				job.Permit.Proposal.Reason = "tampered after signing"
			}
			if change == "ca" {
				other, err := pki.Generate(b.Native.Now())
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(b.Native.Policy.CAFile, other.CA, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if change == "policy" {
				b.Native.Policy.Name = "changed"
			}
			b.Trust.VerifyRelease = software.VerifyRelease
			b.Trust.VerifyAdmittedPermit = software.VerifyAdmittedPermit
			if err := config.Write(req.BaselineReleaseFile, baseline); err != nil {
				t.Fatal(err)
			}
			if err := config.Write(path, job); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedJournalSHA256, req.ExpectedPermitSHA256 = domain.Hash(string(raw)), domain.Hash(job.Permit.Signature)
			receipt, err := b.AbortBeforeManagedActivation(context.Background(), req)
			if change == "none" || change == "self-founder-original-binding" {
				if err != nil || receipt.Result.State != "failed" {
					t.Fatal("real distribution and CA signatures did not recover exact original job", receipt, err)
				}
				if err := software.ValidateResult(job.Permit, receipt.Result); err != nil {
					t.Fatal("failed settlement cannot be reported", err)
				}
			} else {
				if err == nil {
					t.Fatal("foreign or tampered signed proof accepted", change)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != string(raw) {
					t.Fatal("denial changed original journal", readErr)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)); !os.IsNotExist(err) {
					t.Fatal("denial prepared recovery intent", err)
				}
			}
		})
	}
}
