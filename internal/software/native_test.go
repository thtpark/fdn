package software

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/upgrade"
)

func TestNativeArchiveFirstInstallUpgradeEncryptedBackupAndExactReplay(t *testing.T) {
	fixture := nativeFixture(t)
	first := fixture.release(t, 1)
	permit := fixture.permit(t, "first-install", first)
	if permit.Proposal.Challenge.State != "absent" || permit.Proposal.Challenge.CurrentSHA256 != "" {
		t.Fatal("new install did not prove native absence")
	}
	reply := applyPermit(t, fixture.Native, permit)
	if reply.Result.ResumeSafe || fixture.Fetches != 1 || fixture.Leases != 1 || len(fixture.Recorder.calls) != 0 {
		t.Fatal("portable install claimed resumption safety or invoked a process")
	}
	if err := ValidateResult(permit, *reply.Result); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := fixture.Native.readReceipt(permit.Proposal.Challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(fixture.Native.Policy.Prefix, "current", "bin/tool"))
	if err != nil || !bytes.Contains(content, []byte("revision 1")) {
		t.Fatal("exact first version is not activated", err)
	}
	for _, action := range []string{"status", "prepare", "apply"} {
		request := domain.SoftwareNativeRequest{Action: action, ID: permit.Proposal.Challenge.ID}
		if action != "status" {
			request.Permit = &permit
		}
		replayed, err := fixture.Native.Handle(context.Background(), request)
		if err != nil || !equalJSON(replayed.Result, reply.Result) {
			t.Fatalf("receipt replay changed: %#v %v", replayed, err)
		}
	}
	pending, err := fixture.Native.PendingResults()
	if err != nil || len(pending) != 1 || !equalJSON(pending[0], *reply.Result) {
		t.Fatal("durable native outbox is not source-bound", pending, err)
	}
	if err := fixture.Native.AcknowledgeResult(permit.ID, domain.Hash("wrong permit")); err == nil {
		t.Fatal("foreign outbox acknowledgement was accepted")
	}
	if err := fixture.Native.AcknowledgeResult(permit.ID, domain.Hash(permit.Signature)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Native.AcknowledgeResult(permit.ID, domain.Hash(permit.Signature)); err != nil {
		t.Fatal(err)
	}
	pending, err = fixture.Native.PendingResults()
	if err != nil || len(pending) != 0 {
		t.Fatal("result acknowledgement did not retain exact delivered state", err)
	}
	second := fixture.release(t, 2)
	next := fixture.permit(t, "archive-upgrade", second)
	if next.Proposal.Challenge.State != "managed" || next.Proposal.Challenge.CurrentRelease != first.ID {
		t.Fatal("upgrade did not bind the current managed receipt")
	}
	upgraded := applyPermit(t, fixture.Native, next)
	if fixture.Fetches != 2 || fixture.Leases != 2 || len(fixture.Recorder.calls) != 0 || upgraded.Result.ResumeSafe {
		t.Fatal("archive upgrade repeated custody/authority or claimed automatic AI resumption")
	}
	if err := verifyTree(receipt.VersionDir, receipt.Payloads, true); err != nil {
		t.Fatal("upgrade overwrote an immutable old version", err)
	}
	backupFile, err := os.Open(fixture.Native.backupFilename(next.Proposal.Challenge.ID))
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(backupFile, fixture.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	foundState, foundOld, foundManifest := false, false, false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "data-00/state.json" {
			foundState = string(body) == "original user state, keys and journals"
		}
		if strings.HasSuffix(header.Name, "/bin/tool") && bytes.Contains(body, []byte("revision 1")) {
			foundOld = true
		}
		if header.Name == "snapshot.json" {
			var manifest backupManifest
			if err := decodeJSON(body, &manifest); err != nil {
				t.Fatal(err)
			}
			foundManifest = manifest.PermitSHA256 == domain.Hash(next.Signature) && manifest.Current.ReceiptID == permit.Proposal.Challenge.ID &&
				len(manifest.CurrentRecord) != 0 && len(manifest.ReceiptRecord) != 0
		}
	}
	if err := errors.Join(gz.Close(), backupFile.Close()); err != nil {
		t.Fatal(err)
	}
	if !foundState || !foundOld || !foundManifest {
		t.Fatal("encrypted cold backup omitted baseline bytes, original state or exact receipt provenance")
	}
	state, err := os.ReadFile(filepath.Join(fixture.Root, "data", "state.json"))
	if err != nil || string(state) != "original user state, keys and journals" {
		t.Fatal("user state was changed", err)
	}
	applyPermit(t, fixture.Native, permit)
	activated, err := os.ReadFile(filepath.Join(fixture.Native.Policy.Prefix, "current", "bin/tool"))
	if err != nil || !bytes.Contains(activated, []byte("revision 2")) || fixture.Leases != 2 || fixture.Fetches != 2 {
		t.Fatal("old receipt replay rolled back current software or reran effects", err)
	}
}

func TestNativeUnknownInstallRefusedAndExplicitVerifiedAdoption(t *testing.T) {
	unknown := nativeFixture(t)
	writeTestFile(t, filepath.Join(unknown.Native.Policy.Prefix, "unmanaged"), []byte("preserve unknown user bytes"), 0600)
	if _, err := unknown.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "unknown"}); err == nil {
		t.Fatal("unknown installation was treated as absence or silently adopted")
	}
	data, err := os.ReadFile(filepath.Join(unknown.Native.Policy.Prefix, "unmanaged"))
	if err != nil || string(data) != "preserve unknown user bytes" {
		t.Fatal("unknown user bytes were modified", err)
	}
	adoption := nativeFixture(t)
	baseline := adoption.release(t, 1)
	unmanaged := filepath.Join(adoption.Root, "approved-baseline")
	platform := baseline.Manifest.Platforms[0]
	if err := ExtractBundle(adoption.Artifacts[platform.ArtifactID], unmanaged, platform.Payloads); err != nil {
		t.Fatal(err)
	}
	adoption.Native.Policy.Adoption = &Baseline{Release: baseline, Path: unmanaged}
	permit := adoption.permit(t, "approved-adoption", baseline)
	if permit.Proposal.Challenge.State != "adoptable" {
		t.Fatal("adoption challenge did not verify the exact signed baseline")
	}
	applyPermit(t, adoption.Native, permit)
	if err := verifyTree(unmanaged, platform.Payloads, false); err != nil {
		t.Fatal("approved adoption modified the original unmanaged installation", err)
	}
	same := adoption.permit(t, "forbidden-same-revision", baseline)
	if _, err := adoption.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: same.Proposal.Challenge.ID, Permit: &same}); err == nil {
		t.Fatal("managed same-revision replacement was allowed")
	}
}

