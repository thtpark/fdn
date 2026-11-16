package software

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

// Receipt is private durable native evidence. Installed means locally checked
// bytes and requested fixed lifecycle, not production qualification or safe AI
// resumption. Result.ResumeSafe is always false in this package.
type Receipt struct {
	FormatVersion   int                        `json:"format_version"`
	ID              string                     `json:"id"`
	PolicySHA256    string                     `json:"policy_sha256"`
	Permit          domain.SoftwarePermit      `json:"permit"`
	Payloads        []domain.SoftwarePayload   `json:"payloads"`
	PreparedSHA256  string                     `json:"prepared_sha256"`
	VersionDir      string                     `json:"version_dir"`
	BundleFile      string                     `json:"bundle_file"`
	BackupFile      string                     `json:"backup_file"`
	BackupSHA256    string                     `json:"backup_sha256"`
	PreviousReceipt string                     `json:"previous_receipt,omitempty"`
	PreviousSHA256  string                     `json:"previous_sha256,omitempty"`
	LeaseRequest    domain.UpgradeLeaseRequest `json:"lease_request"`
	Lease           domain.UpgradeLease        `json:"lease"`
	CompletedAt     int64                      `json:"completed_at"`
	Result          domain.SoftwareResult      `json:"result"`
}

type receiptPointer struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type outboxRecord struct {
	ChallengeID  string                `json:"challenge_id"`
	ReceiptSHA   string                `json:"receipt_sha256,omitempty"`
	Result       domain.SoftwareResult `json:"result"`
	Problem      *domain.Fault         `json:"problem,omitempty"`
	Acknowledged bool                  `json:"acknowledged"`
}

func (n *Native) makeReceipt(config nativeConfig, job nativeJob, directory string) Receipt {
	id := job.Permit.Proposal.Challenge.ID
	return Receipt{FormatVersion: 1, ID: id, PolicySHA256: config.PolicySHA256, Permit: job.Permit,
		Payloads: job.Payloads, PreparedSHA256: job.PreparedSHA256, VersionDir: directory,
		BundleFile: n.bundleFilename(id), BackupFile: n.backupFilename(id), BackupSHA256: job.BackupSHA256,
		PreviousReceipt: job.Snapshot.ReceiptID, PreviousSHA256: job.Snapshot.ReceiptSHA,
		LeaseRequest: job.LeaseRequest, Lease: job.Lease, CompletedAt: n.clock().Unix(),
		Result: n.resultFor(job, "installed", "")}
}

func (n *Native) readReceipt(id string) (Receipt, string, error) {
	if !domain.ValidOperation(id) {
		return Receipt{}, "", domain.Fail(domain.Invalid, "native software receipt requires its exact operation ID")
	}
	filename := n.receiptFilename(id)
	if err := protectedPath(filename, uint32(os.Geteuid()), false); err != nil {
		return Receipt{}, "", err
	}
	data, err := secure.Read(filename, maxRecordBytes)
	if err != nil {
		return Receipt{}, "", err
	}
	var receipt Receipt
	if err := decodeJSON(data, &receipt); err != nil {
		return Receipt{}, "", err
	}
	return receipt, domain.Hash(string(data)), nil
}

