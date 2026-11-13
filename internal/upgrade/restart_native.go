package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type nativeRestartJob struct {
	Permit    domain.AgentRestartPermit        `json:"permit"`
	Challenge domain.UpgradeChallenge          `json:"challenge"`
	State     string                           `json:"state"`
	Runtime   *domain.NativeRuntimeObservation `json:"runtime,omitempty"`
}

func (n *Native) restartGeneration() (int64, error) {
	var identity config.IdentityService
	if err := n.readConfig(n.Policy.IdentityConfig, &identity); err != nil {
		return 0, err
	}
	if !inside(n.Policy.Home, identity.StateDir) {
		return 0, domain.Fail(domain.Denied, "native restart identity state is outside the fixed installing account")
	}
	var profile config.ConnectionProfile
	if err := n.readConfig(filepath.Join(identity.StateDir, "connection.json"), &profile); err != nil {
		return 0, err
	}
	if profile.Identity.AgentID != n.Policy.TargetID || profile.Identity.InstanceID != n.Policy.InstanceID ||
		profile.Identity.State != "active" || profile.Identity.Generation <= 0 {
		return 0, domain.Fail(domain.Denied, "native restart belongs to another retained computer identity")
	}
	return profile.Identity.Generation, nil
}

func (n *Native) RestartHandle(ctx context.Context, request domain.AgentRestartNativeRequest) (domain.AgentRestartNativeReply, error) {
	var reply domain.AgentRestartNativeReply
	if n.Policy.Role != "agent" || !n.Policy.AllowNativeRestart {
		return reply, domain.Fail(domain.Denied, "native service restart is not administrator-enabled for this fixed worker")
	}
	if err := n.Policy.Validate(); err != nil {
		return reply, err
	}
	if request.Action != "inspect" && request.Action != "apply" && request.Action != "status" ||
		request.Action != "apply" && request.Lease != nil {
		return reply, domain.Fail(domain.Invalid, "native restart helper accepts only exact inspect/apply/status controls")
	}
	permit := request.Permit
	ca, err := secure.ReadPublic(n.Policy.CAFile, domain.MaxBody)
	if err != nil {
		return reply, err
	}
	if request.Action == "status" {
		if err := ValidateRestartPermit(permit); err != nil {
			return reply, err
		}
		data, err := restartPermitBytes(permit)
		if err != nil {
			return reply, err
		}
		if err := verifyAuthorization(data, permit.Signature, ca, n.Now()); err != nil {
			return reply, err
		}
	} else if err := VerifyRestartPermit(permit, ca, n.Now()); err != nil {
		return reply, err
	}
	if permit.TargetID != n.Policy.TargetID || permit.InstanceID != n.Policy.InstanceID {
		return reply, domain.Fail(domain.Denied, "native restart permit does not match the fixed target policy")
	}
	generation, err := n.restartGeneration()
	if err != nil {
		return reply, err
	}
	if generation != permit.TargetGeneration {
		return reply, domain.Fail(domain.Denied, "native restart computer generation changed")
	}
	if err := secure.Dir(n.Policy.StateDir); err != nil {
		return reply, err
	}
	path := filepath.Join(n.Policy.StateDir, "restart-"+permit.ID+".json")
	var job nativeRestartJob
	readErr := config.Read(path, &job)
	if readErr != nil && !os.IsNotExist(readErr) {
		return reply, readErr
	}
	if readErr == nil && (!reflect.DeepEqual(job.Permit, permit) || job.Challenge.PolicySHA256 != n.policyHash()) {
		return reply, domain.Fail(domain.Conflict, "native restart request or pinned policy changed; retain its original receipt")
	}
	if request.Action == "status" {
		if readErr != nil {
			return reply, readErr
		}
		if job.State == "verified" {
			return domain.AgentRestartNativeReply{State: "verified", Runtime: job.Runtime}, nil
		}
		return domain.AgentRestartNativeReply{State: "operator-required"}, nil
	}
	lock, err := secure.Acquire(filepath.Join(n.Policy.StateDir, "upgrade.lock"))
	if err != nil {
		return reply, err
	}
	defer lock.Close()
	if request.Action == "inspect" {
		if readErr == nil {
			if job.State != "planned" {
				return domain.AgentRestartNativeReply{State: job.State, Runtime: job.Runtime}, nil
			}
			return domain.AgentRestartNativeReply{State: "planned", Challenge: &job.Challenge}, nil
		}
		version, commit, err := n.installed(ctx)
		if err != nil {
			return reply, err
		}
		if _, err := CompareVersion(version, version); err != nil {
			return reply, err
		}
		if !sourceCommit.MatchString(commit) {
			return reply, domain.Fail(domain.Invalid, "native restart requires an observed released source")
		}
		job = nativeRestartJob{Permit: permit, State: "planned", Challenge: domain.UpgradeChallenge{
			ID: permit.ID, Nonce: domain.NewToken(), InstanceID: permit.InstanceID, TargetID: permit.TargetID,
			Role: "agent", Generation: generation, CurrentVersion: version, CurrentCommit: commit,
			PolicySHA256: n.policyHash(), ExpiresAt: permit.ExpiresAt}}
		if err := config.Write(path, job); err != nil {
			return reply, err
		}
		return domain.AgentRestartNativeReply{State: "planned", Challenge: &job.Challenge}, nil
	}
	if readErr != nil {
		return reply, readErr
	}
	if job.State == "verified" {
		return domain.AgentRestartNativeReply{State: "verified", Runtime: job.Runtime}, nil
	}
	if job.State != "planned" || request.Lease == nil {
		return reply, domain.Fail(domain.Prerequisite, "native restart cannot repeat uncertain service effects")
	}
	if err := VerifyRestartLease(*request.Lease, permit, job.Challenge.Nonce, ca, n.Now()); err != nil {
		return reply, err
	}
	version, commit, err := n.installed(ctx)
	if err != nil {
		return reply, err
	}
	if version != job.Challenge.CurrentVersion || commit != job.Challenge.CurrentCommit {
		return reply, domain.Fail(domain.Conflict, "native restart source changed after exact inspection")
	}
	action, cancel := context.WithTimeout(ctx, min(time.Minute, time.Until(time.Unix(permit.ExpiresAt, 0))))
	defer cancel()
	job.State = "restarting"
	if err := config.Write(path, job); err != nil {
		return reply, err
	}
	if err := n.services(action, "stop", generation); err != nil {
		return reply, err
	}
	workLock, err := n.lockApplication()
	if err != nil {
		return reply, err
	}
	if err := workLock.Close(); err != nil {
		return reply, err
	}
	if err := n.services(action, "start", generation); err != nil {
		return reply, err
	}
	if err := n.CheckServices(action); err != nil {
		return reply, err
	}
	var status struct {
		Agent domain.Agent `json:"agent"`
		Local *struct {
			PendingInvocations []json.RawMessage `json:"pending_invocations"`
			SessionFence       *struct {
				Active *bool `json:"active"`
			} `json:"session_fence"`
			Work *struct {
				PendingReports *int `json:"pending_reports"`
			} `json:"work"`
		} `json:"local"`
	}
	if err := n.jsonCommand(action, filepath.Join(n.Policy.BinaryDir, "agent-cli"),
		[]string{"--config", n.Policy.ConfigFile, "status"}, true, &status); err != nil {
		return reply, err
	}
	if status.Agent.ID != permit.TargetID || !status.Agent.Active || status.Agent.Mode != "manual" {
		return reply, domain.Fail(domain.Denied, "restarted worker does not confirm the exact paused identity")
	}
	if status.Local == nil || status.Local.PendingInvocations == nil ||
		status.Local.SessionFence == nil || status.Local.SessionFence.Active == nil ||
		status.Local.Work == nil || status.Local.Work.PendingReports == nil || *status.Local.Work.PendingReports < 0 {
		return reply, domain.Fail(domain.Prerequisite, "native restart status lacks explicit pending-work and fence observations")
	}
	current, err := n.target(action)
	if err != nil || current != generation {
		return reply, errors.Join(err, domain.Fail(domain.Conflict, "restarted identity generation changed"))
	}
	job.State = "verified"
	job.Runtime = &domain.NativeRuntimeObservation{Version: version, Commit: commit, IdentityGeneration: generation,
		ServicesRunning: true, PendingInvocations: len(status.Local.PendingInvocations),
		PendingReports: *status.Local.Work.PendingReports, SessionFenced: *status.Local.SessionFence.Active}
	if err := config.Write(path, job); err != nil {
		return reply, err
	}
	return domain.AgentRestartNativeReply{State: "verified", Runtime: job.Runtime}, nil
}
