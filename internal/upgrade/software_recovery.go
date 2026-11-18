package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type SoftwareRecoveryRequest struct {
	OperationID           string `json:"operation_id"`
	ID                    string `json:"id"`
	ExpectedJournalSHA256 string `json:"expected_journal_sha256"`
	ExpectedPermitSHA256  string `json:"expected_permit_sha256"`
	Reason                string `json:"reason"`
	BaselineReleaseFile   string `json:"baseline_release_file,omitempty"`
	ExpectedModeRevision  int64  `json:"expected_mode_revision,omitempty"`
}

type SoftwareRecoveryReceipt struct {
	Request               SoftwareRecoveryRequest `json:"request"`
	State                 string                  `json:"state"`
	HelperVersion         string                  `json:"helper_version"`
	HelperCommit          string                  `json:"helper_commit"`
	OriginalJournalSHA256 string                  `json:"original_journal_sha256"`
	AbortedJournalSHA256  string                  `json:"aborted_journal_sha256"`
	BaselineVersion       string                  `json:"baseline_version"`
	BaselineCommit        string                  `json:"baseline_commit"`
	Generation            int64                   `json:"generation"`
	PartialBackupSHA256   string                  `json:"partial_backup_sha256,omitempty"`
	PartialBackupBytes    int64                   `json:"partial_backup_bytes,omitempty"`
	Result                domain.SoftwareResult   `json:"result"`
	BaselineReleaseSHA256 string                  `json:"baseline_release_sha256,omitempty"`
}

func recoveredJournal(raw []byte, receipt SoftwareRecoveryReceipt) error {
	var job softwareBridgeJournal
	if err := config.Decode(raw, &job); err != nil {
		return err
	}
	state, backup := "aborted-before-install", ""
	if receipt.Request.BaselineReleaseFile != "" {
		state, backup = "failed-before-managed-activation", receipt.Result.BackupSHA256
	}
	if domain.Hash(string(raw)) != receipt.AbortedJournalSHA256 || job.State != state ||
		job.Backup != backup || job.Result == nil || *job.Result != receipt.Result ||
		domain.Hash(job.Permit.Signature) != receipt.Request.ExpectedPermitSHA256 ||
		job.Permit.ID != receipt.Result.PermitID {
		return domain.Fail(domain.Conflict, "recovery outcome does not match its exact retained aborted native job")
	}
	return nil
}

// AbortBeforeInstall only retires a proven failed pre-install attempt. It never
// installs, repeats a backup, starts work or clears application/session fences.
func (b *SoftwareBridge) AbortBeforeInstall(ctx context.Context, req SoftwareRecoveryRequest) (receipt SoftwareRecoveryReceipt, err error) {
	if req.BaselineReleaseFile != "" || req.ExpectedModeRevision != 0 {
		return receipt, domain.Fail(domain.Invalid, "stopping-only abort accepts no managed-activation recovery inputs")
	}
	return b.abortSoftware(ctx, req, false)
}

func (b *SoftwareBridge) AbortBeforeManagedActivation(ctx context.Context, req SoftwareRecoveryRequest) (SoftwareRecoveryReceipt, error) {
	if !filepath.IsAbs(req.BaselineReleaseFile) || filepath.Clean(req.BaselineReleaseFile) != req.BaselineReleaseFile || req.ExpectedModeRevision <= 0 {
		return SoftwareRecoveryReceipt{}, domain.Fail(domain.Invalid, "managed-activation recovery requires a local signed baseline release and exact manual revision")
	}
	return b.abortSoftware(ctx, req, true)
}

