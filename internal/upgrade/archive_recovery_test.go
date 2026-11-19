package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type archiveRecoveryRunner struct {
	recorder *nativeRecorder
	agent    domain.Agent
	identity *domain.AgentIdentity
}

func (r *archiveRecoveryRunner) Run(ctx context.Context, spec process.Spec) error {
	if r.identity != nil && strings.Contains(strings.Join(spec.Args, " "), "agent identity") {
		r.recorder.calls = append(r.recorder.calls, spec)
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": r.identity})
	}
	if strings.Contains(strings.Join(spec.Args, " "), "agent show") {
		r.recorder.calls = append(r.recorder.calls, spec)
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": r.agent})
	}
	return r.recorder.Run(ctx, spec)
}

func archiveRecoveryFixture(t *testing.T) (*SoftwareBridge, *archiveRecoveryRunner, SoftwareRecoveryRequest, string) {
	t.Helper()
	b, recorder, req, path := softwareRecoveryFixture(t)
	n := b.Native
	var job softwareBridgeJournal
	if err := config.Read(path, &job); err != nil {
		t.Fatal(err)
	}
	runner := &archiveRecoveryRunner{recorder: recorder, agent: domain.Agent{
		ID: n.Policy.TargetID, Active: true, Mode: "manual", Revision: 29,
		ModeRequestedBy: job.Permit.ActorID, ModeChangedAt: job.Permit.IssuedAt - 1,
	}}
	n.Runner = runner
	if err := os.Remove(filepath.Join(filepath.Dir(path), "before-upgrade.tar.age")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(filepath.Dir(path), "backup-command"), filepath.Join(filepath.Dir(path), "failed-backup-command")); err != nil {
		t.Fatal(err)
	}
	var err error
	job.Backup, err = n.backup(context.Background(), filepath.Dir(path), job.Permit.Proposal.BackupRecipient)
	if err != nil {
		t.Fatal(err)
	}
	job.State = "installing"
	job.Payload = filepath.Join(n.Policy.Prefix, "releases", "internal-"+job.Permit.Proposal.Release.ID)
	if err := os.MkdirAll(filepath.Join(job.Payload, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	platform := &job.Permit.Proposal.Release.Manifest.Platforms[0]
	platform.Payloads = nil
	baseline := job.Permit.Proposal.Release
	baseline.ID = "release_baseline"
	baseline.Manifest.InstanceID = n.Policy.InstanceID
	baseline.Manifest.Version, baseline.Manifest.Source.Commit = "0.3.2", strings.Repeat("b", 40)
	oldPlatform := *platform
	for _, name := range archiveBinaries {
		old := fixtureExecutable(baseline.Manifest.Version, baseline.Manifest.Source.Commit, name)
		next := fixtureExecutable(recorder.release.Version, recorder.release.Commit, name)
		oldPlatform.Payloads = append(oldPlatform.Payloads, domain.SoftwarePayload{Path: "bin/" + name, Mode: 0755, Size: int64(len(old)), SHA256: domain.Hash(string(old))})
		platform.Payloads = append(platform.Payloads, domain.SoftwarePayload{Path: "bin/" + name, Mode: 0755, Size: int64(len(next)), SHA256: domain.Hash(string(next))})
		if err := os.WriteFile(filepath.Join(job.Payload, "bin", name), next, 0755); err != nil {
			t.Fatal(err)
		}
	}
	baseline.Manifest.Platforms = []domain.SoftwarePlatform{oldPlatform}
	baselineBytes, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	originalVerify := b.Trust.VerifyRelease
	b.Trust.VerifyRelease = func(release domain.SoftwareRelease, key []byte) error {
		raw, err := json.Marshal(release)
		if err == nil && string(raw) == string(baselineBytes) {
			return nil
		}
		return originalVerify(release, key)
	}
	req.BaselineReleaseFile = filepath.Join(filepath.Dir(path), "signed-baseline.json")
	req.ExpectedModeRevision = runner.agent.Revision
	if err := config.Write(req.BaselineReleaseFile, baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(job.Payload, filepath.Join(n.Policy.Prefix, "current")); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(path, job); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedJournalSHA256 = domain.Hash(string(raw))
	return b, runner, req, path
}

type ArchiveRecoveryTestJournal = softwareBridgeJournal

func ArchiveRecoveryTestFixture(t *testing.T, selfFounder bool) (*SoftwareBridge, SoftwareRecoveryRequest, string) {
	b, runner, req, path := archiveRecoveryFixture(t)
	if selfFounder {
		var job softwareBridgeJournal
		if err := config.Read(path, &job); err != nil {
			t.Fatal(err)
		}
		archiveRecoverySelfFounder(b, runner, &job)
		if err := config.Write(path, job); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		req.ExpectedJournalSHA256 = domain.Hash(string(raw))
	}
	return b, req, path
}

func archiveRecoverySelfFounder(b *SoftwareBridge, runner *archiveRecoveryRunner, job *softwareBridgeJournal) {
	job.Permit.ActorID = b.Native.Policy.TargetID
	job.Permit.ActorBindingID = job.Permit.ActorID
	runner.agent.Founder = true
	runner.agent.CreatedAt = job.Permit.IssuedAt - 100
	runner.agent.ModeRequestedBy, runner.agent.ModeChangedAt = "", 0
	runner.identity = &domain.AgentIdentity{AgentID: runner.agent.ID,
		InstanceID: b.Native.Policy.InstanceID, Generation: job.Permit.ActorGeneration, State: "active"}
}

func TestInternalSoftwareArchiveRecoverySelfFounderOriginalManual(t *testing.T) {
	for _, change := range []string{"none", "separate-device", "non-founder", "foreign-issuer", "foreign-device", "missing-separate-device", "foreign-binding", "foreign-agent", "foreign-instance", "inactive-identity", "generation", "newer-manual", "partial-attribution", "missing-created", "newer-created", "revision", "banned"} {
		t.Run(change, func(t *testing.T) {
			b, runner, req, path := archiveRecoveryFixture(t)
			var job softwareBridgeJournal
			if err := config.Read(path, &job); err != nil {
				t.Fatal(err)
			}
			archiveRecoverySelfFounder(b, runner, &job)
			switch change {
			case "separate-device":
				job.Permit.ActorBindingID = "device_original"
				runner.identity.DeviceID = job.Permit.ActorBindingID
			case "non-founder":
				runner.agent.Founder = false
			case "foreign-issuer":
				job.Permit.ActorID = "agent_foreign"
			case "foreign-device":
				runner.identity.DeviceID = "device_foreign"
			case "missing-separate-device":
				job.Permit.ActorBindingID = "device_original"
			case "foreign-binding":
				job.Permit.ActorBindingID = "agent_foreign"
			case "foreign-agent":
				runner.identity.AgentID = "agent_foreign"
			case "foreign-instance":
				runner.identity.InstanceID = "instance_foreign"
			case "inactive-identity":
				runner.identity.State = "revoked"
			case "generation":
				runner.identity.Generation++
			case "newer-manual":
				runner.agent.ModeRequestedBy, runner.agent.ModeChangedAt = job.Permit.ActorID, job.Permit.IssuedAt+1
			case "partial-attribution":
				runner.agent.ModeRequestedBy = job.Permit.ActorID
			case "missing-created":
				runner.agent.CreatedAt = 0
			case "newer-created":
				runner.agent.CreatedAt = job.Permit.IssuedAt
			case "revision":
				runner.agent.Revision++
			case "banned":
				runner.agent.Banned = true
			}
			if err := config.Write(path, job); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedJournalSHA256 = domain.Hash(string(raw))
			_, err = b.AbortBeforeManagedActivation(context.Background(), req)
			wantRecovery := change == "none" || change == "separate-device"
			if (err == nil) != wantRecovery {
				t.Fatal("self-Founder original manual proof", change, err)
			}
			if !wantRecovery {
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

func TestInternalSoftwareArchiveRecoverySettlesAndReplaysWithoutNativeEffects(t *testing.T) {
	for _, crash := range []string{"none", "before-journal", "after-journal"} {
		t.Run(crash, func(t *testing.T) {
			b, runner, req, path := archiveRecoveryFixture(t)
			before := len(runner.recorder.calls)
			receipt, err := b.AbortBeforeManagedActivation(context.Background(), req)
			if err != nil || receipt.State != "completed" || receipt.Result.State != "failed" || receipt.Result.ResumeSafe || !domain.ValidHash(receipt.Result.BackupSHA256) {
				t.Fatal(receipt, err)
			}
			for _, call := range runner.recorder.calls[before:] {
				args := strings.Join(call.Args, " ")
				if call.Path == "/usr/bin/tar" || call.Path == "/bin/sh" || strings.Contains(args, " start ") || strings.Contains(args, " stop ") || strings.Contains(args, "bootstrap") || strings.Contains(args, "bootout") {
					t.Fatal("recovery performed native effects", call)
				}
			}
			assertArchiveExecutables(t, b.Native, "0.3.2", strings.Repeat("b", 40))
			directory := filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)
			if crash != "none" {
				receipt.State = "prepared"
				if err := config.Write(filepath.Join(directory, "receipt.json"), receipt); err != nil {
					t.Fatal(err)
				}
				if crash == "before-journal" {
					raw, err := os.ReadFile(filepath.Join(directory, "original-job.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := secure.Write(path, raw); err != nil {
						t.Fatal(err)
					}
				}
			}
			receipt.State = "completed"
			replay, err := b.AbortBeforeManagedActivation(context.Background(), req)
			if err != nil || !reflect.DeepEqual(replay, receipt) {
				t.Fatal("exact crash replay failed", replay, err)
			}
			results, err := b.PendingResults()
			if err != nil || len(results) != 1 || results[0] != receipt.Result {
				t.Fatal("failed outcome missing from outbox", results, err)
			}
			status, err := b.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "status", ID: req.ID, ProductID: b.Native.Policy.InternalProductID})
			if err != nil || status.State != "failed-before-managed-activation" || status.Result == nil || *status.Result != receipt.Result {
				t.Fatal("status lost exact failed settlement", status, err)
			}
			if err := b.AcknowledgeResult(receipt.Result.PermitID, domain.Hash("foreign")); err == nil {
				t.Fatal("foreign result ACK accepted")
			}
			if err := b.AcknowledgeResult(receipt.Result.PermitID, receipt.Result.PermitSHA256); err != nil {
				t.Fatal(err)
			}
			results, err = b.PendingResults()
			if err != nil || len(results) != 0 {
				t.Fatal("failed result ACK not retained", results, err)
			}
		})
	}
}

func TestInternalSoftwareArchiveRecoveryDeniesChangedProofWithoutSettlement(t *testing.T) {
	for _, change := range []string{"activated", "old-bytes", "new-bytes", "backup", "baseline", "manual", "busy", "session", "generation", "auxiliary", "permit", "journal"} {
		t.Run(change, func(t *testing.T) {
			b, runner, req, path := archiveRecoveryFixture(t)
			var job softwareBridgeJournal
			if err := config.Read(path, &job); err != nil {
				t.Fatal(err)
			}
			var err error
			switch change {
			case "activated":
				link := filepath.Join(archiveRoot(b.Native.Policy.Prefix), "current")
				err = os.Remove(link)
				if err == nil {
					err = os.Symlink(job.Payload, link)
				}
			case "old-bytes":
				current, readErr := os.Readlink(filepath.Join(archiveRoot(b.Native.Policy.Prefix), "current"))
				err = readErr
				if err == nil {
					err = os.WriteFile(filepath.Join(current, "bin", "fdn-service"), []byte("changed"), 0755)
				}
			case "new-bytes":
				err = os.WriteFile(filepath.Join(job.Payload, "bin", "agent-identity"), []byte("changed"), 0755)
			case "backup":
				err = os.WriteFile(filepath.Join(filepath.Dir(path), "before-upgrade.tar.age"), []byte("changed"), 0600)
			case "baseline":
				err = os.WriteFile(req.BaselineReleaseFile, []byte("{}"), 0600)
			case "manual":
				runner.agent.Revision++
			case "busy":
				runner.recorder.busy = true
			case "session":
				runner.recorder.clearFence = false
			case "generation":
				runner.recorder.generation++
			case "auxiliary":
				err = os.Remove(filepath.Join(b.Native.Policy.Prefix, "current"))
			case "permit":
				req.ExpectedPermitSHA256 = domain.Hash("foreign")
			case "journal":
				req.ExpectedJournalSHA256 = domain.Hash("foreign")
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.AbortBeforeManagedActivation(context.Background(), req); err == nil {
				t.Fatal("changed or unprovable archive state was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(raw) != string(after) {
				t.Fatal("denied recovery changed original native job", err)
			}
		})
	}
}

func TestInternalSoftwareArchiveRecoveryDeniesHeldLocksWithoutIntent(t *testing.T) {
	for _, name := range []string{"software-worker.lock", "internal-updater.lock", "upgrade.lock", "daemon.lock", "work.lock"} {
		t.Run(name, func(t *testing.T) {
			b, _, req, path := archiveRecoveryFixture(t)
			root := b.Native.Policy.StateDir
			if name == "daemon.lock" || name == "work.lock" {
				var err error
				root, err = b.Native.applicationState()
				if err != nil {
					t.Fatal(err)
				}
			}
			lock, err := secure.Acquire(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.AbortBeforeManagedActivation(context.Background(), req); err == nil {
				t.Fatal("occupied native/application lock accepted", name)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("lock denial changed original job", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)); !os.IsNotExist(err) {
				t.Fatal("lock denial prepared recovery intent", err)
			}
			results, err := b.PendingResults()
			if err != nil || len(results) != 0 {
				t.Fatal("lock denial emitted a failed result", results, err)
			}
		})
	}
}

func TestInternalSoftwareArchiveRecoveryPartialIntentPreservesExactEvidence(t *testing.T) {
	for _, retained := range []string{"original-only", "baseline-only", "both", "foreign-original", "foreign-baseline"} {
		t.Run(retained, func(t *testing.T) {
			b, _, req, path := archiveRecoveryFixture(t)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := os.ReadFile(req.BaselineReleaseFile)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)
			if err := secure.Dir(directory); err != nil {
				t.Fatal(err)
			}
			saved := map[string][]byte{}
			if retained != "baseline-only" {
				saved["original-job.json"] = original
			}
			if retained != "original-only" {
				saved["baseline-release.json"] = baseline
			}
			if retained == "foreign-original" {
				saved["original-job.json"] = []byte("foreign original")
			}
			if retained == "foreign-baseline" {
				saved["baseline-release.json"] = []byte("foreign baseline")
			}
			for name, raw := range saved {
				if err := secure.Create(filepath.Join(directory, name), raw); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := b.AbortBeforeManagedActivation(context.Background(), req)
			if strings.HasPrefix(retained, "foreign-") {
				if err == nil {
					t.Fatal("foreign partial intent adopted")
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != string(original) {
					t.Fatal("foreign partial intent changed original job", readErr)
				}
				if _, err := os.Stat(filepath.Join(directory, "receipt.json")); !os.IsNotExist(err) {
					t.Fatal("foreign partial intent prepared receipt", err)
				}
				results, readErr := b.PendingResults()
				if readErr != nil || len(results) != 0 {
					t.Fatal("foreign partial intent emitted result", results, readErr)
				}
			} else if err != nil || receipt.State != "completed" || receipt.Result.State != "failed" {
				t.Fatal("exact partial intent did not settle", receipt, err)
			}
			for name, raw := range saved {
				after, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil || string(after) != string(raw) {
					t.Fatal("retained evidence was replaced", name, err)
				}
			}
		})
	}
}

func TestInternalSoftwareArchiveRecoveryReplayRejectsChangedRetainedEvidence(t *testing.T) {
	for _, state := range []string{"prepared", "completed"} {
		for _, name := range []string{"original-job.json", "baseline-release.json"} {
			t.Run(state+"/"+name, func(t *testing.T) {
				b, _, req, path := archiveRecoveryFixture(t)
				receipt, err := b.AbortBeforeManagedActivation(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				directory := filepath.Join(filepath.Dir(path), "recovery-"+req.OperationID)
				receipt.State = state
				if err := config.Write(filepath.Join(directory, "receipt.json"), receipt); err != nil {
					t.Fatal(err)
				}
				if err := secure.Write(filepath.Join(directory, name), []byte("changed retained evidence")); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := b.AbortBeforeManagedActivation(context.Background(), req); err == nil {
					t.Fatal("replay accepted changed retained evidence")
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) {
					t.Fatal("denied replay changed settled job", err)
				}
				var retained SoftwareRecoveryReceipt
				if err := config.Read(filepath.Join(directory, "receipt.json"), &retained); err != nil || retained != receipt {
					t.Fatal("denied replay changed receipt", retained, err)
				}
			})
		}
	}
}
