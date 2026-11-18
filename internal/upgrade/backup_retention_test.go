package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type backupRetentionFixture struct {
	bridge    *SoftwareBridge
	recorder  *nativeRecorder
	old       softwareBridgeJournal
	failedDir string
	current   domain.SoftwarePermit
}

func newBackupRetentionFixture(t *testing.T, enabled bool) backupRetentionFixture {
	t.Helper()
	ctx := context.Background()
	b, recorder, recovery, jobPath := softwareRecoveryFixture(t)
	receipt, err := b.AbortBeforeInstall(ctx, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AcknowledgeResult(receipt.Result.PermitID, receipt.Result.PermitSHA256); err != nil {
		t.Fatal(err)
	}
	var old softwareBridgeJournal
	if err := config.Read(jobPath, &old); err != nil {
		t.Fatal(err)
	}
	n := b.Native
	now := n.Now().Add(2 * time.Second)
	n.Now = func() time.Time { return now }
	n.Policy.PruneObsoleteBackups = enabled
	n.Runner = recorder
	current := old.Permit
	current.ID = "permit_next"
	current.IssuedAt, current.ExpiresAt = now.Unix(), now.Unix()+900
	c, err := b.Handle(ctx, domain.SoftwareNativeRequest{Action: "challenge", ID: "retention.next", ProductID: n.Policy.InternalProductID})
	if err != nil || c.Challenge == nil {
		t.Fatal(c, err)
	}
	current.Proposal.Challenge = *c.Challenge
	current.Proposal.Release.ID = "release_next"
	platform := current.Proposal.Release.Manifest.Platforms[0]
	platform.Payloads = append([]domain.SoftwarePayload(nil), platform.Payloads...)
	for i, entry := range platform.Payloads {
		data := fixtureExecutable(recorder.release.Version, recorder.release.Commit, filepath.Base(entry.Path))
		platform.Payloads[i].Size = int64(len(data))
		platform.Payloads[i].SHA256 = domain.Hash(string(data))
	}
	current.Proposal.Release.Manifest.Platforms = []domain.SoftwarePlatform{platform}
	verify := b.Trust.VerifyRelease
	b.Trust.VerifyRelease = func(r domain.SoftwareRelease, key []byte) error {
		if r.ID == current.Proposal.Release.ID && r.Manifest.Source.Commit == recorder.release.Commit {
			return nil
		}
		return verify(r, key)
	}
	b.Trust.Extract = func(_ []byte, root string, payloads []domain.SoftwarePayload) error {
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			return err
		}
		for _, entry := range payloads {
			data := fixtureExecutable(recorder.release.Version, recorder.release.Commit, filepath.Base(entry.Path))
			if err := os.WriteFile(filepath.Join(root, entry.Path), data, 0755); err != nil {
				return err
			}
		}
		return nil
	}
	return backupRetentionFixture{bridge: b, recorder: recorder, old: old, failedDir: filepath.Dir(jobPath), current: current}
}

func (f backupRetentionFixture) handle(action string) (domain.SoftwareNativeReply, error) {
	request := domain.SoftwareNativeRequest{Action: action, ID: f.current.Proposal.Challenge.ID, ProductID: f.current.Proposal.Challenge.ProductID}
	if action == "prepare" || action == "apply" {
		request.Permit = &f.current
	}
	return f.bridge.Handle(context.Background(), request)
}