func (b *SoftwareBridge) abortSoftware(ctx context.Context, req SoftwareRecoveryRequest, managed bool) (receipt SoftwareRecoveryReceipt, err error) {
	n, p := b.Native, b.Native.Policy
	code, settled, original := "operator-aborted-before-install", "aborted-before-install", "stopping"
	if managed {
		code, settled, original = "operator-failed-before-managed-activation", "failed-before-managed-activation", "installing"
	}
	if !domain.ValidOperation(req.OperationID) || !domain.ValidOperation(req.ID) ||
		!domain.ValidHash(req.ExpectedJournalSHA256) || !domain.ValidHash(req.ExpectedPermitSHA256) {
		return receipt, domain.Fail(domain.Invalid, "recovery requires its exact operation, native job, journal and permit hashes")
	}
	if err := domain.CheckText(req.Reason, "pre-install recovery reason", 2048, true); err != nil {
		return receipt, err
	}
	if err := p.Validate(); err != nil {
		return receipt, err
	}
	if p.Role != "agent" || !p.EnableInternalSoftware || b.Trust.VerifyAdmittedPermit == nil || b.Trust.VerifyRelease == nil {
		return receipt, domain.Fail(domain.Denied, "pre-install recovery requires the existing exact Agent updater and explicit local operator authority")
	}
	var locks []*secure.Lock
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			err = errors.Join(err, locks[i].Close())
		}
	}()
	for _, name := range []string{"software-worker.lock", "internal-updater.lock", "upgrade.lock"} {
		path := filepath.Join(p.StateDir, name)
		if err := protected(path, uint32(os.Geteuid())); err != nil {
			return receipt, err
		}
		lock, err := secure.Acquire(path)
		if err != nil {
			return receipt, err
		}
		locks = append(locks, lock)
	}
	directory := filepath.Join(p.StateDir, "internal-"+req.ID)
	jobPath := filepath.Join(directory, "job.json")
	raw, err := secure.Read(jobPath, domain.MaxBody)
	if err != nil {
		return receipt, err
	}
	recovery := filepath.Join(directory, "recovery-"+req.OperationID)
	receiptPath := filepath.Join(recovery, "receipt.json")
	priorErr := config.Read(receiptPath, &receipt)
	if priorErr == nil {
		if receipt.Request != req || receipt.OriginalJournalSHA256 != req.ExpectedJournalSHA256 ||
			!domain.ValidHash(receipt.AbortedJournalSHA256) || receipt.HelperVersion == "" || receipt.HelperCommit == "" ||
			receipt.Result.PermitSHA256 != req.ExpectedPermitSHA256 || receipt.Result.State != "failed" ||
			receipt.Result.Code != code || receipt.Result.ResumeSafe || (!managed && receipt.Result.BackupSHA256 != "") ||
			(managed && (!domain.ValidHash(receipt.Result.BackupSHA256) || !domain.ValidHash(receipt.BaselineReleaseSHA256))) {
			return receipt, domain.Fail(domain.Conflict, "recovery operation belongs to a different original proposal")
		}
		if managed {
			saved, err := secure.Read(filepath.Join(recovery, "original-job.json"), domain.MaxBody)
			if err != nil {
				return receipt, err
			}
			baseline, err := secure.Read(filepath.Join(recovery, "baseline-release.json"), domain.MaxBody)
			if err != nil {
				return receipt, err
			}
			if domain.Hash(string(saved)) != req.ExpectedJournalSHA256 || domain.Hash(string(baseline)) != receipt.BaselineReleaseSHA256 {
				return receipt, domain.Fail(domain.Conflict, "retained original recovery evidence changed")
			}
		}
		if receipt.State == "completed" {
			return receipt, recoveredJournal(raw, receipt)
		}
		if receipt.State != "prepared" {
			return receipt, domain.Fail(domain.Conflict, "unknown recovery phase; inspect preserved native evidence")
		}
		if receipt.HelperVersion != domain.Version || receipt.HelperCommit != domain.Commit {
			return receipt, domain.Fail(domain.Conflict, "prepared recovery requires its same exact source-bound helper")
		}
		if domain.Hash(string(raw)) == receipt.AbortedJournalSHA256 {
			if err := recoveredJournal(raw, receipt); err != nil {
				return receipt, err
			}
			receipt.State = "completed"
			return receipt, config.Write(receiptPath, receipt)
		}
	} else if !errors.Is(priorErr, os.ErrNotExist) {
		return receipt, priorErr
	}
	if domain.Hash(string(raw)) != req.ExpectedJournalSHA256 {
		return receipt, domain.Fail(domain.Conflict, "native journal differs from the exact inspected pre-install attempt")
	}
	var job softwareBridgeJournal
	if err := config.Decode(raw, &job); err != nil {
		return receipt, err
	}
	challenge := job.Permit.Proposal.Challenge
	if job.State != original || job.Result != nil || (!managed && job.Backup != "") ||
		challenge.ID != req.ID || domain.Hash(job.Permit.Signature) != req.ExpectedPermitSHA256 ||
		challenge.ProductID != p.InternalProductID || challenge.TargetID != p.TargetID ||
		challenge.BindingID != p.TargetID || challenge.InstanceID != p.InstanceID ||
		challenge.PolicySHA256 != n.policyHash() || job.Permit.Proposal.Release.Manifest.Kind != "foundation" {
		return receipt, domain.Fail(domain.Denied, "recovery is only for the exact failed pre-install stop/backup, never installing or unknown effects")
	}
	ca, err := secure.ReadPublic(p.CAFile, domain.MaxBody)
	if err != nil {
		return receipt, err
	}
	public, err := secure.ReadPublic(p.DistributionPublicKeyFile, 16<<10)
	if err != nil {
		return receipt, err
	}
	if err := b.Trust.VerifyAdmittedPermit(job.Permit, ca, n.Now()); err != nil {
		return receipt, err
	}
	if err := b.Trust.VerifyRelease(job.Permit.Proposal.Release, public); err != nil {
		return receipt, err
	}
	var baselineRaw []byte
	if managed {
		baselineRaw, err = b.proveBeforeManagedActivation(ctx, job, req, directory, public)
		if err != nil {
			return receipt, err
		}
	}
	generation, err := n.target(ctx)
	if err != nil {
		return receipt, err
	}
	if generation != challenge.Generation {
		return receipt, domain.Fail(domain.Conflict, "recovery target identity generation changed")
	}
	version, commit, err := n.installed(ctx)
	if err != nil {
		return receipt, err
	}
	binaryHash, err := fileHash(filepath.Join(p.BinaryDir, "agent-cli"))
	if err != nil {
		return receipt, err
	}
	if version != challenge.CurrentVersion || binaryHash != challenge.CurrentSHA256 ||
		challenge.CurrentRelease != "baseline_"+domain.Hash(version + ":" + commit + ":" + binaryHash)[:32] {
		return receipt, domain.Fail(domain.Conflict, "pre-install baseline changed; recovery cannot classify installation as absent")
	}
	for _, name := range []string{"agent-service", "agent-identity"} {
		var observed struct{ Version, Commit string }
		if err := n.jsonCommand(ctx, filepath.Join(p.BinaryDir, name), []string{"--version"}, true, &observed); err != nil {
			return receipt, err
		}
		if observed.Version != version || observed.Commit != commit {
			return receipt, domain.Fail(domain.Conflict, "paired baseline executables disagree; recovery cannot classify installation as absent")
		}
	}
	_, clear, err := n.idleState(ctx)
	if err != nil {
		return receipt, err
	}
	if !clear {
		return receipt, domain.Fail(domain.Prerequisite, "an independent session fence remains; recovery did not clear it")
	}
	state, err := n.applicationState()
	if err != nil {
		return receipt, err
	}
	daemon, err := n.applicationLock(filepath.Join(state, "daemon.lock"))
	if err != nil {
		return receipt, err
	}
	defer func() { err = errors.Join(err, daemon.Close()) }()
	work, err := n.lockApplication()
	if err != nil {
		return receipt, err
	}
	defer func() { err = errors.Join(err, work.Close()) }()
	result := domain.SoftwareResult{PermitID: job.Permit.ID, PermitSHA256: req.ExpectedPermitSHA256,
		ProductID: challenge.ProductID, ReleaseID: job.Permit.Proposal.Release.ID,
		State: "failed", Code: code}
	if managed {
		result.BackupSHA256 = job.Backup
		for _, platform := range job.Permit.Proposal.Release.Manifest.Platforms {
			if platform.Platform == challenge.Platform {
				result.ArtifactID, result.SHA256 = platform.ArtifactID, platform.SHA256
			}
		}
	}
	receipt = SoftwareRecoveryReceipt{Request: req, State: "prepared", HelperVersion: domain.Version, HelperCommit: domain.Commit, OriginalJournalSHA256: req.ExpectedJournalSHA256,
		BaselineVersion: version, BaselineCommit: commit, Generation: generation, Result: result}
	if managed {
		receipt.BaselineReleaseSHA256 = domain.Hash(string(baselineRaw))
	}
	partial := filepath.Join(directory, "before-upgrade.tar.age")
	if info, statErr := os.Lstat(partial); statErr == nil {
		if err := protected(partial, uint32(os.Geteuid())); err != nil {
			return receipt, err
		}
		if !info.Mode().IsRegular() {
			return receipt, domain.Fail(domain.Denied, "partial backup evidence is not a private regular file")
		}
		receipt.PartialBackupSHA256, err = fileHash(partial)
		if err != nil {
			return receipt, err
		}
		receipt.PartialBackupBytes = info.Size()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return receipt, statErr
	}
	job.State, job.Result = settled, &result
	next, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return receipt, err
	}
	next = append(next, '\n')
	receipt.AbortedJournalSHA256 = domain.Hash(string(next))
	if priorErr != nil {
		if managed {
			if err := secure.Dir(recovery); err != nil {
				return receipt, err
			}
			if err := retainRecoveryBytes(filepath.Join(recovery, "original-job.json"), raw); err != nil {
				return receipt, err
			}
			if err := retainRecoveryBytes(filepath.Join(recovery, "baseline-release.json"), baselineRaw); err != nil {
				return receipt, err
			}
		} else {
			if err := os.Mkdir(recovery, 0700); err != nil {
				return receipt, err
			}
			if err := secure.Create(filepath.Join(recovery, "original-job.json"), raw); err != nil {
				return receipt, err
			}
		}
		if err := config.Write(receiptPath, receipt); err != nil {
			return receipt, err
		}
	} else {
		var saved SoftwareRecoveryReceipt
		if err := config.Read(receiptPath, &saved); err != nil {
			return receipt, err
		}
		if saved != receipt {
			return receipt, domain.Fail(domain.Conflict, "prepared recovery evidence changed; original files were preserved")
		}
	}
	current, err := secure.Read(jobPath, domain.MaxBody)
	if err != nil {
		return receipt, err
	}
	if domain.Hash(string(current)) != req.ExpectedJournalSHA256 {
		return receipt, domain.Fail(domain.Conflict, "native job changed during recovery inspection")
	}
	if err := secure.Write(jobPath, next); err != nil {
		return receipt, err
	}
	receipt.State = "completed"
	return receipt, config.Write(receiptPath, receipt)
}

func retainRecoveryBytes(path string, raw []byte) error {
	prior, err := secure.Read(path, domain.MaxBody)
	if errors.Is(err, os.ErrNotExist) {
		return secure.Create(path, raw)
	}
	if err != nil {
		return err
	}
	if err := protected(path, uint32(os.Geteuid())); err != nil {
		return err
	}
	if string(prior) != string(raw) {
		return domain.Fail(domain.Conflict, "original recovery evidence differs; retained bytes were not replaced")
	}
	return nil
}
