package upgrade

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type backupFailureRunner struct{ recorder *nativeRecorder }

func (r backupFailureRunner) Run(ctx context.Context, spec process.Spec) error {
	if err := r.recorder.Run(ctx, spec); err != nil {
		return err
	}
	if spec.Path == "/usr/bin/tar" {
		_, err := io.WriteString(spec.Stderr, "private fixture: backup input changed")
		return errors.Join(errors.New("fixture backup failure"), err)
	}
	return nil
}

func softwareRecoveryFixture(t *testing.T) (*SoftwareBridge, *nativeRecorder, SoftwareRecoveryRequest, string) {
	t.Helper()
	n, recorder, _, recipient := nativeFixtureForUser(t)
	n.Policy.EnableInternalSoftware, n.Policy.InternalProductID = true, "product_core"
	n.Policy.DistributionPublicKeyFile = filepath.Join(filepath.Dir(n.Policy.CAFile), "distribution.pem")
	if err := os.WriteFile(n.Policy.DistributionPublicKeyFile, []byte("separate fixture distribution public key"), 0600); err != nil {
		t.Fatal(err)
	}
	managedArchiveFixture(t, n)
	recorder.clearFence = true
	n.Runner = backupFailureRunner{recorder}
	platform := domain.SoftwarePlatform{Platform: runtime.GOOS + "-" + runtime.GOARCH,
		ArtifactID: "artifact_core", SHA256: domain.Hash("payload"), Size: 7, Profile: "archive"}
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		platform.Payloads = append(platform.Payloads, domain.SoftwarePayload{Path: "bin/" + name, Mode: 0755})
	}
	bridge := &SoftwareBridge{Native: n, Trust: SoftwareBridgeTrust{
		VerifyPermit: func(p domain.SoftwarePermit, _ []byte, _ time.Time) error {
			if p.Signature != "original-permission" {
				return errors.New("wrong original permission")
			}
			return nil
		},
		VerifyAdmittedPermit: func(p domain.SoftwarePermit, _ []byte, _ time.Time) error {
			if p.Signature != "original-permission" {
				return errors.New("wrong admitted permission")
			}
			return nil
		},
		VerifyRelease: func(r domain.SoftwareRelease, _ []byte) error {
			if r.ID != "release_core" || r.Manifest.Source.Commit != recorder.release.Commit {
				return errors.New("wrong release")
			}
			return nil
		},
		Fetch: func(_ context.Context, _ string, path string) error {
			return os.WriteFile(path, []byte("payload"), 0600)
		},
		Extract: func(_ []byte, path string, _ []domain.SoftwarePayload) error {
			if err := os.MkdirAll(filepath.Join(path, "bin"), 0700); err != nil {
				return err
			}
			return nil
		},
		Lease: n.Lease,
	}}
	ctx := context.Background()
	challenge, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "challenge", ID: "native_recover", ProductID: n.Policy.InternalProductID})
	if err != nil {
		t.Fatal(err)
	}
	permit := domain.SoftwarePermit{ID: "permit_recover", ActorID: "agent_founder", ActorBindingID: "agent_founder",
		ActorGeneration: 1, IssuedAt: n.Now().Unix(), ExpiresAt: n.Now().Unix() + 900, Signature: "original-permission",
		Proposal: domain.SoftwareProposal{Challenge: *challenge.Challenge, BackupRecipient: recipient.Recipient().String(), Reason: "Explicit fixture",
			Release: domain.SoftwareRelease{ID: "release_core", Manifest: domain.SoftwareManifest{Kind: "foundation",
				ProductID: n.Policy.InternalProductID, Version: recorder.release.Version, Source: domain.TaskSource{Commit: recorder.release.Commit},
				Platforms: []domain.SoftwarePlatform{platform}}}}}
	if _, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "prepare", ID: "native_recover", ProductID: n.Policy.InternalProductID, Permit: &permit}); err == nil {
		t.Fatal("fixture did not fail after admitted native stop/partial backup")
	}
	for _, path := range []string{filepath.Join(n.Policy.StateDir, "software-worker.lock"), filepath.Join(filepath.Dir(n.Policy.ConfigFile), "worker/daemon.lock")} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(n.Policy.StateDir, "internal-native_recover/job.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	req := SoftwareRecoveryRequest{OperationID: "abort.exact", ID: "native_recover",
		ExpectedJournalSHA256: domain.Hash(string(raw)), ExpectedPermitSHA256: domain.Hash(permit.Signature), Reason: "Explicit inspected pre-install abort"}
	return bridge, recorder, req, path
}