func (n *Native) verifyReceipt(config nativeConfig, receipt Receipt, hash string) error {
	policy, permit := config.Policy, receipt.Permit
	challenge := permit.Proposal.Challenge
	anchored := policy.Current != nil && hash == policy.Current.ReceiptSHA256 &&
		policy.Current.ReceiptFile == n.receiptFilename(receipt.ID) &&
		equalJSON(policy.Current.Release.Manifest, permit.Proposal.Release.Manifest)
	if receipt.FormatVersion != 1 || !domain.ValidOperation(receipt.ID) || receipt.ID != challenge.ID ||
		receipt.PolicySHA256 != challenge.PolicySHA256 || receipt.PolicySHA256 != config.PolicySHA256 && !anchored ||
		challenge.InstanceID != policy.InstanceID || challenge.TargetID != policy.TargetID || challenge.BindingID != policy.BindingID ||
		challenge.Generation > policy.Generation || permit.Proposal.Release.Manifest.ProductID != policy.ProductID ||
		permit.Proposal.Release.Manifest.Kind != policy.Kind || challenge.Platform != policy.Platform ||
		receipt.LeaseRequest.PermitSHA256 != domain.Hash(permit.Signature) ||
		!validNonce(receipt.LeaseRequest.Nonce) || receipt.LeaseRequest.Nonce == challenge.Nonce ||
		receipt.CompletedAt < receipt.Lease.IssuedAt-5 || receipt.Result.State != "installed" || receipt.Result.ResumeSafe ||
		receipt.BackupSHA256 != receipt.Result.BackupSHA256 || !domain.ValidHash(receipt.PreparedSHA256) ||
		receipt.PreviousReceipt != "" && (!domain.ValidOperation(receipt.PreviousReceipt) || !domain.ValidHash(receipt.PreviousSHA256)) ||
		receipt.PreviousReceipt == "" && receipt.PreviousSHA256 != "" {
		return domain.Fail(domain.Conflict, "native software receipt does not prove the exact original authorized installation")
	}
	if err := VerifyRelease(permit.Proposal.Release, config.PublicKey); err != nil {
		return err
	}
	admittedAt := time.Unix(receipt.Lease.IssuedAt, 0)
	if err := VerifyPermit(permit, config.CA, admittedAt); err != nil {
		return err
	}
	if err := upgrade.VerifyLease(receipt.Lease, receipt.LeaseRequest, config.CA, admittedAt); err != nil {
		return err
	}
	if err := ValidateResult(permit, receipt.Result); err != nil {
		return err
	}
	name, err := immutableName(permit.Proposal.Release)
	if err != nil || receipt.VersionDir != filepath.Join(policy.Prefix, "versions", name) ||
		receipt.BackupFile != n.backupFilename(receipt.ID) || receipt.BundleFile != n.bundleFilename(receipt.ID) {
		return domain.Fail(domain.Conflict, "native software receipt replaces protected evidence or activation destinations")
	}
	if hash, _, err := digestFile(receipt.BackupFile, MaxBackupEncryptedBytes, true); err != nil || hash != receipt.BackupSHA256 {
		return domain.Fail(domain.Conflict, "native encrypted cold backup is missing, corrupted or not receipt-bound")
	}
	var job nativeJob
	if err := readRecord(n.jobFilename(receipt.ID), &job); err != nil {
		return err
	}
	if !equalJSON(job.Permit, permit) || job.PreparedSHA256 != receipt.PreparedSHA256 ||
		!slices.Equal(job.Payloads, receipt.Payloads) || job.BackupSHA256 != receipt.BackupSHA256 ||
		job.LeaseRequest != receipt.LeaseRequest || job.Lease != receipt.Lease ||
		job.ReceiptSHA256 != "" && job.ReceiptSHA256 != hash {
		return domain.Fail(domain.Conflict, "native software receipt differs from the original durable job and proof")
	}
	if err := n.validatePrepared(config, job); err != nil {
		return err
	}
	return verifyTree(receipt.VersionDir, receipt.Payloads, true)
}

