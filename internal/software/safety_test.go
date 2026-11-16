package software

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func TestNativeCurrentCorruptionCannotProduceAnotherEffectOrDeliverableResult(t *testing.T) {
	for _, name := range []string{"receipt", "payload", "backup", "prepared-proof", "outbox"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			useServiceFixture(fixture, "linux")
			permit := fixture.permit(t, "corruption-"+name, fixture.release(t, 1))
			applyPermit(t, fixture.Native, permit)
			calls, leases, fetches := len(fixture.Recorder.calls), fixture.Leases, fixture.Fetches
			switch name {
			case "receipt":
				writeTestFile(t, fixture.Native.receiptFilename(permit.Proposal.Challenge.ID), []byte("{}"), 0600)
			case "payload":
				filename := filepath.Join(fixture.Native.Policy.Prefix, "current", "bin/tool")
				if err := os.Chmod(filename, 0755); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filename, []byte("changed native payload"), 0555)
			case "backup":
				writeTestFile(t, fixture.Native.backupFilename(permit.Proposal.Challenge.ID), []byte("corrupted cold backup"), 0600)
			case "prepared-proof":
				writeTestFile(t, fixture.Native.proofFilename(permit.Proposal.Challenge.ID), []byte("{}"), 0600)
			case "outbox":
				filename := filepath.Join(fixture.Native.Policy.StateDir, "outbox", resultKey(permit.ID))
				writeTestFile(t, filename, []byte("{}"), 0600)
			}
			if _, err := fixture.Native.PendingResults(); err == nil {
				t.Fatal("corrupted native provenance was reported as deliverable installed evidence")
			}
			if name != "outbox" {
				if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "status", ID: permit.Proposal.Challenge.ID}); err == nil {
					t.Fatal("corrupted native receipt was reported as checked installation")
				}
			}
			if len(fixture.Recorder.calls) != calls || fixture.Leases != leases || fixture.Fetches != fetches {
				t.Fatal("native corruption caused an effect, fresh lease or artifact retry")
			}
		})
	}
}

func TestNativeInterruptedUnprovenPhasePreservesOperatorFence(t *testing.T) {
	fixture := nativeFixture(t)
	useServiceFixture(fixture, "linux")
	permit := fixture.permit(t, "interrupted-before-proof", fixture.release(t, 1))
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err != nil {
		t.Fatal(err)
	}
	var job nativeJob
	if err := readRecord(fixture.Native.jobFilename(permit.Proposal.Challenge.ID), &job); err != nil {
		t.Fatal(err)
	}
	job.State, job.Effects = "stopping", true
	if err := fixture.Native.saveJob(job, false); err != nil {
		t.Fatal(err)
	}
	calls := len(fixture.Recorder.calls)
	reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "status", ID: permit.Proposal.Challenge.ID})
	if err == nil || reply.State != "operator-required" || reply.Result.Code != "stopping" {
		t.Fatal("unproven interrupted effects were not retained as operator-required", reply, err)
	}
	pending, err := fixture.Native.PendingResults()
	if err != nil || len(pending) != 1 || pending[0].State != "operator-required" {
		t.Fatal("operator-required result was not durably retained for its original source", pending, err)
	}
	fixture.Now = fixture.Now.Add(2 * time.Hour)
	_, _ = fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
	if len(fixture.Recorder.calls) != calls || fixture.Leases != 0 {
		t.Fatal("an unknown interrupted native phase was adopted or rerun")
	}
}

func TestNativeFreshLeaseRecheckedAfterAdmissionInspection(t *testing.T) {
	fixture := nativeFixture(t)
	useServiceFixture(fixture, "linux")
	permit := fixture.permit(t, "lease-expiring-during-inspection", fixture.release(t, 1))
	advanced := false
	fixture.Native.requirements = func(context.Context, Policy) error {
		if fixture.Leases > 0 && !advanced {
			fixture.Now = fixture.Now.Add(31 * time.Second)
			advanced = true
		}
		return nil
	}
	reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
	if err == nil || reply.State == "installed" || len(fixture.Recorder.effects) != 0 {
		t.Fatal("inspection consumed the signed lease lifetime but native effects were still started", reply, err)
	}
	if _, err := os.Lstat(fixture.Native.Policy.Prefix); !errors.Is(err, os.ErrNotExist) || fixture.Leases != 1 {
		t.Fatal("expired admission renewed its proof or modified installation", err)
	}
}

