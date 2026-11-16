package software

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

const MaxNativeJobs = 1024

type Native struct {
	Policy Policy
	Runner process.Runner
	Fetch  func(context.Context, string, string) error
	Lease  func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error)
	Now    func() time.Time

	host         func() hostIdentity
	requirements func(context.Context, Policy) error
	servicePath  func(Policy) string
}

type nativeConfig struct {
	Policy       Policy
	PolicySHA256 string
	PublicKey    []byte
	CA           []byte
}

type snapshot struct {
	State      string                   `json:"state"`
	Release    domain.SoftwareRelease   `json:"release"`
	Payloads   []domain.SoftwarePayload `json:"payloads"`
	Root       string                   `json:"root,omitempty"`
	ReceiptID  string                   `json:"receipt_id,omitempty"`
	ReceiptSHA string                   `json:"receipt_sha256,omitempty"`
	Service    serviceState             `json:"service"`
	Package    packageState             `json:"package"`
}

type challengeRecord struct {
	Challenge domain.SoftwareChallenge `json:"challenge"`
	Snapshot  snapshot                 `json:"snapshot"`
}

type nativeJob struct {
	Permit         domain.SoftwarePermit      `json:"permit"`
	State          string                     `json:"state"`
	Snapshot       snapshot                   `json:"snapshot"`
	Payloads       []domain.SoftwarePayload   `json:"payloads"`
	PreparedSHA256 string                     `json:"prepared_sha256,omitempty"`
	Effects        bool                       `json:"effects"`
	LeaseRequest   domain.UpgradeLeaseRequest `json:"lease_request"`
	Lease          domain.UpgradeLease        `json:"lease"`
	BackupSHA256   string                     `json:"backup_sha256,omitempty"`
	ReceiptSHA256  string                     `json:"receipt_sha256,omitempty"`
	Result         *domain.SoftwareResult     `json:"result,omitempty"`
	Problem        *domain.Fault              `json:"problem,omitempty"`
}

type preparedProof struct {
	PermitSHA256   string                   `json:"permit_sha256"`
	ManifestSHA256 string                   `json:"manifest_sha256"`
	Payloads       []domain.SoftwarePayload `json:"payloads"`
}

type permitReference struct {
	ChallengeID  string `json:"challenge_id"`
	PermitSHA256 string `json:"permit_sha256"`
}

func NewNative(policy Policy) *Native {
	return &Native{Policy: normalizePolicy(policy), Runner: process.Native{}, Now: time.Now}
}

func (n *Native) clock() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Native) hostIdentity() hostIdentity {
	if n.host != nil {
		return n.host()
	}
	return currentHost()
}

func (n *Native) configuration() (nativeConfig, error) {
	policy := normalizePolicy(n.Policy)
	if err := policy.validateFor(n.hostIdentity()); err != nil {
		return nativeConfig{}, err
	}
	if policy.sourceFile != "" {
		data, err := secure.ReadPublic(policy.sourceFile, 2*MaxManifestBytes)
		if err != nil || domain.Hash(string(data)) != policy.sourceSHA256 {
			return nativeConfig{}, domain.Fail(domain.Conflict, "provisioned software policy source changed; resident policy authority cannot be reused")
		}
		var enrolled Policy
		if err := decodeJSON(data, &enrolled); err != nil || !equalJSON(normalizePolicy(enrolled), policy) {
			return nativeConfig{}, domain.Fail(domain.Conflict, "resident native software capability differs from its exact provisioned policy source")
		}
	}
	for _, filename := range []string{policy.StateDir, policy.Prefix, policy.PublicKeyFile, policy.CAFile} {
		if err := protectedPath(filename, uint32(os.Geteuid()), filename == policy.Prefix); err != nil {
			return nativeConfig{}, err
		}
	}
	if err := secure.Dir(policy.StateDir); err != nil {
		return nativeConfig{}, err
	}
	publicPEM, err := secure.ReadPublic(policy.PublicKeyFile, 64<<10)
	if err != nil {
		return nativeConfig{}, err
	}
	caPEM, err := secure.ReadPublic(policy.CAFile, 64<<10)
	if err != nil {
		return nativeConfig{}, err
	}
	public, err := publicKey(publicPEM)
	if err != nil {
		return nativeConfig{}, err
	}
	ca, err := caKey(caPEM, n.clock())
	if err != nil {
		return nativeConfig{}, err
	}
	if ed25519.PublicKey(public).Equal(ca) {
		return nativeConfig{}, domain.Fail(domain.Denied, "distribution signing and Foundation authorization must use separate Ed25519 keys")
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return nativeConfig{}, err
	}
	hash := domain.Hash(string(data) + "\x00" + domain.Hash(string(publicPEM)) + "\x00" + domain.Hash(string(caPEM)))
	return nativeConfig{Policy: policy, PolicySHA256: hash, PublicKey: publicPEM, CA: caPEM}, nil
}

func (n *Native) jobDirectory(id string) string {
	return filepath.Join(normalizePolicy(n.Policy).StateDir, "jobs", id)
}

