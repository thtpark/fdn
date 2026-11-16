package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type SoftwareBridgeTrust struct {
	VerifyPermit         func(domain.SoftwarePermit, []byte, time.Time) error
	VerifyAdmittedPermit func(domain.SoftwarePermit, []byte, time.Time) error
	VerifyRelease        func(domain.SoftwareRelease, []byte) error
	Extract              func([]byte, string, []domain.SoftwarePayload) error
	Fetch                func(context.Context, string, string) error
	Lease                func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error)
}

type SoftwareBridge struct {
	Native *Native
	Trust  SoftwareBridgeTrust
}

type softwareBridgeJournal struct {
	Permit  domain.SoftwarePermit  `json:"permit"`
	State   string                 `json:"state"`
	Payload string                 `json:"payload"`
	Backup  string                 `json:"backup"`
	Result  *domain.SoftwareResult `json:"result,omitempty"`
}

func (b *SoftwareBridge) bridgeDirectory(id string) (string, error) {
	if !domain.ValidOperation(id) {
		return "", domain.Fail(domain.Invalid, "internal updater requires its exact native challenge ID")
	}
	path := filepath.Join(b.Native.Policy.StateDir, "internal-"+id)
	return path, secure.Dir(path)
}

func (b *SoftwareBridge) Handle(ctx context.Context, request domain.SoftwareNativeRequest) (domain.SoftwareNativeReply, error) {
	var reply domain.SoftwareNativeReply
	n, p := b.Native, b.Native.Policy
	if !p.EnableInternalSoftware || p.InternalProductID != request.ProductID || b.Trust.VerifyPermit == nil ||
		b.Trust.VerifyAdmittedPermit == nil || b.Trust.VerifyRelease == nil ||
		b.Trust.Extract == nil || b.Trust.Fetch == nil || b.Trust.Lease == nil {
		return reply, domain.Fail(domain.Prerequisite, "Foundation internal updater bridge is not independently provisioned for this exact product")
	}
	if err := p.Validate(); err != nil {
		return reply, err
	}
	directory, err := b.bridgeDirectory(request.ID)
	if err != nil {
		return reply, err
	}
	lock, err := secure.Acquire(filepath.Join(p.StateDir, "internal-updater.lock"))
	if err != nil {
		return reply, err
	}
	defer lock.Close()
	if request.Action == "inspect" || request.Action == "challenge" {
		if request.Permit != nil {
			return reply, domain.Fail(domain.Invalid, "internal updater observation accepts no installation permit")
		}
		action := "inspect"
		if request.Action == "challenge" {
			action = "challenge"
		}
		observed, err := n.Handle(ctx, domain.UpgradeNativeRequest{Action: action, ID: request.ID})
		if err != nil {
			return reply, err
		}
		reply.State = "observed"
		if request.Action == "inspect" {
			return reply, nil
		}
		c := observed.Challenge
		if c == nil {
			return reply, domain.Fail(domain.Prerequisite, "existing native updater did not produce its exact source challenge")
		}
		binary := "agent-cli"
		binding := p.TargetID
		if p.Role == "foundation" {
			binary, binding = "fdn-service", ""
		}
		digest, err := fileHash(filepath.Join(p.BinaryDir, binary))
		if err != nil {
			return reply, err
		}
		challenge := domain.SoftwareChallenge{ID: c.ID, Nonce: c.Nonce, InstanceID: c.InstanceID, TargetID: c.TargetID, BindingID: binding,
			Generation: c.Generation, ProductID: p.InternalProductID, Platform: observed.Platform, State: "adoptable",
			CurrentSHA256: digest, CurrentVersion: c.CurrentVersion, CurrentRelease: "baseline_" + domain.Hash(c.CurrentVersion + ":" + c.CurrentCommit + ":" + digest)[:32],
			PolicySHA256: n.policyHash(), ExpiresAt: c.ExpiresAt}
		if challenge.Platform == "" {
			challenge.Platform = runtime.GOOS + "-" + runtime.GOARCH
		}
		challengePath := filepath.Join(directory, "challenge.json")
		var retained domain.SoftwareChallenge
		if err := config.Read(challengePath, &retained); err == nil {
			if retained.ID != challenge.ID || retained.Nonce != challenge.Nonce {
				return reply, domain.Fail(domain.Conflict, "retained internal native challenge differs")
			}
			challenge = retained
		} else if errors.Is(err, os.ErrNotExist) {
			if err := config.Write(challengePath, challenge); err != nil {
				return reply, err
			}
		} else {
			return reply, err
		}
		reply.Challenge = &challenge
		return reply, nil
	}
	jobPath := filepath.Join(directory, "job.json")
	var job softwareBridgeJournal
	jobErr := config.Read(jobPath, &job)
	if request.Action == "status" {
		if request.Permit != nil {
			return reply, domain.Fail(domain.Invalid, "status cannot supply replacement authority")
		}
		if jobErr != nil {
			return reply, jobErr
		}
		return b.bridgeReply(ctx, job)
	}
	if (request.Action != "prepare" && request.Action != "apply") || request.Permit == nil {
		return reply, domain.Fail(domain.Invalid, "internal updater accepts inspect/challenge/prepare/apply/status only")
	}
	permit := *request.Permit
	if permit.Proposal.Challenge.ID != request.ID || permit.Proposal.Release.Manifest.Kind != "foundation" ||
		permit.Proposal.Challenge.ProductID != p.InternalProductID || permit.Proposal.Challenge.TargetID != p.TargetID ||
		permit.Proposal.Challenge.InstanceID != p.InstanceID || permit.Proposal.Challenge.PolicySHA256 != n.policyHash() {
		return reply, domain.Fail(domain.Denied, "internal Foundation permission differs from the exact pinned native updater")
	}
	ca, err := secure.ReadPublic(p.CAFile, domain.MaxBody)
	if err != nil {
		return reply, err
	}
	public, err := secure.ReadPublic(p.DistributionPublicKeyFile, 16<<10)
	if err != nil {
		return reply, err
	}
	if jobErr == nil {
		old, err := json.Marshal(job.Permit)
		if err != nil {
			return reply, err
		}
		next, err := json.Marshal(permit)
		if err != nil {
			return reply, err
		}
		if !bytes.Equal(old, next) {
			return reply, domain.Fail(domain.Conflict, "native internal job has different original authority")
		}
		if job.State == "verified" {
			return b.bridgeReply(ctx, job)
		}
		if job.State != "prepared" {
			return reply, domain.Fail(domain.Prerequisite, "internal native effects are uncertain; inspect original status, never rerun")
		}
	} else if !errors.Is(jobErr, os.ErrNotExist) {
		return reply, jobErr
	}
	verify := b.Trust.VerifyPermit
	admitted := jobErr == nil && job.State == "prepared"
	if admitted {
		if err := n.verifyColdBackup(directory, job.Backup); err != nil {
			return reply, err
		}
		verify = b.Trust.VerifyAdmittedPermit
	}
	if err := verify(permit, ca, n.Now()); err != nil {
		return reply, err
	}
	if err := b.Trust.VerifyRelease(permit.Proposal.Release, public); err != nil {
		return reply, err
	}
	if admitted && request.Action == "prepare" {
		return domain.SoftwareNativeReply{State: "prepared"}, nil
	}
	var challenge domain.SoftwareChallenge
	if err := config.Read(filepath.Join(directory, "challenge.json"), &challenge); err != nil {
		return reply, err
	}
	if challenge != permit.Proposal.Challenge {
		return reply, domain.Fail(domain.Conflict, "internal native proposal changed its retained observation")
	}
	platform := (*domain.SoftwarePlatform)(nil)
	for i := range permit.Proposal.Release.Manifest.Platforms {
		candidate := &permit.Proposal.Release.Manifest.Platforms[i]
		if candidate.Platform == challenge.Platform {
			platform = candidate
		}
	}
	if platform == nil {
		return reply, domain.Fail(domain.Prerequisite, "internal release has no exact native platform")
	}
	if request.Action == "prepare" {
		version, commit, err := n.installed(ctx)
		if err != nil {
			return reply, err
		}
		order, err := CompareVersion(version, permit.Proposal.Release.Manifest.Version)
		if err != nil || order >= 0 {
			return reply, domain.Fail(domain.Conflict, "internal Foundation release must advance the actual stable installed version")
		}
		binary := "agent-cli"
		if p.Role == "foundation" {
			binary = "fdn-service"
		}
		digest, err := fileHash(filepath.Join(p.BinaryDir, binary))
		if err != nil {
			return reply, err
		}
		if challenge.CurrentSHA256 != digest || challenge.CurrentRelease != "baseline_"+domain.Hash(version + ":" + commit + ":" + digest)[:32] {
			return reply, domain.Fail(domain.Conflict, "native Foundation source changed after planning")
		}
		if generation, err := n.target(ctx); err != nil || generation != challenge.Generation {
			return reply, domain.Fail(domain.Conflict, "native identity changed after internal planning")
		}
		path := filepath.Join(directory, "payload.tar.gz")
		if err := b.Trust.Fetch(ctx, platform.ArtifactID, path); err != nil {
			return reply, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return reply, err
		}
		if info.Size() != platform.Size {
			return reply, domain.Fail(domain.Conflict, "staged internal payload size differs")
		}
		actual, err := fileHash(path)
		if err != nil || actual != platform.SHA256 {
			return reply, domain.Fail(domain.Conflict, "staged internal payload digest differs")
		}
		data, err := secure.Read(path, domain.MaxSoftwareBundleBytes)
		if err != nil {
			return reply, err
		}
		payload := filepath.Join(directory, "payload")
		if p.Method == "archive" {
			if err := secure.Dir(filepath.Join(p.Prefix, "releases")); err != nil {
				return reply, err
			}
			payload = filepath.Join(p.Prefix, "releases", "internal-"+permit.Proposal.Release.ID)
		}
		if err := b.Trust.Extract(data, payload, platform.Payloads); err != nil {
			return reply, err
		}
		if p.Method == "archive" {
			for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
				found := false
				for _, entry := range platform.Payloads {
					found = found || entry.Path == "bin/"+name && entry.Mode == 0755
				}
				if !found {
					return reply, domain.Fail(domain.Prerequisite, "internal Foundation source must include every native executable")
				}
			}
		} else {
			if _, err := b.debianPayload(permit, *platform); err != nil {
				return reply, err
			}
		}
		if err := filepath.WalkDir(payload, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0755)
			}
			return nil
		}); err != nil {
			return reply, err
		}
		if _, err := n.waitIdle(ctx); err != nil {
			return reply, err
		}
		if _, err := n.backupPaths(); err != nil {
			return reply, err
		}
		nonce := domain.NewToken()
		leaseReq := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: nonce}
		start := time.Now()
		lease, err := b.Trust.Lease(ctx, leaseReq)
		if err != nil {
			return reply, err
		}
		if time.Since(start) > 30*time.Second {
			return reply, domain.Fail(domain.Denied, "internal start proof exceeded its monotonic lifetime")
		}
		if err := VerifyLease(lease, leaseReq, ca, n.Now()); err != nil {
			return reply, err
		}
		job = softwareBridgeJournal{Permit: permit, State: "stopping", Payload: payload}
		if err := config.Write(jobPath, job); err != nil {
			return reply, err
		}
		if err := n.services(ctx, "stop", challenge.Generation); err != nil {
			return reply, err
		}
		workLock, err := n.lockApplication()
		if err != nil {
			return reply, err
		}
		job.Backup, err = n.backup(ctx, directory, permit.Proposal.BackupRecipient)
		closeErr := workLock.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return reply, err
		}
		job.State = "prepared"
		if err := config.Write(jobPath, job); err != nil {
			return reply, err
		}
		return domain.SoftwareNativeReply{State: "prepared"}, nil
	}
	job.State = "installing"
	if err := config.Write(jobPath, job); err != nil {
		return reply, err
	}
	workLock, err := n.lockApplication()
	if err != nil {
		return reply, err
	}
	if p.Method == "archive" {
		link := filepath.Join(p.Prefix, "current")
		temporary := filepath.Join(p.Prefix, "current-"+request.ID)
		if err := os.Symlink(job.Payload, temporary); err != nil {
			return reply, errors.Join(err, workLock.Close())
		}
		if err := os.Rename(temporary, link); err != nil {
			return reply, errors.Join(err, workLock.Close())
		}
	} else {
		path, err := b.debianPayload(permit, *platform)
		if err != nil {
			return reply, errors.Join(err, workLock.Close())
		}
		if err := n.command(ctx, "/usr/bin/dpkg", []string{"-i", filepath.Join(job.Payload, path)}, false, nil); err != nil {
			return reply, errors.Join(err, workLock.Close())
		}
	}
	if err := workLock.Close(); err != nil {
		return reply, err
	}
	if err := n.services(ctx, "start", challenge.Generation); err != nil {
		return reply, err
	}
	job.State = "verified"
	result := domain.SoftwareResult{PermitID: permit.ID, PermitSHA256: domain.Hash(permit.Signature), ProductID: p.InternalProductID,
		ReleaseID: permit.Proposal.Release.ID, ArtifactID: platform.ArtifactID, SHA256: platform.SHA256, BackupSHA256: job.Backup, State: "verified"}
	job.Result = &result
	reply, err = b.bridgeReply(ctx, job)
	if err != nil {
		return reply, err
	}
	return reply, config.Write(jobPath, job)
}