func TestNativeServicePreservesManualInactiveIntentAndRejectsReplacement(t *testing.T) {
	fixture := nativeFixture(t)
	useServiceFixture(fixture, "linux")
	first := fixture.permit(t, "prior-service-intent", fixture.release(t, 1))
	applyPermit(t, fixture.Native, first)
	fixture.Recorder.running, fixture.Recorder.enabled, fixture.Recorder.pid = false, false, 0
	next := fixture.permit(t, "preserve-inactive-intent", fixture.release(t, 2))
	applyPermit(t, fixture.Native, next)
	if fixture.Recorder.running || fixture.Recorder.enabled {
		t.Fatal("native upgrade enabled or started a manually inactive/disabled service")
	}
	fixture.Recorder.running, fixture.Recorder.pid = true, 999
	changed := fixture.permit(t, "service-generation-change", fixture.release(t, 3))
	fixture.Recorder.pid++
	calls, fetches, leases := len(fixture.Recorder.effects), fixture.Fetches, fixture.Leases
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: changed.Proposal.Challenge.ID, Permit: &changed}); err == nil {
		t.Fatal("a different live native service generation was stopped under an old challenge")
	}
	if len(fixture.Recorder.effects) != calls || fixture.Leases != leases || fixture.Fetches != fetches {
		t.Fatal("changed service generation caused a native effect")
	}
	fixture.Recorder.dropIns = "/unmanaged/override.conf"
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "unmanaged-overrides"}); err == nil {
		t.Fatal("native service overrides were adopted")
	}
}

func TestNativeUserServiceAdoptionIsExactAndLeavesOriginalBytes(t *testing.T) {
	fixture := nativeFixture(t)
	useServiceFixture(fixture, "linux")
	baseline := fixture.release(t, 1)
	root := filepath.Join(fixture.Root, "baseline-service")
	platform := baseline.Manifest.Platforms[0]
	if err := ExtractBundle(fixture.Artifacts[platform.ArtifactID], root, platform.Payloads); err != nil {
		t.Fatal(err)
	}
	fixture.Native.Policy.Adoption = &Baseline{Release: baseline, Path: root}
	fixture.Recorder.loaded, fixture.Recorder.running, fixture.Recorder.enabled = true, true, true
	fixture.Recorder.pid, fixture.Recorder.activeRoot = 800, root
	writeTestFile(t, fixture.Native.serviceFilename(fixture.Native.Policy),
		serviceDefinition(fixture.Native.Policy, fixture.Native.hostIdentity(), root), 0600)
	permit := fixture.permit(t, "adopt-service-baseline", baseline)
	applyPermit(t, fixture.Native, permit)
	if !fixture.Recorder.running || fixture.Recorder.activeRoot != filepath.Join(fixture.Native.Policy.Prefix, "current") {
		t.Fatal("approved service adoption did not bind the new immutable activation")
	}
	if err := verifyTree(root, platform.Payloads, false); err != nil {
		t.Fatal("approved service adoption modified original unmanaged bytes", err)
	}
}