func (f backupRetentionFixture) verified(t *testing.T, id string, secondsAgo int64, edit func(*softwareBridgeJournal)) string {
	t.Helper()
	old := f.old
	old.Permit.ID = "permit_" + id
	old.Permit.IssuedAt = f.current.IssuedAt - secondsAgo
	old.Permit.Proposal.Challenge.ID = id
	data := []byte("encrypted archive fixture " + id)
	old.State, old.Backup = "verified", domain.Hash(string(data))
	platform := old.Permit.Proposal.Release.Manifest.Platforms[0]
	old.Result = &domain.SoftwareResult{State: "verified", PermitID: old.Permit.ID, PermitSHA256: domain.Hash(old.Permit.Signature),
		ProductID: old.Permit.Proposal.Challenge.ProductID, ReleaseID: old.Permit.Proposal.Release.ID,
		BackupSHA256: old.Backup, ArtifactID: platform.ArtifactID, SHA256: platform.SHA256}
	if edit != nil {
		edit(&old)
	}
	dir := filepath.Join(f.bridge.Native.Policy.StateDir, "internal-"+id)
	if err := secure.Dir(dir); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"job.json": old, "backup-result.json": backupStreamResult{State: "complete"}, "challenge.json": old.Permit.Proposal.Challenge} {
		if err := config.Write(filepath.Join(dir, name), value); err != nil {
			t.Fatal(err)
		}
	}
	for name, raw := range map[string][]byte{"ack": []byte(old.Result.PermitSHA256), coldBackupArchive: data} {
		if err := secure.Create(filepath.Join(dir, name), raw); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f backupRetentionFixture) plan(t *testing.T) *backupRetirementPlan {
	t.Helper()
	p, err := f.bridge.planBackupRetirement(context.Background(), f.current, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.root.Close() })
	return p
}

func requireBackupEntry(t *testing.T, dir, name string, present bool) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, name))
	if present && err != nil || !present && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s/%s presence want %t: %v", filepath.Base(dir), name, present, err)
	}
}

func TestInternalBackupRetentionPrunesBeforeFreshBackupAndApply(t *testing.T) {
	f := newBackupRetentionFixture(t, true)
	older := f.verified(t, "verified_older", 40, nil)
	newest := f.verified(t, "verified_newest", 20, nil)
	tied := f.verified(t, "verified_tied", 20, nil)
	foreign := f.verified(t, "foreign", 50, func(j *softwareBridgeJournal) { j.Permit.Proposal.Challenge.Generation++ })
	for _, dir := range []string{f.failedDir, older, newest} {
		requireBackupEntry(t, dir, coldBackupArchive, true)
		requireBackupEntry(t, dir, backupRetirementFile, false)
	}
	f.recorder.afterBackup = func() {
		for _, dir := range []string{f.failedDir, older} {
			requireBackupEntry(t, dir, coldBackupArchive, false)
			requireBackupEntry(t, dir, backupRetirementFile, true)
			requireBackupEntry(t, dir, "job.json", true)
			requireBackupEntry(t, dir, "ack", true)
		}
		for _, dir := range []string{newest, tied, foreign} {
			requireBackupEntry(t, dir, coldBackupArchive, true)
			requireBackupEntry(t, dir, backupRetirementFile, false)
		}
	}
	prepared, err := f.handle("prepare")
	if err != nil || prepared.State != "prepared" {
		t.Fatal(prepared, err)
	}
	newDir := filepath.Join(f.bridge.Native.Policy.StateDir, "internal-"+f.current.Proposal.Challenge.ID)
	requireBackupEntry(t, newDir, coldBackupArchive, true)
	archive := filepath.Join(newDir, coldBackupArchive)
	parked := filepath.Join(newDir, "test-held-current-archive")
	if err := os.Rename(archive, parked); err != nil {
		t.Fatal(err)
	}
	// Retention is not an alternate admission path: the new job still needs its own backup.
	if _, err := f.handle("apply"); err == nil {
		t.Fatal("apply succeeded without the current job's cold backup")
	}
	assertArchiveExecutables(t, f.bridge.Native, "0.3.2", strings.Repeat("b", 40))
	if err := os.Rename(parked, archive); err != nil {
		t.Fatal(err)
	}
	f.recorder.newSource = true
	if got, err := f.handle("apply"); err != nil || got.State != "verified" || got.Result == nil || !domain.ValidHash(got.Result.BackupSHA256) {
		t.Fatal("fresh admitted upgrade did not continue after retirement and backup", got, err)
	}
	assertArchiveExecutables(t, f.bridge.Native, f.recorder.release.Version, f.recorder.release.Commit)
}