func (b *SoftwareBridge) bridgeReply(ctx context.Context, job softwareBridgeJournal) (domain.SoftwareNativeReply, error) {
	reply := domain.SoftwareNativeReply{State: job.State, Result: job.Result}
	if job.State != "verified" {
		if job.State != "prepared" {
			reply.State = "operator-required"
		}
		return reply, nil
	}

	version, commit, err := b.Native.installed(ctx)
	if err != nil {
		return reply, err
	}
	if version != job.Permit.Proposal.Release.Manifest.Version || commit != job.Permit.Proposal.Release.Manifest.Source.Commit {
		return reply, domain.Fail(domain.Conflict, "internal native receipt is not the actual installed source")
	}
	check := b.Native.CheckServices
	if check == nil {
		check = b.Native.checkServices
	}
	if err := check(ctx); err != nil {
		return reply, err
	}
	return reply, nil
}

func (b *SoftwareBridge) debianPayload(permit domain.SoftwarePermit, platform domain.SoftwarePlatform) (string, error) {
	role := "agent"
	if b.Native.Policy.Role == "foundation" {
		role = "foundation"
	}
	name := "fdn-" + role + "_" + permit.Proposal.Release.Manifest.Version + "_" + strings.TrimPrefix(platform.Platform, "linux-") + ".deb"
	if len(platform.Payloads) != 1 || filepath.Base(platform.Payloads[0].Path) != name {
		return "", domain.Fail(domain.Denied, "internal Debian updater requires the exact fixed Foundation/Agent role package")
	}
	return platform.Payloads[0].Path, nil
}

func (b *SoftwareBridge) PendingResults() ([]domain.SoftwareResult, error) {
	entries, err := os.ReadDir(b.Native.Policy.StateDir)
	if err != nil {
		return nil, err
	}
	results := []domain.SoftwareResult{}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "internal-") {
			continue
		}
		var job softwareBridgeJournal
		if err := config.Read(filepath.Join(b.Native.Policy.StateDir, entry.Name(), "job.json"), &job); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if job.Result == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.Native.Policy.StateDir, entry.Name(), "ack")); errors.Is(err, os.ErrNotExist) {
			results = append(results, *job.Result)
		} else if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (b *SoftwareBridge) AcknowledgeResult(id, fingerprint string) error {
	entries, err := os.ReadDir(b.Native.Policy.StateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "internal-") {
			continue
		}
		var job softwareBridgeJournal
		path := filepath.Join(b.Native.Policy.StateDir, entry.Name())
		if err := config.Read(filepath.Join(path, "job.json"), &job); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if job.Permit.ID == id && domain.Hash(job.Permit.Signature) == fingerprint {
			return secure.Write(filepath.Join(path, "ack"), []byte(fingerprint))
		}
	}
	return domain.Fail(domain.NotFound, "original internal native result is unavailable")
}