func (n *Native) installPointer(config nativeConfig, receipt Receipt, hash string) error {
	policy := config.Policy
	target, err := currentLink(policy, receipt.VersionDir)
	if err != nil {
		return err
	}
	link, err := os.Readlink(filepath.Join(policy.Prefix, "current"))
	if err != nil {
		return domain.Fail(domain.Conflict, "verified native software activation pointer is missing")
	}
	var pointer receiptPointer
	if err := readRecord(policy.CurrentReceipt, &pointer); err == nil {
		if pointer.ID == receipt.ID && pointer.SHA256 == hash && link == target {
			return nil
		}
		if pointer.ID != receipt.PreviousReceipt || pointer.SHA256 != receipt.PreviousSHA256 {
			current, currentHash, err := n.readReceipt(pointer.ID)
			if err != nil || currentHash != pointer.SHA256 || current.Permit.Proposal.Release.Manifest.Revision <= receipt.Permit.Proposal.Release.Manifest.Revision {
				return domain.Fail(domain.Conflict, "native software receipt cannot replace an unknown or changed current generation")
			}
			if err := n.verifyReceipt(config, current, currentHash); err != nil {
				return err
			}
			expected, err := currentLink(policy, current.VersionDir)
			if err != nil || expected != link {
				return domain.Fail(domain.Conflict, "newer native software current generation is not exactly receipt-bound")
			}
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if receipt.PreviousReceipt != "" {
		return domain.Fail(domain.Conflict, "original current receipt disappeared; native metadata recovery cannot adopt unknown changes")
	}
	if link != target {
		return domain.Fail(domain.Conflict, "native metadata completion cannot activate or change software effects")
	}
	return writeRecord(policy.CurrentReceipt, receiptPointer{ID: receipt.ID, SHA256: hash}, false)
}

func (n *Native) reconcile(config nativeConfig, job nativeJob) (nativeJob, error) {
	id := job.Permit.Proposal.Challenge.ID
	receipt, hash, receiptErr := n.readReceipt(id)
	if receiptErr == nil {
		if !equalJSON(receipt.Permit, job.Permit) {
			return nativeJob{}, domain.Fail(domain.Conflict, "native software receipt belongs to another permit")
		}
		if err := n.verifyReceipt(config, receipt, hash); err != nil {
			return job, err
		}
		expected, expectedErr := currentLink(config.Policy, receipt.VersionDir)
		actual, linkErr := os.Readlink(filepath.Join(config.Policy.Prefix, "current"))
		if expectedErr == nil && linkErr == nil && actual == expected {
			if err := n.verifyActivationNamespace(config, receipt); err != nil {
				return job, err
			}
		}
		if err := n.installPointer(config, receipt, hash); err != nil {
			return job, err
		}
		job.State, job.Problem, job.Result, job.ReceiptSHA256 = "installed", nil, &receipt.Result, hash
		if err := n.saveJob(job, false); err != nil {
			return job, err
		}
		if err := n.writeOutbox(job); err != nil {
			return job, err
		}
		return job, nil
	}
	if !errors.Is(receiptErr, os.ErrNotExist) {
		return job, receiptErr
	}
	if terminal(job.State) {
		if job.State == "installed" || job.Result == nil {
			return job, domain.Fail(domain.Conflict, "native installed result lacks its original durable receipt")
		}
		if err := ValidateResult(job.Permit, *job.Result); err != nil {
			return job, err
		}
		if err := n.writeOutbox(job); err != nil {
			return job, err
		}
		return job, nil
	}
	job.Effects = true
	return n.failJob(job, domain.Fail(domain.Prerequisite, "interrupted native software effects lack a verified completion receipt; operator inspection is required"))
}

func (n *Native) writeOutbox(job nativeJob) error {
	if job.Result == nil {
		return domain.Fail(domain.Invalid, "native software outbox requires an actual terminal result")
	}
	if err := ValidateResult(job.Permit, *job.Result); err != nil {
		return err
	}
	record := outboxRecord{ChallengeID: job.Permit.Proposal.Challenge.ID, ReceiptSHA: job.ReceiptSHA256,
		Result: *job.Result, Problem: job.Problem}
	filename := filepath.Join(normalizePolicy(n.Policy).StateDir, "outbox", resultKey(job.Permit.ID))
	var existing outboxRecord
	if err := readRecord(filename, &existing); err == nil {
		if existing.ChallengeID != record.ChallengeID || existing.ReceiptSHA != record.ReceiptSHA ||
			!equalJSON(existing.Result, record.Result) || !equalJSON(existing.Problem, record.Problem) {
			return domain.Fail(domain.Conflict, "native software result already has a different immutable outbox record")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeRecord(filename, record, true)
}

func (n *Native) validateOutbox(config nativeConfig, record outboxRecord) error {
	if !domain.ValidOperation(record.ChallengeID) {
		return domain.Fail(domain.Invalid, "native software outbox operation reference is malformed")
	}
	var job nativeJob
	if err := readRecord(n.jobFilename(record.ChallengeID), &job); err != nil {
		return err
	}
	if err := n.checkOriginalJob(config, job); err != nil {
		return err
	}
	if job.Result == nil || !terminal(job.State) || !equalJSON(*job.Result, record.Result) ||
		job.ReceiptSHA256 != record.ReceiptSHA || !equalJSON(job.Problem, record.Problem) {
		return domain.Fail(domain.Conflict, "native software outbox is not the exact original durable outcome")
	}
	if record.Result.State == "installed" {
		receipt, hash, err := n.readReceipt(record.ChallengeID)
		if err != nil || hash != record.ReceiptSHA {
			return domain.Fail(domain.Conflict, "native software outbox lost its original verified receipt")
		}
		return n.verifyReceipt(config, receipt, hash)
	}
	return ValidateResult(job.Permit, record.Result)
}

// PendingResults returns only retained, source-checked undelivered outcomes.
// It performs no artifact fetch, lease, lifecycle or package-manager invocation.
func (n *Native) PendingResults() ([]domain.SoftwareResult, error) {
	config, err := n.configuration()
	if err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(config.Policy.StateDir, "software.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(filepath.Join(config.Policy.StateDir, "outbox"))
	if errors.Is(err, os.ErrNotExist) {
		return []domain.SoftwareResult{}, nil
	}
	if err != nil || len(entries) > MaxNativeJobs {
		return nil, domain.Fail(domain.Prerequisite, "native software outbox is unavailable or exceeds its bound")
	}
	results := []domain.SoftwareResult{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, domain.Fail(domain.Denied, "native software outbox contains an unrecognized resource")
		}
		var record outboxRecord
		if err := readRecord(filepath.Join(config.Policy.StateDir, "outbox", entry.Name()), &record); err != nil {
			return nil, err
		}
		if record.Acknowledged {
			continue
		}
		if err := n.validateOutbox(config, record); err != nil {
			return nil, err
		}
		if entry.Name() != resultKey(record.Result.PermitID) {
			return nil, domain.Fail(domain.Conflict, "native software outbox record was moved or replaced")
		}
		if !record.Acknowledged {
			results = append(results, record.Result)
		}
	}
	return results, nil
}

// AcknowledgeResult marks only delivery of the exact original outcome. It does
// not delete evidence, renew a permit, clear uncertainty or restart automation.
func (n *Native) AcknowledgeResult(permitID, permitSHA256 string) error {
	if !domain.ValidOperation(permitID) || !domain.ValidHash(permitSHA256) {
		return domain.Fail(domain.Invalid, "native software result acknowledgement requires the exact permit ID and fingerprint")
	}
	config, err := n.configuration()
	if err != nil {
		return err
	}
	lock, err := secure.Acquire(filepath.Join(config.Policy.StateDir, "software.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	filename := filepath.Join(config.Policy.StateDir, "outbox", resultKey(permitID))
	var record outboxRecord
	if err := readRecord(filename, &record); err != nil {
		return err
	}
	if record.Result.PermitSHA256 != permitSHA256 {
		return domain.Fail(domain.Conflict, "native software result acknowledgement changed its exact original permit")
	}
	if err := n.validateOutbox(config, record); err != nil {
		return err
	}
	if record.Acknowledged {
		return nil
	}
	record.Acknowledged = true
	return writeRecord(filename, record, false)
}