func TestNativeChangedApprovalTargetSourcePolicyAndProfileFailBeforeEffects(t *testing.T) {
	for _, name := range []string{"source", "target", "binding", "generation", "signature", "distribution", "profile", "platform", "policy"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			release := fixture.release(t, 1)
			permit := fixture.permit(t, "changed-"+name, release)
			switch name {
			case "source":
				permit.Proposal.Release.Manifest.Source.Commit = strings.Repeat("b", 40)
			case "target":
				permit.Proposal.Challenge.TargetID = "different_target"
				fixture.signPermit(t, &permit)
			case "binding":
				permit.Proposal.Challenge.BindingID = "different_binding"
				fixture.signPermit(t, &permit)
			case "generation":
				permit.Proposal.Challenge.Generation++
				fixture.signPermit(t, &permit)
			case "signature":
				permit.Signature = strings.Repeat("A", 86)
			case "distribution":
				permit.Proposal.Release.Signature = strings.Repeat("A", 86)
				fixture.signPermit(t, &permit)
			case "profile":
				permit.Proposal.Release.Manifest.Platforms[0].Profile = "user-service"
				fixture.signRelease(t, &permit.Proposal.Release)
				fixture.signPermit(t, &permit)
			case "platform":
				other := "linux-amd64"
				if other == fixture.Native.Policy.Platform {
					other = "darwin-arm64"
				}
				permit.Proposal.Challenge.Platform, permit.Proposal.Release.Manifest.Platforms[0].Platform = other, other
				fixture.signRelease(t, &permit.Proposal.Release)
				fixture.signPermit(t, &permit)
			case "policy":
				fixture.Native.Policy.BackupPaths = append(fixture.Native.Policy.BackupPaths, filepath.Join(fixture.Root, "data2"))
			}
			if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err == nil {
				t.Fatal("changed approval was accepted")
			}
			if fixture.Fetches != 0 {
				t.Fatal("changed authorization fetched artifacts")
			}
			requireNoPrefix(t, fixture)
		})
	}
}