func TestInternalSoftwarePreinstallRecoveryRetainsOriginalEvidenceAndReplay(t *testing.T) {
	bridge, recorder, req, jobPath := softwareRecoveryFixture(t)
	before, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(filepath.Dir(jobPath), "before-upgrade.tar.age")
	archive, err := os.ReadFile(partial)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(recorder.calls)
	reply, err := bridge.AbortBeforeInstall(context.Background(), req)
	if err != nil || reply.State != "completed" || reply.Result.State != "failed" || reply.Result.ResumeSafe || reply.Result.BackupSHA256 != "" {
		t.Fatal("proven failed pre-install attempt was not explicitly aborted", reply, err)
	}
	for _, call := range recorder.calls[calls:] {
		if call.Path == "/usr/bin/tar" || strings.Contains(strings.Join(call.Args, " "), " start ") || strings.Contains(strings.Join(call.Args, " "), " stop ") {
			t.Fatal("recovery repeated native effects", call)
		}
	}
	retained, err := os.ReadFile(filepath.Join(filepath.Dir(jobPath), "recovery-"+req.OperationID, "original-job.json"))
	if err != nil || string(retained) != string(before) {
		t.Fatal("original journal was not preserved", err)
	}
	if current, err := os.ReadFile(partial); err != nil || string(current) != string(archive) || reply.PartialBackupSHA256 != domain.Hash(string(archive)) {
		t.Fatal("partial archive was changed or described as verified", err)
	}
	calls = len(recorder.calls)
	replay, err := bridge.AbortBeforeInstall(context.Background(), req)
	if err != nil || !reflect.DeepEqual(reply, replay) || len(recorder.calls) != calls {
		t.Fatal("exact recovery replay repeated observations or effects", replay, err)
	}
	changed := req
	changed.Reason = "different request"
	if _, err := bridge.AbortBeforeInstall(context.Background(), changed); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("operation adopted a changed original request", err)
	}
	pending, err := bridge.PendingResults()
	if err != nil || len(pending) != 1 || pending[0].State != "failed" || pending[0].Code != "operator-aborted-before-install" {
		t.Fatal("actual failed native outcome was not retained for reporting", pending, err)
	}
	if err := bridge.AcknowledgeResult(reply.Result.PermitID, reply.Result.PermitSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestInternalSoftwarePreinstallRecoveryDeniesUnknownEffectsAndFences(t *testing.T) {
	for _, name := range []string{"installing", "prepared", "verified", "unknown", "journal", "permit", "generation", "baseline", "pending", "session", "receiver", "daemon", "policy", "signature", "distribution", "partial-symlink"} {
		t.Run(name, func(t *testing.T) {
			bridge, recorder, req, path := softwareRecoveryFixture(t)
			var job softwareBridgeJournal
			if err := config.Read(path, &job); err != nil {
				t.Fatal(err)
			}

			switch name {
			case "installing", "prepared", "verified", "unknown":
				job.State = name
				if err := config.Write(path, job); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				req.ExpectedJournalSHA256 = domain.Hash(string(raw))
			case "journal":
				req.ExpectedJournalSHA256 = domain.Hash("foreign")
			case "permit":
				req.ExpectedPermitSHA256 = domain.Hash("foreign")
			case "generation":
				recorder.generation++
			case "baseline":
				recorder.newSource = true
			case "pending":
				recorder.busy = true
			case "session":
				recorder.clearFence = false
			case "receiver":
				lock, err := secure.Acquire(filepath.Join(bridge.Native.Policy.StateDir, "software-worker.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "daemon":
				state, err := bridge.Native.applicationState()
				if err != nil {
					t.Fatal(err)
				}
				lock, err := bridge.Native.applicationLock(filepath.Join(state, "daemon.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "policy":
				bridge.Native.Policy.InternalProductID = "product_foreign"
			case "signature":
				bridge.Trust.VerifyAdmittedPermit = func(domain.SoftwarePermit, []byte, time.Time) error { return errors.New("revoked original signer") }
			case "distribution":
				bridge.Trust.VerifyRelease = func(domain.SoftwareRelease, []byte) error { return errors.New("foreign distribution signature") }
			case "partial-symlink":
				partial := filepath.Join(filepath.Dir(path), "before-upgrade.tar.age")
				if err := os.Rename(partial, partial+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(partial+".retained", partial); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.AbortBeforeInstall(context.Background(), req); err == nil {
				t.Fatal("unsafe or foreign pre-install recovery accepted")
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
				t.Fatal("denied recovery changed native journal", err)
			}
		})
	}
}

func TestInternalSoftwarePreinstallRecoveryReconcilesOnlyItsPreparedIntent(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-journal", true: "after-journal"}[committed], func(t *testing.T) {
			bridge, _, req, path := softwareRecoveryFixture(t)
			receipt, err := bridge.AbortBeforeInstall(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)
			receipt.State = "prepared"
			if err := config.Write(filepath.Join(directory, "receipt.json"), receipt); err != nil {
				t.Fatal(err)
			}
			if !committed {
				raw, err := os.ReadFile(filepath.Join(directory, "original-job.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := secure.Write(path, raw); err != nil {
					t.Fatal(err)
				}
			}
			replay, err := bridge.AbortBeforeInstall(context.Background(), req)
			if err != nil || replay.State != "completed" || replay.Result.State != "failed" {
				t.Fatal("exact prepared abort did not reconcile without effects", replay, err)
			}
		})
	}
}