func TestInternalBackupRetentionPreservesHistoryOnUnauthorizedAndObservedPaths(t *testing.T) {
	for _, name := range []string{"disabled", "invalid-permit", "invalid-lease", "status", "native-lock", "prepared-replay", "verified-replay"} {
		t.Run(name, func(t *testing.T) {
			f := newBackupRetentionFixture(t, name != "disabled")
			ctx := context.Background()
			n := f.bridge.Native
			switch name {
			case "disabled":
				if got, err := f.handle("prepare"); err != nil || got.State != "prepared" {
					t.Fatal(got, err)
				}
			case "status":
				id := f.old.Permit.Proposal.Challenge.ID
				result, err := f.bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "status", ID: id, ProductID: n.Policy.InternalProductID})
				if err != nil || result.State != "aborted-before-install" {
					t.Fatal(result, err)
				}
			case "prepared-replay", "verified-replay":
				// Add eligible history only after the new job is staged.
				archive := filepath.Join(f.failedDir, coldBackupArchive)
				parked := filepath.Join(f.failedDir, "test-held-archive")
				if err := os.Rename(archive, parked); err != nil {
					t.Fatal(err)
				}
				if got, err := f.handle("prepare"); err != nil || got.State != "prepared" {
					t.Fatal(got, err)
				}
				if name == "verified-replay" {
					f.recorder.newSource = true
					if got, err := f.handle("apply"); err != nil || got.State != "verified" {
						t.Fatal(got, err)
					}
					assertArchiveExecutables(t, n, f.recorder.release.Version, f.recorder.release.Commit)
				}
				if err := os.Rename(parked, archive); err != nil {
					t.Fatal(err)
				}
				if _, err := f.handle("prepare"); err != nil {
					t.Fatal(err)
				}
			default:
				switch name {
				case "invalid-permit":
					f.current.Signature = "invalid"
				case "invalid-lease":
					f.bridge.Trust.Lease = func(ctx context.Context, req domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
						lease, err := n.Lease(ctx, req)
						lease.Nonce = domain.NewToken()
						return lease, err
					}
				case "native-lock":
					locked, err := secure.Acquire(filepath.Join(n.Policy.StateDir, "upgrade.lock"))
					if err != nil {
						t.Fatal(err)
					}
					defer locked.Close()
				}
				before := len(f.recorder.calls)
				if _, err := f.handle("prepare"); err == nil {
					t.Fatal("unapproved prepare succeeded")
				}
				for _, call := range f.recorder.calls[before:] {
					args := strings.Join(call.Args, " ")
					if call.Path == "/usr/bin/tar" || strings.Contains(args, "bootout") || strings.Contains(args, " stop ") {
						t.Fatal("failed admission caused native effects", call)
					}
				}
			}
			requireBackupEntry(t, f.failedDir, coldBackupArchive, true)
			requireBackupEntry(t, f.failedDir, backupRetirementFile, false)
		})
	}
}

func TestInternalBackupRetentionDoesNotAdoptRefusedArchiveStaging(t *testing.T) {
	f := newBackupRetentionFixture(t, true)
	b := f.bridge
	originalExtract, originalLease := b.Trust.Extract, b.Trust.Lease
	b.Trust.Extract = func(data []byte, root string, payloads []domain.SoftwarePayload) error {
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			return domain.Fail(domain.Conflict, "fixture extraction requires a new immutable directory")
		}
		return originalExtract(data, root, payloads)
	}
	b.Trust.Lease = func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		return domain.UpgradeLease{}, domain.Fail(domain.Denied, "fixture start authority lost before admission")
	}
	refusedPayload := b.archivePayload(f.current)
	if _, err := f.handle("prepare"); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("original lease denial not surfaced", err)
	}
	requireBackupEntry(t, f.failedDir, coldBackupArchive, true)
	requireBackupEntry(t, refusedPayload, "bin", true)
	ctx := context.Background()
	c, err := b.Handle(ctx, domain.SoftwareNativeRequest{Action: "challenge", ID: "retention.replan", ProductID: b.Native.Policy.InternalProductID})
	if err != nil || c.Challenge == nil {
		t.Fatal(c, err)
	}
	f.current.ID, f.current.Proposal.Challenge = "permit_replan", *c.Challenge
	b.Trust.Lease = originalLease
	if got, err := f.handle("prepare"); err != nil || got.State != "prepared" {
		t.Fatal("fresh plan could not stage alongside the original refused payload", got, err)
	}
	requireBackupEntry(t, refusedPayload, "bin", true)
	requireBackupEntry(t, f.failedDir, coldBackupArchive, false)
	f.recorder.newSource = true
	if got, err := f.handle("apply"); err != nil || got.State != "verified" {
		t.Fatal(got, err)
	}
	target, err := os.Readlink(filepath.Join(archiveRoot(b.Native.Policy.Prefix), "current"))
	if err != nil || target == refusedPayload || target != b.archivePayload(f.current) {
		t.Fatal("fresh plan adopted the refused payload", target, err)
	}
}

