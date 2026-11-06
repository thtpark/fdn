package upgrade

import (
	"github.com/thtpark/fdn/internal/domain"
)

const PreflightFailureMessage = "Native preflight failed on the target; inspect its private evidence before retrying"

func ValidateMailboxCommand(request domain.MessageWorkerCommand) error {
	if !request.Preflight && request.PreflightReport == nil {
		return nil
	}
	if request.MessageID != "" || request.Report != nil || request.Preflight && request.PreflightReport != nil {
		return domain.Fail(domain.Invalid, "native preflight mailbox accepts exactly one bounded inbox or result operation")
	}
	if request.PreflightReport != nil && (!domain.ValidOperation(request.PreflightReport.ID) ||
		!domain.ValidOperation(request.PreflightReport.OperationID) || request.PreflightReport.ExpectedRevision <= 0) {
		return domain.Fail(domain.Invalid, "native preflight result requires its exact request, operation and revision")
	}
	return nil
}

func ValidatePreflight(request domain.UpgradePreflightRequest) error {
	if !domain.ValidOperation(request.InstanceID) || !domain.ValidOperation(request.TargetID) ||
		!domain.ValidOperation(request.Native.ID) || request.ExpectedRevision != 0 ||
		(request.Role != "foundation" && request.Role != "agent") ||
		(request.Platform != "linux-amd64" && request.Platform != "linux-arm64" && request.Platform != "darwin-arm64") ||
		(request.Role == "foundation" && request.Platform == "darwin-arm64") ||
		request.Native.Permit != nil ||
		(request.Native.Action != "inspect" && request.Native.Action != "challenge" && request.Native.Action != "status") {
		return domain.Fail(domain.Invalid, "upgrade preflight requires an exact instance, target, platform and read-only inspect/challenge/status action without an install permit")
	}
	return nil
}

func ValidatePreflightResult(record domain.UpgradePreflight, reply domain.UpgradeNativeReply, now int64) error {
	if reply.Lease != nil {
		return domain.Fail(domain.Invalid, "native preflight cannot return installation or start authority")
	}
	if reply.Error != nil {
		if reply.State != "failed" || reply.Challenge != nil || reply.Version != "" ||
			reply.Commit != "" || reply.BackupSHA256 != "" || reply.ResumeSafe {
			return domain.Fail(domain.Invalid, "failed native preflight must remain an explicit failed observation")
		}
		return nil
	}
	request := record.Request
	if reply.Platform != request.Platform {
		return domain.Fail(domain.Conflict, "native preflight platform does not match the exact target")
	}
	if request.Native.Action == "status" {
		switch reply.State {
		case "verified":
			if !sourceCommit.MatchString(reply.Commit) || !domain.ValidHash(reply.BackupSHA256) {
				return domain.Fail(domain.Invalid, "verified native status requires actual source and encrypted-backup evidence")
			}
			_, err := CompareVersion(reply.Version, reply.Version)
			return err
		case "prepared":
			if !domain.ValidHash(reply.BackupSHA256) {
				return domain.Fail(domain.Invalid, "prepared native status requires its retained encrypted-backup evidence")
			}
			return nil
		case "stopping", "installing", "operator-required":
			return nil
		default:
			return domain.Fail(domain.Invalid, "native status did not return an observed upgrade phase")
		}
	}
	challenge := reply.Challenge
	if challenge == nil || challenge.InstanceID != request.InstanceID || challenge.TargetID != request.TargetID ||
		challenge.Role != request.Role || challenge.Generation != record.TargetGeneration ||
		!sourceCommit.MatchString(challenge.CurrentCommit) || !domain.ValidHash(challenge.PolicySHA256) {
		return domain.Fail(domain.Denied, "native preflight belongs to another identity, policy or source")
	}
	if _, err := CompareVersion(challenge.CurrentVersion, challenge.CurrentVersion); err != nil {
		return err
	}
	if request.Native.Action == "inspect" {
		if reply.State != "observed" || reply.Version != challenge.CurrentVersion || reply.Commit != challenge.CurrentCommit ||
			challenge.ID != "" || challenge.Nonce != "" || challenge.ExpiresAt != 0 {
			return domain.Fail(domain.Invalid, "native inspection must report actual source without a fabricated challenge")
		}
	} else if reply.State != "planned" || challenge.ID != request.Native.ID || len(challenge.Nonce) != 43 ||
		challenge.ExpiresAt <= now || challenge.ExpiresAt > now+domain.UpgradePreflightTTL {
		return domain.Fail(domain.Conflict, "native challenge is changed, expired or outside its fixed lifetime")
	}
	return nil
}