func TestNativeSignedWrongDigestAndUnsafeArchivesFailBeforeLeaseOrInstall(t *testing.T) {
	for _, name := range []string{"digest", "payload-digest", "traversal", "symlink", "checksum"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			release := fixture.release(t, 1)
			platform := &release.Manifest.Platforms[0]
			switch name {
			case "digest":
				platform.SHA256 = domain.Hash("different bytes")
			case "payload-digest":
				platform.Payloads[0].SHA256 = domain.Hash("different payload")
			case "traversal":
				bundle := gzipTar(t, []*tar.Header{{Name: "../escape", Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}}, nil)
				fixture.Artifacts[platform.ArtifactID] = bundle
				platform.SHA256, platform.Size = domain.Hash(string(bundle)), int64(len(bundle))
			case "symlink":
				bundle := gzipTar(t, []*tar.Header{{Name: "bin/tool", Mode: 0755, Typeflag: tar.TypeSymlink, Linkname: "/etc", Format: tar.FormatUSTAR}}, nil)
				fixture.Artifacts[platform.ArtifactID] = bundle
				platform.SHA256, platform.Size = domain.Hash(string(bundle)), int64(len(bundle))
			case "checksum":
				bundle := append([]byte{}, fixture.Artifacts[platform.ArtifactID]...)
				bundle[len(bundle)-8] ^= 1
				fixture.Artifacts[platform.ArtifactID] = bundle
				platform.SHA256 = domain.Hash(string(bundle))
			}
			fixture.signRelease(t, &release)
			permit := fixture.permit(t, "bad-"+name, release)
			reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
			if err == nil || reply.State != "failed" || reply.Result == nil {
				t.Fatalf("unsafe source was accepted: %#v %v", reply, err)
			}
			requireNoPrefix(t, fixture)
		})
	}
}

func TestNativeExpiryNonceAndCurrentAuthorityCannotBeRenewed(t *testing.T) {
	for _, name := range []string{"expiry", "nonce", "permit-hash", "foreign-ca", "stale-lease", "generation-change", "lease-loss"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			permit := fixture.permit(t, "lease-"+name, fixture.release(t, 1))
			if name == "expiry" {
				fixture.Now = fixture.Now.Add(901 * time.Second)
				replayed, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: permit.Proposal.Challenge.ID})
				if err != nil || replayed.Challenge.Nonce != permit.Proposal.Challenge.Nonce ||
					replayed.Challenge.ExpiresAt != permit.Proposal.Challenge.ExpiresAt {
					t.Fatal("challenge replay renewed native authority", err)
				}
			} else {
				fixture.Native.Lease = func(_ context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
					fixture.Leases++
					lease := domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce,
						IssuedAt: fixture.Now.Unix(), ExpiresAt: fixture.Now.Unix() + 30}
					key := fixture.CAKey
					switch name {
					case "nonce":
						lease.Nonce = domain.NewToken()
					case "permit-hash":
						lease.PermitSHA256 = domain.Hash("different permit")
					case "foreign-ca":
						other, err := pki.Generate(fixture.Now)
						if err != nil {
							t.Fatal(err)
						}
						key = other.CAKey
					case "stale-lease":
						lease.IssuedAt -= 31
						lease.ExpiresAt -= 31
					case "generation-change":
						fixture.Native.Policy.Generation++
					case "lease-loss":
						return lease, errors.New("lease response lost")
					}
					var err error
					lease.Signature, err = upgrade.SignLease(lease, key)
					return lease, err
				}
			}
			reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
			if err == nil || reply.State == "installed" {
				t.Fatal("stale, revoked or uncertain start authority was accepted", reply)
			}
			if _, err := os.Lstat(fixture.Native.Policy.Prefix); !errors.Is(err, os.ErrNotExist) || len(fixture.Recorder.effects) != 0 {
				t.Fatal("invalid fresh proof caused a native effect", err)
			}
			if name != "generation-change" && name != "expiry" {
				leases, fetches := fixture.Leases, fixture.Fetches
				_, _ = fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "apply",
					ID: permit.Proposal.Challenge.ID, Permit: &permit})
				if fixture.Leases != leases || fixture.Fetches != fetches {
					t.Fatal("uncertain authorization was renewed or source fetched again")
				}
			}
		})
	}
}

