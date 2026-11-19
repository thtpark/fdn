package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func (b *SoftwareBridge) proveBeforeManagedActivation(ctx context.Context, job softwareBridgeJournal, req SoftwareRecoveryRequest, directory string, public []byte) ([]byte, error) {
	n, p := b.Native, b.Native.Policy
	challenge := job.Permit.Proposal.Challenge
	if p.Method != "archive" || challenge.Platform != runtime.GOOS+"-"+runtime.GOARCH ||
		job.Payload != filepath.Join(p.Prefix, "releases", "internal-"+job.Permit.Proposal.Release.ID) {
		return nil, domain.Fail(domain.Denied, "recovery is only for the original unused archive bridge layout")
	}
	var retained domain.SoftwareChallenge
	if err := config.Read(filepath.Join(directory, "challenge.json"), &retained); err != nil {
		return nil, err
	}
	if retained != challenge {
		return nil, domain.Fail(domain.Conflict, "original signed challenge differs from retained native observation")
	}
	current, err := managedArchive(p.Prefix)
	if err != nil {
		return nil, err
	}
	if err := managedLink(filepath.Join(p.Prefix, "current"), job.Payload); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(p.Prefix, "current-"+req.ID)); !os.IsNotExist(err) {
		return nil, domain.Fail(domain.Conflict, "archive activation has an unproved temporary link")
	}
	if err := n.verifyColdBackup(directory, job.Backup); err != nil {
		return nil, err
	}
	var backup backupStreamResult
	if err := config.Read(filepath.Join(directory, "backup-result.json"), &backup); err != nil {
		return nil, err
	}
	if backup.State != "complete" || backup.Error != "" {
		return nil, domain.Fail(domain.Conflict, "original cold backup did not complete successfully")
	}
	if err := protected(req.BaselineReleaseFile, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	raw, err := secure.Read(req.BaselineReleaseFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	var baseline domain.SoftwareRelease
	if err := config.Decode(raw, &baseline); err != nil {
		return nil, err
	}
	if err := b.Trust.VerifyRelease(baseline, public); err != nil {
		return nil, err
	}
	version, commit, err := n.installed(ctx)
	if err != nil {
		return nil, err
	}
	if baseline.Revoked || baseline.ID == job.Permit.Proposal.Release.ID || baseline.Manifest.Kind != "foundation" || baseline.Manifest.ProductID != p.InternalProductID ||
		baseline.Manifest.InstanceID != p.InstanceID || baseline.Manifest.InstanceID != challenge.InstanceID ||
		baseline.Manifest.Version != version || baseline.Manifest.Source.Commit != commit {
		return nil, domain.Fail(domain.Conflict, "signed baseline is not the unchanged original installed source")
	}
	if order, err := CompareVersion(version, job.Permit.Proposal.Release.Manifest.Version); err != nil || order >= 0 {
		return nil, domain.Fail(domain.Conflict, "original archive permission does not advance the signed baseline")
	}
	for _, release := range []domain.SoftwareRelease{baseline, job.Permit.Proposal.Release} {
		found := false
		for _, platform := range release.Manifest.Platforms {
			if platform.Platform != challenge.Platform {
				continue
			}
			root := current
			if release.ID == job.Permit.Proposal.Release.ID {
				root = job.Payload
			}
			if found || platform.Profile != "archive" {
				return nil, domain.Fail(domain.Denied, "recovery requires one exact signed archive platform")
			}
			if err := verifyArchivePayload(root, platform); err != nil {
				return nil, err
			}
			found = true
		}
		if !found {
			return nil, domain.Fail(domain.Denied, "signed release omits the original native platform")
		}
	}
	var agent domain.Agent
	if err := n.jsonCommand(ctx, filepath.Join(p.BinaryDir, "agent-cli"),
		[]string{"--config", p.ConfigFile, "agent", "show", p.TargetID}, true, &agent); err != nil {
		return nil, err
	}
	if agent.ID != p.TargetID || !agent.Active || agent.Banned || agent.Mode != "manual" || agent.Revision != req.ExpectedModeRevision {
		return nil, domain.Fail(domain.Conflict, "original Founder manual intent changed or is not provable")
	}
	attributed := agent.ModeRequestedBy == job.Permit.ActorID && agent.ModeChangedAt > 0 && agent.ModeChangedAt < job.Permit.IssuedAt
	if !attributed {
		if !agent.Founder || agent.ID != job.Permit.ActorID || agent.ModeRequestedBy != "" || agent.ModeChangedAt != 0 ||
			agent.CreatedAt <= 0 || agent.CreatedAt >= job.Permit.IssuedAt {
			return nil, domain.Fail(domain.Conflict, "unattributed manual intent requires the original self-Founder")
		}
		var identity domain.AgentIdentity
		if err := n.jsonCommand(ctx, filepath.Join(p.BinaryDir, "agent-cli"),
			[]string{"--config", p.ConfigFile, "agent", "identity"}, true, &identity); err != nil {
			return nil, err
		}
		if identity.AgentID != agent.ID || identity.InstanceID != p.InstanceID || identity.State != "active" ||
			identity.BindingID() != job.Permit.ActorBindingID ||
			identity.Generation != job.Permit.ActorGeneration || identity.Generation != challenge.Generation {
			return nil, domain.Fail(domain.Conflict, "self-Founder recovery requires its original current binding and generation")
		}
	}
	return raw, nil
}
