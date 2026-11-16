package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestInternalSoftwareBridgeRetainsPairedNativePolicyAndExactReceipts(t *testing.T) {
	n, recorder, _, recipient := nativeFixtureForUser(t)
	ctx := context.Background()
	n.Policy.EnableInternalSoftware = true
	n.Policy.InternalProductID = "product_foundation"
	n.Policy.DistributionPublicKeyFile = filepath.Join(filepath.Dir(n.Policy.CAFile), "distribution.pem")
	if err := os.WriteFile(n.Policy.DistributionPublicKeyFile, []byte("independent public fixture key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		if err := os.WriteFile(filepath.Join(n.Policy.BinaryDir, name), []byte("original "+name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte("verified source archive fixture")
	platform := domain.SoftwarePlatform{Platform: runtime.GOOS + "-" + runtime.GOARCH, ArtifactID: "artifact_internal", Size: int64(len(payload)), SHA256: domain.Hash(string(payload)), Profile: "archive"}
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		content := []byte("new " + name)
		platform.Payloads = append(platform.Payloads, domain.SoftwarePayload{Path: "bin/" + name, Mode: 0755, Size: int64(len(content)), SHA256: domain.Hash(string(content))})
	}
	verifications, fetches, stops := 0, 0, 0
	bridge := &SoftwareBridge{Native: n, Trust: SoftwareBridgeTrust{
		VerifyPermit: func(permit domain.SoftwarePermit, _ []byte, _ time.Time) error {
			verifications++
			if permit.Signature != "original-software-permission" {
				return domain.Fail(domain.Denied, "fixture original permission changed")
			}
			return nil
		},
		VerifyRelease: func(release domain.SoftwareRelease, _ []byte) error {
			if release.ID != "release_internal" || release.Manifest.Source.Commit != recorder.release.Commit {
				return domain.Fail(domain.Denied, "fixture source changed")
			}
			return nil
		},
		Fetch: func(_ context.Context, id, path string) error {
			fetches++
			if id != platform.ArtifactID {
				t.Fatal("updater selected another archive", id)
			}
			return os.WriteFile(path, payload, 0600)
		},
		Extract: func(data []byte, path string, entries []domain.SoftwarePayload) error {
			if string(data) != string(payload) {
				t.Fatal("source archive bytes changed")
			}
			if err := os.MkdirAll(filepath.Join(path, "bin"), 0700); err != nil {
				return err
			}
			for _, entry := range entries {
				if err := os.WriteFile(filepath.Join(path, entry.Path), []byte("new "+filepath.Base(entry.Path)), 0755); err != nil {
					return err
				}
			}
			return nil
		},
		Lease: n.Lease,
	}}
	recorder.clearFence = true
	n.CheckServices = func(context.Context) error {
		target, err := os.Readlink(filepath.Join(n.Policy.Prefix, "current"))
		if err != nil {
			return err
		}
		if !strings.Contains(target, "release_internal") {
			t.Fatal("activation did not use its immutable internal release", target)
		}
		recorder.newSource = true
		return nil
	}
	challenge, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "challenge", ID: "native_internal", ProductID: n.Policy.InternalProductID})
	if err != nil || challenge.Challenge == nil {
		t.Fatal("existing native inspection did not bind internal source", challenge, err)
	}
	if challenge.Challenge.State != "adoptable" || !strings.HasPrefix(challenge.Challenge.CurrentRelease, "baseline_") {
		t.Fatal("official installation was mislabeled as an internal release", challenge)
	}
	permit := domain.SoftwarePermit{ID: "software_permit_original", Signature: "original-software-permission", ActorID: "agent_founder", ActorBindingID: "agent_founder", ActorGeneration: 1,
		IssuedAt: n.Now().Unix(), ExpiresAt: n.Now().Unix() + 900,
		Proposal: domain.SoftwareProposal{Challenge: *challenge.Challenge, BackupRecipient: recipient.Recipient().String(), Reason: "Exact authorized bridge fixture",
			Release: domain.SoftwareRelease{ID: "release_internal", Manifest: domain.SoftwareManifest{Kind: "foundation", ProductID: n.Policy.InternalProductID,
				Version: recorder.release.Version, Source: domain.TaskSource{Commit: recorder.release.Commit}, Platforms: []domain.SoftwarePlatform{platform}}}}}
	prepared, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "prepare", ID: "native_internal", ProductID: n.Policy.InternalProductID, Permit: &permit})
	if err != nil || prepared.State != "prepared" {
		t.Fatal("exact paired stop/backup did not prepare", prepared, err)
	}
	for _, call := range recorder.calls {
		if strings.Contains(strings.Join(call.Args, " "), "bootout") || strings.Contains(strings.Join(call.Args, " "), "stop") {
			stops++
		}
	}
	recorder.newSource = true
	result, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "apply", ID: "native_internal", ProductID: n.Policy.InternalProductID, Permit: &permit})
	if err != nil || result.State != "verified" || result.Result == nil || result.Result.PermitID != permit.ID || !domain.ValidHash(result.Result.BackupSHA256) {
		t.Fatal("bridge did not verify original software receipt", result, err)
	}
	before := len(recorder.calls)
	if _, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "prepare", ID: "native_internal", ProductID: n.Policy.InternalProductID, Permit: &permit}); err != nil {
		t.Fatal(err)
	}
	for _, call := range recorder.calls[before:] {
		if strings.Contains(strings.Join(call.Args, " "), "bootout") || strings.Contains(strings.Join(call.Args, " "), "stop") {
			t.Fatal("verified replay repeated manager effects")
		}
	}
	if fetches != 1 || verifications < 3 || stops == 0 {
		t.Fatal("original permission/source/native stop gates were not exercised", fetches, verifications, stops)
	}
	pending, err := bridge.PendingResults()
	if err != nil || len(pending) != 1 || pending[0].PermitID != permit.ID {
		t.Fatal("native outbox lost exact permission", pending, err)
	}
	if err := bridge.AcknowledgeResult(permit.ID, domain.Hash(permit.Signature)); err != nil {
		t.Fatal(err)
	}
	pending, err = bridge.PendingResults()
	if err != nil || len(pending) != 0 {
		t.Fatal("acknowledgement did not settle only the delivered receipt", pending, err)
	}
}