func TestNativePreparedSourceChangesAndMissingBackupsFailClosed(t *testing.T) {
	fixture := nativeFixture(t)
	permit := fixture.permit(t, "prepared-source", fixture.release(t, 1))
	if reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err != nil || reply.State != "prepared" {
		t.Fatal(reply, err)
	}
	if err := os.WriteFile(fixture.Native.bundleFilename(permit.Proposal.Challenge.ID), []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err == nil {
		t.Fatal("changed staged artifact was installed")
	}
	requireNoPrefix(t, fixture)
	missing := nativeFixture(t)
	permit = missing.permit(t, "missing-backup-source", missing.release(t, 1))
	if err := os.Remove(filepath.Join(missing.Root, "data", "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(missing.Root, "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err == nil {
		t.Fatal("missing pinned backup source was ignored")
	}
	requireNoPrefix(t, missing)
	installed := nativeFixture(t)
	first := installed.permit(t, "installed-proof", installed.release(t, 1))
	applyPermit(t, installed.Native, first)
	next := installed.permit(t, "missing-cold-proof", installed.release(t, 2))
	if err := os.Remove(installed.Native.backupFilename(first.Proposal.Challenge.ID)); err != nil {
		t.Fatal(err)
	}
	leases := installed.Leases
	if _, err := installed.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: next.Proposal.Challenge.ID, Permit: &next}); err == nil {
		t.Fatal("upgrade proceeded without the current encrypted cold-backup proof")
	}
	if installed.Leases != leases {
		t.Fatal("missing backup was discovered only after native start admission")
	}
}

func TestNativeLostCompletionReconcilesMetadataWithoutRerunningEffects(t *testing.T) {
	fixture := nativeFixture(t)
	useServiceFixture(fixture, "linux")
	permit := fixture.permit(t, "recorded-before-response-loss", fixture.release(t, 1))
	original := applyPermit(t, fixture.Native, permit)
	var job nativeJob
	if err := readRecord(fixture.Native.jobFilename(permit.Proposal.Challenge.ID), &job); err != nil {
		t.Fatal(err)
	}
	job.State, job.Result, job.ReceiptSHA256 = "recording", nil, ""
	if err := fixture.Native.saveJob(job, false); err != nil {
		t.Fatal(err)
	}
	fixture.Now = fixture.Now.Add(2 * time.Hour)
	calls, fetches, leases := len(fixture.Recorder.calls), fixture.Fetches, fixture.Leases
	replayed := applyPermit(t, fixture.Native, permit)
	if !equalJSON(replayed.Result, original.Result) || len(fixture.Recorder.calls) != calls ||
		fixture.Fetches != fetches || fixture.Leases != leases {
		t.Fatal("verified completion was not reconciled from its original receipt only")
	}
	renewed := clonePermit(t, permit)
	renewed.ID = "different_permit"
	renewed.IssuedAt, renewed.ExpiresAt = fixture.Now.Unix(), fixture.Now.Unix()+900
	fixture.signPermit(t, &renewed)
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: renewed.Proposal.Challenge.ID, Permit: &renewed}); err == nil {
		t.Fatal("renewed validly signed permit adopted the original receipt")
	}
}

func TestNativePolicyActualUserTrustProtectedPathsAndSourceChange(t *testing.T) {
	fixture := nativeFixture(t)
	policyFile := filepath.Join(fixture.Root, "policy.json")
	data, err := json.Marshal(fixture.Native.Policy)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, policyFile, data, 0600)
	policy, err := ReadPolicy(policyFile)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Native.Policy = policy
	permit := fixture.permit(t, "protected-policy", fixture.release(t, 1))
	writeTestFile(t, policyFile, append(data, '\n'), 0600)
	if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
		Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit}); err == nil {
		t.Fatal("changed resident policy source was reused")
	}
	requireNoPrefix(t, fixture)
	for _, name := range []string{"uid", "home", "trust", "same-key", "privileged-user", "foundation"} {
		t.Run(name, func(t *testing.T) {
			f := nativeFixture(t)
			switch name {
			case "uid":
				f.Native.Policy.UID++
			case "home":
				f.Native.Policy.Home = f.Root
			case "trust":
				if err := os.Remove(f.Native.Policy.PublicKeyFile); err != nil {
					t.Fatal(err)
				}
			case "same-key":
				key, err := privateKey(f.CAKey)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKIXPublicKey(key.Public())
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, f.Native.Policy.PublicKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
			case "privileged-user":
				f.Native.Policy.AllowPrivileged = true
			case "foundation":
				f.Native.Policy.Kind = "foundation"
			}
			if _, err := f.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "invalid-policy"}); err == nil {
				t.Fatal("missing or invalid native prerequisite was accepted")
			}
			requireNoPrefix(t, f)
		})
	}
}