func (n *Native) jobFilename(id string) string { return filepath.Join(n.jobDirectory(id), "job.json") }
func (n *Native) challengeFilename(id string) string {
	return filepath.Join(n.jobDirectory(id), "challenge.json")
}
func (n *Native) bundleFilename(id string) string {
	return filepath.Join(n.jobDirectory(id), "bundle.tar.gz")
}
func (n *Native) outerDirectory(id string) string { return filepath.Join(n.jobDirectory(id), "outer") }
func (n *Native) contentDirectory(id string) string {
	if n.Policy.Method == "debian" {
		return filepath.Join(n.jobDirectory(id), "contents")
	}
	return n.outerDirectory(id)
}
func (n *Native) proofFilename(id string) string {
	return filepath.Join(n.jobDirectory(id), "prepared.json")
}
func (n *Native) backupFilename(id string) string {
	return filepath.Join(n.jobDirectory(id), "backup.age")
}
func (n *Native) receiptFilename(id string) string {
	return filepath.Join(normalizePolicy(n.Policy).StateDir, "receipts", id+".json")
}

func (n *Native) initializeDirectories() error {
	for _, name := range []string{"jobs", "permits", "receipts", "outbox"} {
		if err := ensurePrivate(filepath.Join(normalizePolicy(n.Policy).StateDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (n *Native) saveJob(job nativeJob, exclusive bool) error {
	return writeRecord(n.jobFilename(job.Permit.Proposal.Challenge.ID), job, exclusive)
}

func equalJSON(a, b any) bool {
	left, lerr := json.Marshal(a)
	right, rerr := json.Marshal(b)
	return lerr == nil && rerr == nil && bytes.Equal(left, right)
}

func terminal(state string) bool {
	return state == "installed" || state == "failed" || state == "operator-required"
}

func jobReply(job nativeJob) (domain.SoftwareNativeReply, error) {
	reply := domain.SoftwareNativeReply{State: job.State, Result: job.Result, Error: job.Problem}
	if job.Problem != nil {
		return reply, job.Problem
	}
	return reply, nil
}

func (n *Native) checkOriginalJob(config nativeConfig, job nativeJob) error {
	permit := job.Permit
	challenge := permit.Proposal.Challenge
	policy := config.Policy
	if challenge.PolicySHA256 != config.PolicySHA256 || challenge.InstanceID != policy.InstanceID ||
		challenge.TargetID != policy.TargetID || challenge.BindingID != policy.BindingID || challenge.Generation != policy.Generation ||
		challenge.ProductID != policy.ProductID || challenge.Platform != policy.Platform ||
		permit.Proposal.Release.Manifest.Kind != policy.Kind || permit.Proposal.BackupRecipient != policy.BackupRecipient {
		return domain.Fail(domain.Conflict, "retained software authority does not match the exact current policy/computer/product")
	}
	if err := VerifyRelease(permit.Proposal.Release, config.PublicKey); err != nil {
		return err
	}
	if err := VerifyPermit(permit, config.CA, time.Unix(permit.IssuedAt, 0)); err != nil {
		return err
	}
	var reference permitReference
	filename := filepath.Join(policy.StateDir, "permits", permit.ID+".json")
	if err := readRecord(filename, &reference); err != nil {
		return err
	}
	if reference.ChallengeID != challenge.ID || reference.PermitSHA256 != domain.Hash(permit.Signature) {
		return domain.Fail(domain.Conflict, "software permit ID was replaced or reused for another native operation")
	}
	return nil
}

func (n *Native) pendingNative(config nativeConfig, allowed string) error {
	entries, err := os.ReadDir(filepath.Join(config.Policy.StateDir, "jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) >= MaxNativeJobs {
		return domain.Fail(domain.Prerequisite, "native software evidence is full; operator retention review must preserve all uncertainty")
	}
	for _, entry := range entries {
		if !entry.IsDir() || !domain.ValidOperation(entry.Name()) {
			return domain.Fail(domain.Denied, "native software evidence contains an unrecognized resource")
		}
		if entry.Name() == allowed {
			continue
		}
		var job nativeJob
		if err := readRecord(n.jobFilename(entry.Name()), &job); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if job.State == "failed" || job.State == "installed" {
			continue
		}
		if job.State == "prepared" && !job.Effects &&
			(job.Permit.ExpiresAt <= n.clock().Unix() || job.Permit.Proposal.Challenge.ExpiresAt <= n.clock().Unix()) {
			continue
		}
		return domain.Fail(domain.Prerequisite, "another native software job is pending or operator-required; no takeover or native retry is permitted")
	}
	return nil
}

func absentDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return protectedPath(directory, uint32(os.Geteuid()), true)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return domain.Fail(domain.Conflict, "first install requires an absent or empty real managed prefix")
	}
	if err := protectedPath(directory, uint32(os.Geteuid()), false); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return domain.Fail(domain.Conflict, "unknown existing software is not absence and requires explicit verified adoption policy")
	}
	return nil
}

func (n *Native) currentSnapshot(ctx context.Context, config nativeConfig) (snapshot, error) {
	policy := config.Policy
	var pointer receiptPointer
	if err := readRecord(policy.CurrentReceipt, &pointer); err == nil {
		if !domain.ValidOperation(pointer.ID) || !domain.ValidHash(pointer.SHA256) {
			return snapshot{}, domain.Fail(domain.Conflict, "current native software receipt pointer is malformed")
		}
		receipt, hash, err := n.readReceipt(pointer.ID)
		if err != nil {
			return snapshot{}, err
		}
		if hash != pointer.SHA256 {
			return snapshot{}, domain.Fail(domain.Conflict, "current native software receipt is corrupted")
		}
		if err := n.verifyReceipt(config, receipt, hash); err != nil {
			return snapshot{}, err
		}
		if err := n.verifyActivationNamespace(config, receipt); err != nil {
			return snapshot{}, err
		}
		installation := filepath.Join(policy.Prefix, "current")
		service, err := n.readService(ctx, policy, installation, false)
		if err != nil {
			return snapshot{}, err
		}
		current := snapshot{State: "managed", Release: receipt.Permit.Proposal.Release, Payloads: receipt.Payloads,
			Root: receipt.VersionDir, ReceiptID: pointer.ID, ReceiptSHA: hash, Service: service}
		if policy.Method == "debian" {
			current.Package, err = n.readPackage(ctx, policy)
			if err != nil || !current.Package.Present || current.Package.Version != current.Release.Manifest.Version {
				return snapshot{}, domain.Fail(domain.Conflict, "managed Debian package no longer matches its exact native receipt")
			}
			if err := verifyTree(policy.PackageRoot, current.Payloads, false); err != nil {
				return snapshot{}, err
			}
		}
		return current, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return snapshot{}, err
	}
	if policy.Current != nil {
		return snapshot{}, domain.Fail(domain.Prerequisite, "enrolled managed baseline lacks its exact current native receipt")
	}
	if err := absentDirectory(policy.Prefix); err != nil {
		return snapshot{}, err
	}
	if policy.Adoption != nil {
		baseline := policy.Adoption
		if err := VerifyRelease(baseline.Release, config.PublicKey); err != nil {
			return snapshot{}, err
		}
		platform, err := platformFor(baseline.Release, policy.Platform)
		if err != nil {
			return snapshot{}, err
		}
		payloads := platform.Payloads
		current := snapshot{State: "adoptable", Release: baseline.Release, Root: baseline.Path}
		if policy.Method == "debian" {
			payload, err := packagePayload(policy, baseline.Release)
			if err != nil {
				return snapshot{}, err
			}
			hash, size, err := digestFile(baseline.PackageFile, domain.MaxSoftwareBundleBytes, false)
			if err != nil || hash != payload.SHA256 || size != payload.Size {
				return snapshot{}, domain.Fail(domain.Conflict, "approved Debian baseline package bytes do not match their distribution signature")
			}
			payloads, err = n.inspectPackage(ctx, policy, baseline.PackageFile, baseline.Release, "")
			if err != nil {
				return snapshot{}, err
			}
			current.Package, err = n.readPackage(ctx, policy)
			if err != nil || !current.Package.Present || current.Package.Version != baseline.Release.Manifest.Version {
				return snapshot{}, domain.Fail(domain.Conflict, "approved Debian baseline does not match the actual registered package")
			}
		}
		if err := verifyTree(baseline.Path, payloads, false); err != nil {
			return snapshot{}, err
		}
		current.Payloads = sortedPayloads(payloads)
		current.Service, err = n.readService(ctx, policy, baseline.Path, false)
		if err != nil {
			return snapshot{}, err
		}
		return current, nil
	}
	current := snapshot{State: "absent"}
	var err error
	current.Service, err = n.readService(ctx, policy, "", true)
	if err != nil {
		return snapshot{}, err
	}
	if policy.Method == "debian" {
		if err := absentDirectory(policy.PackageRoot); err != nil {
			return snapshot{}, err
		}
		current.Package, err = n.readPackage(ctx, policy)
		if err != nil || current.Package.Present {
			return snapshot{}, domain.Fail(domain.Conflict, "first Debian install requires proven package and package-prefix absence")
		}
	}
	return current, nil
}

func (n *Native) checkDependencies(config nativeConfig, release domain.SoftwareRelease) error {
	platform, err := platformFor(release, config.Policy.Platform)
	if err != nil {
		return err
	}
	references := make([]domain.SoftwareDependency, 0, len(config.Policy.Dependencies))
	for _, dependency := range config.Policy.Dependencies {
		references = append(references, dependency.Reference)
		if err := protectedPath(dependency.ReceiptFile, uint32(os.Geteuid()), false); err != nil {
			return err
		}
		data, err := secure.Read(dependency.ReceiptFile, maxRecordBytes)
		if err != nil || domain.Hash(string(data)) != dependency.ReceiptSHA256 {
			return domain.Fail(domain.Prerequisite, "software dependency lacks its exact protected native receipt")
		}
		var receipt Receipt
		if err := decodeJSON(data, &receipt); err != nil {
			return err
		}
		permit := receipt.Permit
		installed, err := platformFor(permit.Proposal.Release, config.Policy.Platform)
		if err != nil || receipt.PolicySHA256 != dependency.PolicySHA256 ||
			permit.Proposal.Release.Manifest.ProductID != dependency.Reference.ProductID ||
			permit.Proposal.Release.ID != dependency.Reference.ReleaseID || installed.SHA256 != dependency.Reference.SHA256 ||
			permit.Proposal.Challenge.InstanceID != config.Policy.InstanceID || permit.Proposal.Challenge.TargetID != config.Policy.TargetID ||
			permit.Proposal.Challenge.BindingID != config.Policy.BindingID || receipt.Result.State != "installed" ||
			permit.Proposal.Challenge.Generation > config.Policy.Generation ||
			receipt.LeaseRequest.PermitSHA256 != domain.Hash(permit.Signature) || !validNonce(receipt.LeaseRequest.Nonce) {
			return domain.Fail(domain.Prerequisite, "software dependency receipt belongs to another source, target or policy")
		}
		if err := VerifyRelease(permit.Proposal.Release, config.PublicKey); err != nil {
			return err
		}
		if err := VerifyPermit(permit, config.CA, time.Unix(receipt.Lease.IssuedAt, 0)); err != nil {
			return err
		}
		if err := upgrade.VerifyLease(receipt.Lease, receipt.LeaseRequest, config.CA, time.Unix(receipt.Lease.IssuedAt, 0)); err != nil {
			return err
		}
		if err := ValidateResult(permit, receipt.Result); err != nil {
			return err
		}
		if installed.Profile != "debian" {
			if err := VerifyPayloads(receipt.Payloads, installed); err != nil {
				return err
			}
		}
		hash, _, err := digestFile(receipt.BackupFile, MaxBackupEncryptedBytes, true)
		if err != nil || hash != receipt.Result.BackupSHA256 || receipt.BackupSHA256 != hash {
			return domain.Fail(domain.Prerequisite, "software dependency cold-backup proof is missing or corrupted")
		}
		name, err := immutableName(permit.Proposal.Release)
		if err != nil || receipt.VersionDir != filepath.Join(dependency.Prefix, "versions", name) {
			return domain.Fail(domain.Conflict, "software dependency activation is outside its pinned immutable prefix")
		}
		if err := verifyTree(receipt.VersionDir, receipt.Payloads, true); err != nil {
			return err
		}
	}
	if !slices.Equal(sortedDependencies(references), sortedDependencies(platform.Dependencies)) {
		return domain.Fail(domain.Prerequisite, "release dependencies do not match the explicitly provisioned native prerequisite receipts")
	}
	return nil
}

func (n *Native) checkRequirements(ctx context.Context, config nativeConfig) error {
	if n.requirements != nil {
		return n.requirements(ctx, config.Policy)
	}
	return n.nativeRequirements(ctx, config.Policy)
}

func (n *Native) observation(id string, config nativeConfig, current snapshot) domain.SoftwareChallenge {
	challenge := domain.SoftwareChallenge{ID: id, InstanceID: config.Policy.InstanceID, TargetID: config.Policy.TargetID,
		BindingID: config.Policy.BindingID, Generation: config.Policy.Generation, ProductID: config.Policy.ProductID,
		Platform: config.Policy.Platform, State: current.State, PolicySHA256: config.PolicySHA256}
	if current.State != "absent" {
		platform, _ := platformFor(current.Release, config.Policy.Platform)
		challenge.CurrentVersion, challenge.CurrentRelease, challenge.CurrentSHA256 =
			current.Release.Manifest.Version, current.Release.ID, platform.SHA256
	}
	return challenge
}

func (n *Native) challenge(ctx context.Context, config nativeConfig, id string) (domain.SoftwareNativeReply, error) {
	if err := n.pendingNative(config, id); err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	if err := n.checkRequirements(ctx, config); err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	current, err := n.currentSnapshot(ctx, config)
	if err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	if err := n.validateBackupSources(config, current); err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	var record challengeRecord
	if err := readRecord(n.challengeFilename(id), &record); err == nil {
		if record.Challenge.PolicySHA256 != config.PolicySHA256 || !equalJSON(record.Snapshot, current) {
			return domain.SoftwareNativeReply{}, domain.Fail(domain.Conflict, "native source or policy changed; an original challenge cannot be renewed or rebound")
		}
		return domain.SoftwareNativeReply{State: "planned", Challenge: &record.Challenge}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.SoftwareNativeReply{}, err
	}
	if err := ensurePrivate(n.jobDirectory(id)); err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	challenge := n.observation(id, config, current)
	challenge.Nonce, challenge.ExpiresAt = domain.NewToken(), n.clock().Unix()+AuthorizationTTL
	record = challengeRecord{Challenge: challenge, Snapshot: current}
	if err := writeRecord(n.challengeFilename(id), record, true); err != nil {
		return domain.SoftwareNativeReply{}, err
	}
	return domain.SoftwareNativeReply{State: "planned", Challenge: &challenge}, nil
}

func (n *Native) validateNewPermit(ctx context.Context, config nativeConfig, permit domain.SoftwarePermit) (challengeRecord, error) {
	if err := VerifyPermit(permit, config.CA, n.clock()); err != nil {
		return challengeRecord{}, err
	}
	if err := VerifyRelease(permit.Proposal.Release, config.PublicKey); err != nil {
		return challengeRecord{}, err
	}
	policy, challenge, manifest := config.Policy, permit.Proposal.Challenge, permit.Proposal.Release.Manifest
	if challenge.PolicySHA256 != config.PolicySHA256 || challenge.InstanceID != policy.InstanceID ||
		challenge.TargetID != policy.TargetID || challenge.BindingID != policy.BindingID || challenge.Generation != policy.Generation ||
		challenge.ProductID != policy.ProductID || challenge.Platform != policy.Platform ||
		manifest.InstanceID != policy.InstanceID || manifest.ProductID != policy.ProductID || manifest.Kind != policy.Kind ||
		permit.Proposal.BackupRecipient != policy.BackupRecipient {
		return challengeRecord{}, domain.Fail(domain.Denied, "software authorization does not match the native policy, trust, target and product")
	}
	platform, err := platformFor(permit.Proposal.Release, policy.Platform)
	if err != nil || platform.Profile != policy.Method {
		return challengeRecord{}, domain.Fail(domain.Denied, "software authorization changes the enrolled native installation profile")
	}
	if err := n.checkDependencies(config, permit.Proposal.Release); err != nil {
		return challengeRecord{}, err
	}
	var record challengeRecord
	if err := readRecord(n.challengeFilename(challenge.ID), &record); err != nil {
		return challengeRecord{}, err
	}
	if record.Challenge != challenge {
		return challengeRecord{}, domain.Fail(domain.Conflict, "software authorization did not use the exact original native challenge")
	}
	current, err := n.currentSnapshot(ctx, config)
	if err != nil {
		return challengeRecord{}, err
	}
	if !equalJSON(current, record.Snapshot) {
		return challengeRecord{}, domain.Fail(domain.Conflict, "native software source, receipt or service generation changed after approval")
	}
	if current.State == "managed" && manifest.Revision <= current.Release.Manifest.Revision ||
		current.State == "adoptable" && (manifest.Revision < current.Release.Manifest.Revision ||
			manifest.Revision == current.Release.Manifest.Revision && !equalJSON(manifest, current.Release.Manifest)) {
		return challengeRecord{}, domain.Fail(domain.Conflict, "software replacement must advance product revision; adoption equality requires the exact verified baseline")
	}
	if err := n.validateBackupSources(config, current); err != nil {
		return challengeRecord{}, err
	}
	return record, nil
}

func (n *Native) validatePrepared(config nativeConfig, job nativeJob) error {
	id := job.Permit.Proposal.Challenge.ID
	platform, err := platformFor(job.Permit.Proposal.Release, config.Policy.Platform)
	if err != nil {
		return err
	}
	hash, size, err := digestFile(n.bundleFilename(id), domain.MaxSoftwareBundleBytes, true)
	if err != nil || hash != platform.SHA256 || size != platform.Size {
		return domain.Fail(domain.Conflict, "staged artifact bytes no longer match the exact signed source")
	}
	data, err := secure.Read(n.proofFilename(id), maxRecordBytes)
	if err != nil || domain.Hash(string(data)) != job.PreparedSHA256 {
		return domain.Fail(domain.Conflict, "native prepared payload proof is missing or corrupted")
	}
	var proof preparedProof
	if err := decodeJSON(data, &proof); err != nil {
		return err
	}
	manifest, err := CanonicalManifest(job.Permit.Proposal.Release.Manifest)
	if err != nil || proof.PermitSHA256 != domain.Hash(job.Permit.Signature) ||
		proof.ManifestSHA256 != domain.Hash(string(manifest)) || !slices.Equal(proof.Payloads, job.Payloads) {
		return domain.Fail(domain.Conflict, "native prepared payload proof is not bound to the original permit and manifest")
	}
	if config.Policy.Method != "debian" {
		if err := VerifyPayloads(job.Payloads, platform); err != nil {
			return err
		}
	}
	return verifyTree(n.contentDirectory(id), job.Payloads, false)
}

func (n *Native) prepare(ctx context.Context, config nativeConfig, permit domain.SoftwarePermit) (nativeJob, error) {
	id := permit.Proposal.Challenge.ID
	var existing nativeJob
	if err := readRecord(n.jobFilename(id), &existing); err == nil {
		if !equalJSON(existing.Permit, permit) {
			return nativeJob{}, domain.Fail(domain.Conflict, "native software operation already retained another exact permit; renewal is forbidden")
		}
		if err := n.checkOriginalJob(config, existing); err != nil {
			return nativeJob{}, err
		}
		if existing.State == "prepared" {
			if err := VerifyPermit(existing.Permit, config.CA, n.clock()); err != nil {
				return existing, err
			}
			return existing, n.validatePrepared(config, existing)
		}
		return n.reconcile(config, existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nativeJob{}, err
	}
	if n.Fetch == nil || n.Lease == nil {
		return nativeJob{}, domain.Fail(domain.Prerequisite, "native software requires authenticated artifact custody and a current Foundation start-lease boundary")
	}
	if err := n.pendingNative(config, id); err != nil {
		return nativeJob{}, err
	}
	if err := n.checkRequirements(ctx, config); err != nil {
		return nativeJob{}, err
	}
	record, err := n.validateNewPermit(ctx, config, permit)
	if err != nil {
		return nativeJob{}, err
	}
	reference := permitReference{ChallengeID: id, PermitSHA256: domain.Hash(permit.Signature)}
	index := filepath.Join(config.Policy.StateDir, "permits", permit.ID+".json")
	if _, err := os.Lstat(index); err == nil {
		return nativeJob{}, domain.Fail(domain.Conflict, "native software permit ID already has durable custody; missing effects cannot be retried")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nativeJob{}, err
	}
	job := nativeJob{Permit: permit, State: "preparing", Snapshot: record.Snapshot}
	if err := n.saveJob(job, true); err != nil {
		return nativeJob{}, err
	}
	if err := writeRecord(index, reference, true); err != nil {
		return n.failJob(job, err)
	}
	if _, err := os.Lstat(n.bundleFilename(id)); !errors.Is(err, os.ErrNotExist) {
		return n.failJob(job, domain.Fail(domain.Conflict, "native artifact staging path already exists or is unprovable"))
	}
	if err := n.Fetch(ctx, platformArtifact(permit.Proposal.Release, config.Policy.Platform), n.bundleFilename(id)); err != nil {
		return n.failJob(job, domain.Fail(domain.Unavailable, "authenticated software artifact fetch failed; the original attempt is retained"))
	}
	platform, _ := platformFor(permit.Proposal.Release, config.Policy.Platform)
	hash, size, err := digestFile(n.bundleFilename(id), domain.MaxSoftwareBundleBytes, true)
	if err != nil || hash != platform.SHA256 || size != platform.Size {
		return n.failJob(job, domain.Fail(domain.Conflict, "downloaded software artifact digest or size differs from its signed platform"))
	}
	if err := ensurePrivate(n.outerDirectory(id)); err != nil {
		return n.failJob(job, err)
	}
	bundle, err := os.Open(n.bundleFilename(id))
	if err != nil {
		return n.failJob(job, err)
	}
	extractErr := ExtractBundleReader(bundle, n.outerDirectory(id), platform.Payloads, platform.Size)
	closeErr := bundle.Close()
	if err := errors.Join(extractErr, closeErr); err != nil {
		return n.failJob(job, err)
	}
	job.Payloads = sortedPayloads(platform.Payloads)
	if config.Policy.Method == "debian" {
		if _, err := packagePayload(config.Policy, permit.Proposal.Release); err != nil {
			return n.failJob(job, err)
		}
		if err := ensurePrivate(n.contentDirectory(id)); err != nil {
			return n.failJob(job, err)
		}
		job.Payloads, err = n.inspectPackage(ctx, config.Policy, filepath.Join(n.outerDirectory(id), config.Policy.PackageFile),
			permit.Proposal.Release, n.contentDirectory(id))
		if err != nil {
			return n.failJob(job, err)
		}
	}
	if config.Policy.Service != "" {
		found := false
		for _, payload := range job.Payloads {
			if payload.Path == config.Policy.Executable && payload.Mode&0100 != 0 {
				found = true
			}
		}
		if !found {
			return n.failJob(job, domain.Fail(domain.Denied, "fixed software service executable is not an executable signed payload"))
		}
	}
	manifest, _ := CanonicalManifest(permit.Proposal.Release.Manifest)
	proof := preparedProof{PermitSHA256: domain.Hash(permit.Signature), ManifestSHA256: domain.Hash(string(manifest)), Payloads: job.Payloads}
	if err := writeRecord(n.proofFilename(id), proof, true); err != nil {
		return n.failJob(job, err)
	}
	data, err := secure.Read(n.proofFilename(id), maxRecordBytes)
	if err != nil {
		return n.failJob(job, err)
	}
	job.PreparedSHA256, job.State = domain.Hash(string(data)), "prepared"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	return job, nil
}

func platformArtifact(release domain.SoftwareRelease, platform string) string {
	selected, _ := platformFor(release, platform)
	return selected.ArtifactID
}

func (n *Native) failJob(job nativeJob, cause error) (nativeJob, error) {
	state := "failed"
	if job.Effects {
		state = "operator-required"
	}
	job.Problem = domain.AsFault(cause)
	code := string(job.Problem.Code)
	if job.Effects {
		code = job.State
	}
	job.State = state
	result := n.resultFor(job, state, code)
	job.Result = &result
	saveErr := n.saveJob(job, false)
	outboxErr := n.writeOutbox(job)
	return job, errors.Join(cause, saveErr, outboxErr)
}

func (n *Native) apply(ctx context.Context, config nativeConfig, job nativeJob) (nativeJob, error) {
	if job.State != "prepared" {
		return n.reconcile(config, job)
	}
	if n.Lease == nil {
		return n.failJob(job, domain.Fail(domain.Prerequisite, "current Foundation start-lease boundary is unavailable"))
	}
	if err := n.checkRequirements(ctx, config); err != nil {
		return n.failJob(job, err)
	}
	if _, err := n.validateNewPermit(ctx, config, job.Permit); err != nil {
		return n.failJob(job, err)
	}
	if err := n.validatePrepared(config, job); err != nil {
		return n.failJob(job, err)
	}
	job.LeaseRequest = domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(job.Permit.Signature), Nonce: domain.NewToken()}
	job.State = "lease-requested"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	start := time.Now()
	leaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	lease, leaseErr := n.Lease(leaseCtx, job.LeaseRequest)
	cancel()
	if leaseErr != nil || time.Since(start) > 10*time.Second {
		return n.failJob(job, domain.Fail(domain.Denied, "software start authority is unavailable or its bounded request was uncertain"))
	}
	if err := upgrade.VerifyLease(lease, job.LeaseRequest, config.CA, n.clock()); err != nil {
		return n.failJob(job, err)
	}
	if _, err := n.validateNewPermit(ctx, config, job.Permit); err != nil {
		return n.failJob(job, err)
	}
	if err := n.validatePrepared(config, job); err != nil {
		return n.failJob(job, err)
	}
	fresh, err := n.configuration()
	if err != nil || fresh.PolicySHA256 != config.PolicySHA256 {
		return n.failJob(job, domain.Fail(domain.Conflict, "software policy or pinned authority changed during native admission"))
	}
	if err := n.checkRequirements(ctx, fresh); err != nil {
		return n.failJob(job, err)
	}
	if err := upgrade.VerifyLease(lease, job.LeaseRequest, config.CA, n.clock()); err != nil {
		return n.failJob(job, err)
	}
	if err := VerifyPermit(job.Permit, config.CA, n.clock()); err != nil {
		return n.failJob(job, err)
	}
	job.Lease = lease
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	policy := config.Policy
	id := job.Permit.Proposal.Challenge.ID
	oldServiceRoot := ""
	if job.Snapshot.State == "managed" {
		oldServiceRoot = filepath.Join(policy.Prefix, "current")
	} else if job.Snapshot.State == "adoptable" {
		oldServiceRoot = job.Snapshot.Root
	}
	if policy.Service != "" && job.Snapshot.Service.Loaded {
		job.State, job.Effects = "stopping", true
		if err := n.saveJob(job, false); err != nil {
			return nativeJob{}, err
		}
		if err := n.serviceEffect(ctx, policy, "stop"); err != nil {
			return n.failJob(job, err)
		}
		state, err := n.readService(ctx, policy, oldServiceRoot, false)
		if err != nil || state.Running || n.hostIdentity().OS == "darwin" && state.Loaded {
			return n.failJob(job, domain.Fail(domain.Unavailable, "native software stop was not independently observed as stopped"))
		}
	}
	job.State = "backing-up"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	backup, err := n.coldBackup(config, job)
	if err != nil {
		return n.failJob(job, err)
	}
	job.BackupSHA256 = backup
	job.State, job.Effects = "installing", true
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	if err := protectedPath(policy.Prefix, uint32(os.Geteuid()), true); err != nil {
		return n.failJob(job, err)
	}
	if err := os.MkdirAll(filepath.Join(policy.Prefix, "versions"), 0755); err != nil {
		return n.failJob(job, err)
	}
	if err := protectedPath(filepath.Join(policy.Prefix, "versions"), uint32(os.Geteuid()), false); err != nil {
		return n.failJob(job, err)
	}
	name, err := immutableName(job.Permit.Proposal.Release)
	if err != nil {
		return n.failJob(job, err)
	}
	versionDir := filepath.Join(policy.Prefix, "versions", name)
	if err := copyTree(n.contentDirectory(id), versionDir, job.Payloads); err != nil {
		return n.failJob(job, err)
	}
	if err := secure.SyncDir(filepath.Dir(versionDir)); err != nil {
		return n.failJob(job, err)
	}
	if policy.Method == "debian" {
		if err := n.installPackage(ctx, policy, filepath.Join(n.outerDirectory(id), policy.PackageFile), job.Permit.Proposal.Release, job.Payloads); err != nil {
			return n.failJob(job, err)
		}
	}
	job.State = "activating"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	if err := n.activate(policy, job, name); err != nil {
		return n.failJob(job, err)
	}
	if err := secure.SyncDir(policy.Prefix); err != nil {
		return n.failJob(job, err)
	}
	if policy.Service != "" {
		job.State = "configuring"
		if err := n.saveJob(job, false); err != nil {
			return nativeJob{}, err
		}
		changed, err := n.writeService(policy, id, oldServiceRoot)
		if err != nil {
			return n.failJob(job, err)
		}
		if changed && n.hostIdentity().OS == "linux" {
			if err := n.serviceEffect(ctx, policy, "daemon-reload"); err != nil {
				return n.failJob(job, err)
			}
		}
		if job.Snapshot.State == "absent" && n.hostIdentity().OS == "linux" {
			if err := n.serviceEffect(ctx, policy, "enable"); err != nil {
				return n.failJob(job, err)
			}
		}
		startService := job.Snapshot.State == "absent" || job.Snapshot.Service.Running
		if startService {
			job.State = "starting"
			if err := n.saveJob(job, false); err != nil {
				return nativeJob{}, err
			}
			if err := n.serviceEffect(ctx, policy, "start"); err != nil {
				return n.failJob(job, err)
			}
		}
		state, err := n.readService(ctx, policy, filepath.Join(policy.Prefix, "current"), false)
		if err != nil || state.Running != startService {
			return n.failJob(job, domain.Fail(domain.Unavailable, "activated native software service did not reach its exact intended running state"))
		}
	}
	job.State = "verifying"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	if err := verifyTree(versionDir, job.Payloads, true); err != nil {
		return n.failJob(job, err)
	}
	receipt := n.makeReceipt(config, job, versionDir)
	job.State = "recording"
	if err := n.saveJob(job, false); err != nil {
		return nativeJob{}, err
	}
	if err := writeRecord(n.receiptFilename(id), receipt, true); err != nil {
		return nativeJob{}, err
	}
	return n.reconcile(config, job)
}

func (n *Native) Handle(ctx context.Context, request domain.SoftwareNativeRequest) (reply domain.SoftwareNativeReply, err error) {
	if err := ctx.Err(); err != nil {
		return reply, err
	}
	if !domain.ValidOperation(request.ID) ||
		!slices.Contains([]string{"inspect", "challenge", "prepare", "apply", "status"}, request.Action) ||
		(request.Action == "prepare" || request.Action == "apply") != (request.Permit != nil) {
		return reply, domain.Fail(domain.Invalid, "native software accepts only typed inspect/challenge/prepare/apply/status requests")
	}
	config, err := n.configuration()
	if err != nil {
		return reply, err
	}
	if request.ProductID != "" && request.ProductID != config.Policy.ProductID ||
		request.Permit != nil && request.Permit.Proposal.Challenge.ID != request.ID {
		return reply, domain.Fail(domain.Denied, "native software request selects another product or challenge")
	}
	lock, err := secure.Acquire(filepath.Join(config.Policy.StateDir, "software.lock"))
	if err != nil {
		return reply, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if request.Action == "inspect" {
		if err := n.pendingNative(config, ""); err != nil {
			return reply, err
		}
		if err := n.checkRequirements(ctx, config); err != nil {
			return reply, err
		}
		current, err := n.currentSnapshot(ctx, config)
		if err != nil {
			return reply, err
		}
		challenge := n.observation(request.ID, config, current)
		return domain.SoftwareNativeReply{State: "observed", Challenge: &challenge}, nil
	}
	if request.Action != "status" {
		if err := n.initializeDirectories(); err != nil {
			return reply, err
		}
	}
	if request.Action == "challenge" {
		return n.challenge(ctx, config, request.ID)
	}
	if request.Action == "status" {
		var job nativeJob
		if err := readRecord(n.jobFilename(request.ID), &job); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return reply, domain.Fail(domain.NotFound, "native software operation has no retained job")
			}
			return reply, err
		}
		if job.Permit.Proposal.Challenge.ID != request.ID {
			return reply, domain.Fail(domain.Conflict, "native software job was moved or replaced")
		}
		if err := n.checkOriginalJob(config, job); err != nil {
			return reply, err
		}
		if job.State == "prepared" {
			if err := n.validatePrepared(config, job); err != nil {
				return reply, err
			}
			return jobReply(job)
		}
		reconciled, err := n.reconcile(config, job)
		if err != nil {
			result, _ := jobReply(reconciled)
			return result, err
		}
		return jobReply(reconciled)
	}
	job, err := n.prepare(ctx, config, *request.Permit)
	if err != nil {
		reply, _ := jobReply(job)
		return reply, err
	}
	if request.Action == "apply" && !terminal(job.State) {
		job, err = n.apply(ctx, config, job)
		if err != nil {
			reply, _ := jobReply(job)
			return reply, err
		}
	}
	return jobReply(job)
}

func (n *Native) resultFor(job nativeJob, state, code string) domain.SoftwareResult {
	release := job.Permit.Proposal.Release
	platform, _ := platformFor(release, job.Permit.Proposal.Challenge.Platform)
	return domain.SoftwareResult{PermitID: job.Permit.ID, PermitSHA256: domain.Hash(job.Permit.Signature),
		ProductID: release.Manifest.ProductID, ReleaseID: release.ID, ArtifactID: platform.ArtifactID, SHA256: platform.SHA256,
		BackupSHA256: job.BackupSHA256, State: state, Code: code}
}

func currentLink(policy Policy, versionDir string) (string, error) {
	name, err := filepath.Rel(policy.Prefix, versionDir)
	if err != nil || !inside(filepath.Join(policy.Prefix, "versions"), versionDir) || filepath.Dir(name) != "versions" {
		return "", domain.Fail(domain.Conflict, "software activation receipt is outside its exact immutable version prefix")
	}
	return name, nil
}

func (n *Native) activate(policy Policy, job nativeJob, name string) error {
	current := filepath.Join(policy.Prefix, "current")
	target := filepath.Join("versions", name)
	if job.Snapshot.State != "managed" {
		return os.Symlink(target, current)
	}
	before, err := os.Lstat(current)
	if err != nil {
		return err
	}
	expected, err := currentLink(policy, job.Snapshot.Root)
	if err != nil {
		return err
	}
	actual, err := os.Readlink(current)
	if err != nil || actual != expected || before.Mode()&os.ModeSymlink == 0 {
		return domain.Fail(domain.Conflict, "native activation changed after approval; refusing to overwrite user or unknown state")
	}
	var pointer receiptPointer
	if err := readRecord(policy.CurrentReceipt, &pointer); err != nil ||
		pointer.ID != job.Snapshot.ReceiptID || pointer.SHA256 != job.Snapshot.ReceiptSHA {
		return domain.Fail(domain.Conflict, "native current receipt changed before activation")
	}
	next := filepath.Join(policy.Prefix, ".current-"+job.Permit.Proposal.Challenge.ID)
	if err := os.Symlink(target, next); err != nil {
		return err
	}
	after, err := os.Lstat(current)
	if err != nil || !os.SameFile(before, after) {
		return domain.Fail(domain.Conflict, "native activation resource was replaced before atomic exchange")
	}
	return os.Rename(next, current)
}

func (n *Native) verifyActivationNamespace(config nativeConfig, current Receipt) error {
	expected, err := currentLink(config.Policy, current.VersionDir)
	if err != nil {
		return err
	}
	actual, err := os.Readlink(filepath.Join(config.Policy.Prefix, "current"))
	if err != nil || actual != expected {
		return domain.Fail(domain.Conflict, "native software current activation differs from its exact receipt")
	}
	entries, err := os.ReadDir(config.Policy.Prefix)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "current" && entry.Name() != "versions" {
			return domain.Fail(domain.Conflict, "managed software prefix contains an unrecognized resource")
		}
	}
	known := make(map[string]bool)
	receipts, err := os.ReadDir(filepath.Join(config.Policy.StateDir, "receipts"))
	if err != nil || len(receipts) > MaxNativeJobs {
		return domain.Fail(domain.Prerequisite, "native software receipt inventory is missing or exceeds its bound")
	}
	for _, entry := range receipts {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return domain.Fail(domain.Denied, "native software receipt inventory contains an unrecognized resource")
		}
		var receipt Receipt
		if err := readRecord(filepath.Join(config.Policy.StateDir, "receipts", entry.Name()), &receipt); err != nil {
			return err
		}
		name, err := immutableName(receipt.Permit.Proposal.Release)
		if err != nil || receipt.VersionDir != filepath.Join(config.Policy.Prefix, "versions", name) ||
			receipt.Permit.Proposal.Release.Manifest.ProductID != config.Policy.ProductID {
			return domain.Fail(domain.Conflict, "native software version inventory is not source-bound")
		}
		known[name] = true
	}
	versions, err := os.ReadDir(filepath.Join(config.Policy.Prefix, "versions"))
	if err != nil || len(versions) > MaxNativeJobs {
		return domain.Fail(domain.Prerequisite, "native software version inventory is missing or exceeds its bound")
	}
	for _, entry := range versions {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !known[entry.Name()] {
			return domain.Fail(domain.Conflict, "native software prefix contains an unknown unreceipted version")
		}
	}
	return nil
}

func resultKey(permitID string) string { return permitID + ".json" }