func TestInternalBackupRetentionSkipsUnprovenPartial(t *testing.T) {
	for _, name := range []string{"unacknowledged", "foreign-ack", "foreign-trust", "changed-bytes", "missing-recovery",
		"wrong-challenge", "active", "completed-failed-activation", "hardlink", "symlink", "fifo", "directory", "public"} {
		t.Run(name, func(t *testing.T) {
			f := newBackupRetentionFixture(t, true)
			archive := filepath.Join(f.failedDir, coldBackupArchive)
			switch name {
			case "unacknowledged":
				if err := os.Remove(filepath.Join(f.failedDir, "ack")); err != nil {
					t.Fatal(err)
				}
			case "foreign-ack", "wrong-challenge", "changed-bytes":
				path := map[string]string{"foreign-ack": "ack", "wrong-challenge": "challenge.json", "changed-bytes": coldBackupArchive}[name]
				if err := secure.Write(filepath.Join(f.failedDir, path), []byte("{}")); err != nil {
					t.Fatal(err)
				}
			case "foreign-trust":
				f.bridge.Trust.VerifyAdmittedPermit = func(domain.SoftwarePermit, []byte, time.Time) error { return errors.New("retained signer unavailable") }
			case "missing-recovery":
				if err := os.Remove(filepath.Join(f.failedDir, "recovery-abort.exact", "receipt.json")); err != nil {
					t.Fatal(err)
				}
			case "active", "completed-failed-activation":
				j := f.old
				j.State = map[string]string{"active": "installing", "completed-failed-activation": "failed-before-managed-activation"}[name]
				if err := config.Write(filepath.Join(f.failedDir, "job.json"), j); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(archive, filepath.Join(f.failedDir, "alias")); err != nil {
					t.Fatal(err)
				}
			case "symlink", "fifo", "directory":
				if err := os.Rename(archive, filepath.Join(f.failedDir, "retained-original")); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "symlink":
					if err := os.Symlink("retained-original", archive); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := unix.Mkfifo(archive, 0600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(archive, 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "public":
				if err := os.Chmod(archive, 0644); err != nil {
					t.Fatal(err)
				}
			}
			p := f.plan(t)
			if len(p.remove) != 0 {
				t.Fatal("unproven backup selected for retirement")
			}
			requireBackupEntry(t, f.failedDir, coldBackupArchive, true)
			requireBackupEntry(t, f.failedDir, backupRetirementFile, false)
		})
	}
}

func TestInternalBackupRetentionKeepsVerifiedRecoveryPoint(t *testing.T) {
	for _, newer := range []string{"absent", "missing-bytes", "corrupt", "unacknowledged", "foreign-policy", "newer-generation"} {
		t.Run(newer, func(t *testing.T) {
			f := newBackupRetentionFixture(t, true)
			old := f.verified(t, "only_usable", 60, nil)
			if newer != "absent" {
				n := f.verified(t, "later", 20, func(j *softwareBridgeJournal) {
					if newer == "foreign-policy" {
						j.Permit.Proposal.Challenge.PolicySHA256 = domain.Hash("different native policy")
					}
					if newer == "newer-generation" {
						j.Permit.Proposal.Challenge.Generation++
					}
				})
				if newer == "corrupt" {
					if err := secure.Write(filepath.Join(n, coldBackupArchive), []byte("different")); err != nil {
						t.Fatal(err)
					}
				}
				if newer == "missing-bytes" {
					if err := os.Remove(filepath.Join(n, coldBackupArchive)); err != nil {
						t.Fatal(err)
					}
				}
				if newer == "unacknowledged" {
					if err := os.Remove(filepath.Join(n, "ack")); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := f.plan(t)
			if len(p.remove) != 1 || p.remove[0].name != filepath.Base(f.failedDir) {
				t.Fatal("failed eligibility must not make an older usable backup expendable")
			}
			if err := p.prune(func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			requireBackupEntry(t, old, coldBackupArchive, true)
		})
	}
}

func TestInternalBackupRetentionRechecksProofAndReconcilesExactIntent(t *testing.T) {
	for _, name := range []string{"exact-retry", "replacement-before-lease", "replacement-after-intent", "ack-changed", "keeper-changed"} {
		t.Run(name, func(t *testing.T) {
			f := newBackupRetentionFixture(t, true)
			archive := filepath.Join(f.failedDir, coldBackupArchive)
			raw, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			var keeper, older string
			if name == "keeper-changed" {
				older = f.verified(t, "older", 60, nil)
				keeper = f.verified(t, "keeper", 20, nil)
			}
			p := f.plan(t)
			switch name {
			case "exact-retry", "replacement-after-intent":
				expired := domain.Fail(domain.Denied, "fixture start lease expired")
				calls := 0
				err := p.prune(func() error {
					calls++
					if calls == 3 {
						return expired
					}
					return nil
				})
				if !errors.Is(err, expired) {
					t.Fatal("expected expiry after recording intent", err)
				}
				requireBackupEntry(t, f.failedDir, backupRetirementFile, true)
				requireBackupEntry(t, f.failedDir, coldBackupArchive, true)
			case "ack-changed":
				if err := secure.Write(filepath.Join(f.failedDir, "ack"), []byte("new ACK")); err != nil {
					t.Fatal(err)
				}
			case "keeper-changed":
				if err := secure.Write(filepath.Join(keeper, coldBackupArchive), []byte("changed")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "replacement-before-lease" || name == "replacement-after-intent" {
				if err := secure.Write(archive, raw); err != nil {
					t.Fatal(err)
				}
			}
			if name == "exact-retry" || name == "replacement-after-intent" {
				p = f.plan(t)
			}
			err = p.prune(func() error { return nil })
			if name == "exact-retry" {
				if err != nil {
					t.Fatal(err)
				}
				next := f.plan(t)
				if len(next.remove) != 0 {
					t.Fatal("already retired archive was selected again")
				}
				requireBackupEntry(t, f.failedDir, coldBackupArchive, false)
				return
			}
			if domain.AsFault(err).Code != domain.Conflict {
				t.Fatal("changed evidence must block the admitted retirement before services stop", err)
			}
			if name == "keeper-changed" {
				requireBackupEntry(t, older, coldBackupArchive, true)
			} else {
				requireBackupEntry(t, f.failedDir, coldBackupArchive, true)
			}
		})
	}
}

func TestBackupRetentionPolicyIsOptInAndOriginalFingerprintIsUnchanged(t *testing.T) {
	const oldPolicy = `{"role":"","name":"","instance_id":"","target_id":"","ca_file":"","state_dir":"","method":"","binary_dir":"","user":"","home":"","config_file":"","backup_paths":null}`
	if nativePolicyHash(Policy{}) != domain.Hash(oldPolicy) {
		t.Fatal("omitted retention flag changed the original policy fingerprint")
	}
	p := Policy{PruneObsoleteBackups: true}
	if nativePolicyHash(p) == nativePolicyHash(Policy{}) || domain.AsFault(p.Validate()).Code != domain.Invalid {
		t.Fatal("retention must have its own fingerprint and paired updating enabled")
	}
}
