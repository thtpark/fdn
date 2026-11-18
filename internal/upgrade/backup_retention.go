package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

const backupRetirementFile = "backup-retirement.json"

type backupFileIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Bytes     int64  `json:"bytes"`
	ChangedNS int64  `json:"changed_ns"`
}

type retiredBackup struct {
	JournalSHA256 string             `json:"journal_sha256"`
	ArchiveSHA256 string             `json:"archive_sha256"`
	Archive       backupFileIdentity `json:"archive"`
}

type backupRetirementReceipt struct {
	Backup           retiredBackup `json:"backup"`
	TriggerPermitID  string        `json:"trigger_permit_id"`
	TriggerPermitSHA string        `json:"trigger_permit_sha256"`
}

type backupCandidate struct {
	name     string
	issuedAt int64
	digest   string
	jobSHA   string
	size     int64 // Recorded for failed partials, zero for a verified archive.
	file     backupFileIdentity
}

type backupRetirementPlan struct {
	root    *os.Root
	current domain.SoftwarePermit
	keeper  *backupCandidate
	remove  []backupCandidate
	prove   func(*os.Root, string) backupCandidate
}

func (b *SoftwareBridge) planBackupRetirement(ctx context.Context, current domain.SoftwarePermit, ca, public []byte) (plan *backupRetirementPlan, err error) {
	n := b.Native
	if err := protected(n.Policy.StateDir, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(n.Policy.StateDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	dir, err := privateBackupDirectory(root, ".")
	if err != nil {
		return nil, err
	}
	onPath, err := os.Lstat(n.Policy.StateDir)
	if err != nil || !os.SameFile(dir, onPath) {
		return nil, errors.Join(err, domain.Fail(domain.Conflict, "protected updater state directory changed"))
	}
	entries, err := backupDirectoryEntries(root, 1024)
	if err != nil {
		return nil, err
	}
	plan = &backupRetirementPlan{root: root, current: current}
	legacy := n.Policy
	legacy.PruneObsoleteBackups = false
	legacyHash := nativePolicyHash(legacy)
	plan.prove = func(dir *os.Root, name string) backupCandidate {
		return b.backupCandidate(ctx, dir, name, legacyHash, current, ca, public)
	}
	var verified []backupCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, internal := strings.CutPrefix(entry.Name(), "internal-")
		if !internal || !entry.IsDir() || !domain.ValidOperation(id) || id == current.Proposal.Challenge.ID {
			continue
		}
		jobDir, err := openBackupJob(root, entry.Name())
		if err != nil {
			continue
		}
		candidate := plan.prove(jobDir, entry.Name())
		jobDir.Close()
		if candidate.digest == "" {
			continue
		}
		if candidate.size == 0 {
			verified = append(verified, candidate)
		} else if plan.verifyArchive(ctx, &candidate) {
			plan.remove = append(plan.remove, candidate)
		}
	}
	var latest backupCandidate
	for _, candidate := range verified {
		if candidate.issuedAt > latest.issuedAt {
			latest = candidate
		}
	}
	for _, candidate := range verified {
		if candidate.issuedAt >= latest.issuedAt {
			continue
		}
		if plan.keeper == nil {
			if !plan.verifyArchive(ctx, &latest) {
				break // A missing or corrupt newest backup cannot justify deleting older full backups.
			}
			plan.keeper = &latest
		}
		if plan.verifyArchive(ctx, &candidate) {
			plan.remove = append(plan.remove, candidate)
		}
	}
	return plan, ctx.Err()
}

func (b *SoftwareBridge) backupCandidate(ctx context.Context, jobDir *os.Root, name, legacyHash string, current domain.SoftwarePermit, ca, public []byte) backupCandidate {
	raw, err := readBackupMetadata(jobDir, "job.json")
	var job softwareBridgeJournal
	if err != nil || config.Decode(raw, &job) != nil || job.Result == nil ||
		(job.State != "verified" && job.State != "aborted-before-install") {
		return backupCandidate{}
	}
	permit, result := job.Permit, *job.Result
	old, next := permit.Proposal.Challenge, current.Proposal.Challenge
	fingerprint := domain.Hash(permit.Signature)
	ack, err := readBackupMetadata(jobDir, "ack")
	if err != nil || string(ack) != fingerprint || permit.ID == current.ID || permit.IssuedAt >= current.IssuedAt ||
		old.ID != strings.TrimPrefix(name, "internal-") || old.InstanceID != next.InstanceID || old.TargetID != next.TargetID || old.BindingID != next.BindingID ||
		old.Generation != next.Generation || old.ProductID != next.ProductID || old.Platform != next.Platform ||
		(old.PolicySHA256 != next.PolicySHA256 && old.PolicySHA256 != legacyHash) ||
		permit.Proposal.Release.Manifest.Kind != "foundation" || result.PermitID != permit.ID ||
		result.PermitSHA256 != fingerprint || result.ProductID != old.ProductID || result.ReleaseID != permit.Proposal.Release.ID ||
		b.Trust.VerifyAdmittedPermit(permit, ca, b.Native.Now()) != nil ||
		b.Trust.VerifyRelease(permit.Proposal.Release, public) != nil {
		return backupCandidate{}
	}
	var retained domain.SoftwareChallenge
	var observed backupStreamResult
	if !decodeBackupMetadata(jobDir, "challenge.json", &retained) || retained != old ||
		!decodeBackupMetadata(jobDir, "backup-result.json", &observed) {
		return backupCandidate{}
	}
	candidate := backupCandidate{name: name, issuedAt: permit.IssuedAt, jobSHA: domain.Hash(string(raw))}
	if job.State == "verified" && observed.State == "complete" && observed.Error == "" &&
		result.State == "verified" && result.Code == "" && domain.ValidHash(job.Backup) && job.Backup == result.BackupSHA256 {
		for _, platform := range permit.Proposal.Release.Manifest.Platforms {
			if platform.Platform == old.Platform && platform.Profile == b.Native.Policy.Method &&
				platform.ArtifactID == result.ArtifactID && platform.SHA256 == result.SHA256 {
				candidate.digest = job.Backup
				return candidate
			}
		}
	} else if job.State == "aborted-before-install" && observed.State == "failed" && observed.Error != "" &&
		job.Backup == "" && result.BackupSHA256 == "" && result.State == "failed" &&
		result.Code == "operator-aborted-before-install" && !result.ResumeSafe && result.ArtifactID == "" && result.SHA256 == "" {
		candidate.digest, candidate.size = retiredPartialProof(ctx, jobDir, raw, job)
	}
	return candidate
}

func retiredPartialProof(ctx context.Context, root *os.Root, raw []byte, job softwareBridgeJournal) (digest string, size int64) {
	entries, err := backupDirectoryEntries(root, 64)
	if err != nil {
		return "", 0
	}
	challenge := job.Permit.Proposal.Challenge
	for _, entry := range entries {
		if ctx.Err() != nil {
			return "", 0
		}
		operation, recovery := strings.CutPrefix(entry.Name(), "recovery-")
		if !recovery || !entry.IsDir() || !domain.ValidOperation(operation) {
			continue
		}
		dir, err := openBackupJob(root, entry.Name())
		if err != nil {
			continue
		}
		var receipt SoftwareRecoveryReceipt
		ok := decodeBackupMetadata(dir, "receipt.json", &receipt)
		original, readErr := readBackupMetadata(dir, "original-job.json")
		dir.Close()
		var previous softwareBridgeJournal
		request := receipt.Request
		if !ok || readErr != nil || receipt.State != "completed" || request.OperationID != operation ||
			request.ID != challenge.ID || request.BaselineReleaseFile != "" || request.ExpectedModeRevision != 0 ||
			receipt.HelperVersion == "" || receipt.HelperCommit == "" || receipt.BaselineVersion != challenge.CurrentVersion ||
			receipt.Generation != challenge.Generation || receipt.PartialBackupBytes <= 0 ||
			!domain.ValidHash(receipt.PartialBackupSHA256) || recoveredJournal(raw, receipt) != nil ||
			receipt.OriginalJournalSHA256 != request.ExpectedJournalSHA256 ||
			domain.Hash(string(original)) != receipt.OriginalJournalSHA256 || config.Decode(original, &previous) != nil ||
			previous.State != "stopping" || previous.Backup != "" || previous.Result != nil ||
			previous.Payload != job.Payload || !reflect.DeepEqual(previous.Permit, job.Permit) {
			continue
		}
		if digest != "" && (digest != receipt.PartialBackupSHA256 || size != receipt.PartialBackupBytes) {
			return "", 0
		}
		digest, size = receipt.PartialBackupSHA256, receipt.PartialBackupBytes
	}
	return digest, size
}

func (p *backupRetirementPlan) verifyArchive(ctx context.Context, candidate *backupCandidate) bool {
	dir, err := openBackupJob(p.root, candidate.name)
	if err != nil {
		return false
	}
	defer dir.Close()
	file, before, err := openBackupArchive(dir)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := privateBackupDirectory(p.root, ".")
	if err != nil || uint64(info.Sys().(*syscall.Stat_t).Dev) != before.Device ||
		candidate.size != 0 && candidate.size != before.Bytes {
		return false
	}
	h := sha256.New()
	count, err := io.Copy(h, io.LimitReader(backupRead{ctx: ctx, Reader: file}, before.Bytes+1))
	if err != nil || count != before.Bytes || hex.EncodeToString(h.Sum(nil)) != candidate.digest {
		return false
	}
	candidate.file = before
	return unchangedBackupCandidate(dir, *candidate) == nil
}

func (p *backupRetirementPlan) prune(checkLease func() error) error {
	for _, candidate := range p.remove {
		if err := checkLease(); err != nil {
			return err
		}
		if candidate.size == 0 {
			dir, err := openBackupJob(p.root, p.keeper.name)
			if err != nil {
				return err
			}
			err = errors.Join(p.recheck(dir, *p.keeper), dir.Close())
			if err != nil {
				return err
			}
		}
		dir, err := openBackupJob(p.root, candidate.name)
		if err != nil {
			return err
		}
		if err := p.recheck(dir, candidate); err != nil {
			return errors.Join(err, dir.Close())
		}
		receipt := backupRetirementReceipt{Backup: retiredBackup{JournalSHA256: candidate.jobSHA, ArchiveSHA256: candidate.digest, Archive: candidate.file},
			TriggerPermitID: p.current.ID, TriggerPermitSHA: domain.Hash(p.current.Signature)}
		err = errors.Join(pruneBackup(dir, candidate, receipt, checkLease), dir.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *backupRetirementPlan) recheck(dir *os.Root, candidate backupCandidate) error {
	fresh := p.prove(dir, candidate.name)
	fresh.file = candidate.file
	if fresh != candidate {
		return domain.Fail(domain.Conflict, "settled backup proof changed before retirement")
	}
	return unchangedBackupCandidate(dir, candidate)
}

func pruneBackup(root *os.Root, candidate backupCandidate, receipt backupRetirementReceipt, checkLease func() error) error {
	if err := unchangedBackupCandidate(root, candidate); err != nil {
		return err
	}
	if err := checkLease(); err != nil {
		return err
	}
	if err := recordBackupRetirement(root, receipt); err != nil {
		return err
	}
	if err := unchangedBackupCandidate(root, candidate); err != nil {
		return err
	}
	if err := checkLease(); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Unlinkat(int(dir.Fd()), coldBackupArchive, 0); err != nil {
		return err
	}
	return dir.Sync()
}

func recordBackupRetirement(root *os.Root, receipt backupRetirementReceipt) error {
	raw, err := readBackupMetadata(root, backupRetirementFile)
	if err == nil {
		var prior backupRetirementReceipt
		if config.Decode(raw, &prior) != nil || prior.Backup != receipt.Backup ||
			!domain.ValidOperation(prior.TriggerPermitID) || !domain.ValidHash(prior.TriggerPermitSHA) {
			return domain.Fail(domain.Conflict, "retained backup retirement belongs to different archive evidence")
		}
		return syncBackupDirectory(root)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temp := ".backup-retirement-" + domain.NewToken()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return errors.Join(err, root.Remove(temp))
	}
	if err := root.Rename(temp, backupRetirementFile); err != nil {
		return errors.Join(err, root.Remove(temp))
	}
	return syncBackupDirectory(root)
}

func unchangedBackupCandidate(root *os.Root, candidate backupCandidate) error {
	raw, err := readBackupMetadata(root, "job.json")
	if err != nil {
		return err
	}
	file, identity, err := openBackupArchive(root)
	if err != nil {
		return err
	}
	if identity != candidate.file || domain.Hash(string(raw)) != candidate.jobSHA {
		return errors.Join(domain.Fail(domain.Conflict, "backup or settled native journal changed before retirement"), file.Close())
	}
	return file.Close()
}

func openBackupArchive(root *os.Root) (*os.File, backupFileIdentity, error) {
	file, stat, err := openBackupFile(root, coldBackupArchive)
	if err != nil {
		return nil, backupFileIdentity{}, err
	}
	return file, backupFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Bytes: stat.Size, ChangedNS: stat.Ctim.Nano()}, nil
}

func openBackupFile(root *os.Root, name string) (*os.File, unix.Stat_t, error) {
	var stat unix.Stat_t
	dir, err := root.Open(".")
	if err != nil {
		return nil, stat, err
	}
	defer dir.Close()
	// Root.OpenFile may resolve a final in-root symlink; these names are fixed leaves.
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, stat, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, stat, errors.Join(err, file.Close())
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Size <= 0 {
		return nil, stat, errors.Join(domain.Fail(domain.Denied, "backup history requires private regular owner files without links"), file.Close())
	}
	return file, stat, nil
}

func readBackupMetadata(root *os.Root, name string) ([]byte, error) {
	file, stat, err := openBackupFile(root, name)
	if err != nil {
		return nil, err
	}
	if stat.Size > domain.MaxBody {
		return nil, errors.Join(domain.Fail(domain.Invalid, "native backup history metadata exceeds its bound"), file.Close())
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if len(data) > domain.MaxBody {
		err = domain.Fail(domain.Invalid, "native backup history metadata exceeds its bound")
	}
	return data, errors.Join(err, file.Close())
}

func decodeBackupMetadata(root *os.Root, name string, value any) bool {
	raw, err := readBackupMetadata(root, name)
	return err == nil && config.Decode(raw, value) == nil
}

func privateBackupDirectory(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || secure.CheckOwner(info) != nil {
		return nil, domain.Fail(domain.Denied, "backup history requires real private owner directories")
	}
	return info, nil
}

func openBackupJob(parent *os.Root, name string) (*os.Root, error) {
	info, err := privateBackupDirectory(parent, name)
	if err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	current, err := privateBackupDirectory(root, ".")
	if err != nil || !os.SameFile(info, current) {
		return nil, errors.Join(err, domain.Fail(domain.Conflict, "protected backup history directory changed"), root.Close())
	}
	return root, nil
}

func backupDirectoryEntries(root *os.Root, limit int) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(limit + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if len(entries) > limit {
		err = domain.Fail(domain.Prerequisite, "native backup history exceeds the bounded retirement inventory")
	}
	return entries, errors.Join(err, dir.Close())
}

func syncBackupDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

type backupRead struct {
	ctx context.Context
	io.Reader
}

func (r backupRead) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}