func TestNativeExplicitManagedReceiptAnchorAndDependencies(t *testing.T) {
	fixture := nativeFixture(t)
	firstRelease := fixture.release(t, 1)
	first := fixture.permit(t, "managed-baseline", firstRelease)
	applyPermit(t, fixture.Native, first)
	receipt, hash, err := fixture.Native.readReceipt(first.Proposal.Challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Native.AcknowledgeResult(first.ID, domain.Hash(first.Signature)); err != nil {
		t.Fatal(err)
	}
	fixture.Native.Policy.Generation++
	fixture.Native.Policy.Current = &Baseline{Release: firstRelease, Path: fixture.Native.Policy.Prefix,
		ReceiptFile: fixture.Native.receiptFilename(first.Proposal.Challenge.ID), ReceiptSHA256: hash}
	second := fixture.permit(t, "enrolled-higher-key-generation", fixture.release(t, 2))
	applyPermit(t, fixture.Native, second)
	if pending, err := fixture.Native.PendingResults(); err != nil || len(pending) != 1 || pending[0].PermitID != second.ID {
		t.Fatal("explicit managed enrollment lost historical acknowledged state or current pending outcome", pending, err)
	}
	consumer := nativeFixture(t)
	consumer.Native.Policy.ProductID = "consumer_fixture"
	consumer.Native.Policy.Generation = fixture.Native.Policy.Generation
	consumer.Native.Policy.Platform = fixture.Native.Policy.Platform
	consumer.Native.Policy.CAFile = fixture.Native.Policy.CAFile
	consumer.Native.Policy.PublicKeyFile = fixture.Native.Policy.PublicKeyFile
	consumer.PrivateKey, consumer.PublicKey, consumer.CAKey, consumer.CA = fixture.PrivateKey, fixture.PublicKey, fixture.CAKey, fixture.CA
	dependency := domain.SoftwareDependency{ProductID: firstRelease.Manifest.ProductID, ReleaseID: firstRelease.ID,
		Platform: firstRelease.Manifest.Platforms[0].Platform, SHA256: firstRelease.Manifest.Platforms[0].SHA256, Purpose: "exact immutable tool dependency"}
	consumer.Native.Policy.Dependencies = []DependencyPolicy{{Reference: dependency, Prefix: fixture.Native.Policy.Prefix,
		ReceiptFile: fixture.Native.receiptFilename(first.Proposal.Challenge.ID), ReceiptSHA256: hash, PolicySHA256: receipt.PolicySHA256}}
	release := consumer.release(t, 1)
	release.Manifest.Platforms[0].Dependencies = []domain.SoftwareDependency{dependency}
	consumer.signRelease(t, &release)
	permit := consumer.permit(t, "exact-dependency", release)
	applyPermit(t, consumer.Native, permit)
	consumer.Native.Policy.Dependencies[0].ReceiptSHA256 = domain.Hash("missing dependency proof")
	if _, err := consumer.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err == nil {
		t.Fatal("changed dependency receipt policy retained native authority")
	}
}

func TestNativeBackupLinksMissingSupervisionRootNamespacesAndSharedSlotDenied(t *testing.T) {
	for _, name := range []string{"symlink", "hardlink", "requirements", "root-prefix", "invalid-path-encoding", "slot"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			release := fixture.release(t, 1)
			switch name {
			case "symlink":
				if err := os.Symlink("state.json", filepath.Join(fixture.Root, "data", "link")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(fixture.Root, "data", "state.json"), filepath.Join(fixture.Root, "data", "link")); err != nil {
					t.Fatal(err)
				}
			case "requirements":
				fixture.Native.requirements = func(context.Context, Policy) error {
					return domain.Fail(domain.Prerequisite, "independent native service supervision is unavailable")
				}
			case "root-prefix":
				useDebianFixture(fixture)
				fixture.Native.Policy.PackageRoot = "/etc"
			case "invalid-path-encoding":
				fixture.Native.Policy.Prefix += string([]byte{255})
			case "slot":
				permit := fixture.permit(t, "shared-native-slot", release)
				lock, err := secure.Acquire(filepath.Join(fixture.Native.Policy.StateDir, "software.lock"))
				if err != nil {
					t.Fatal(err)
				}
				_, applyErr := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
					Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				if applyErr == nil {
					t.Fatal("another native slot owner was taken over")
				}
				requireNoPrefix(t, fixture)
				return
			}
			if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "unsafe-prerequisite"}); err == nil {
				t.Fatal("unsafe native prerequisite was ignored")
			}
			requireNoPrefix(t, fixture)
		})
	}
}
