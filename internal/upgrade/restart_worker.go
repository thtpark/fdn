package upgrade

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
)

func callRestart(ctx context.Context, runner process.Runner, target Target, request domain.AgentRestartNativeRequest) (domain.AgentRestartNativeReply, error) {
	var reply domain.AgentRestartNativeReply
	args, err := operatorArguments(target, "restart-helper")
	if err != nil {
		return reply, err
	}
	if target.Role != "agent" {
		return reply, domain.Fail(domain.Denied, "native restart routes target worker services only")
	}
	raw, err := message.MarshalPayload(request)
	if err != nil {
		return reply, err
	}
	output, diagnostic := process.NewBuffer(domain.MaxBody), process.NewBuffer(8192)
	runErr := runner.Run(ctx, process.Spec{Path: "ssh", Args: args, Stdin: bytes.NewReader(raw),
		Env: process.Environment(), Stdout: output, Stderr: diagnostic})
	data, err := output.Bytes()
	if err != nil {
		return reply, err
	}
	if len(data) == 0 {
		return reply, errors.Join(runErr, domain.Fail(domain.Unavailable, "native restart response is uncertain; inspect the same operation"))
	}
	if err := message.UnmarshalPayload(data, &reply); err != nil {
		return reply, err
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	return reply, runErr
}

type nativeRestartProgress struct {
	Permit domain.AgentRestartPermit  `json:"permit"`
	Target *Target                    `json:"target,omitempty"`
	Phase  string                     `json:"phase"`
	Lease  *domain.UpgradeLease       `json:"lease,omitempty"`
	Report *domain.AgentRestartReport `json:"report,omitempty"`
}

func (n *Native) RestartStep(ctx context.Context) (bool, error) {
	if n.Policy.Role != "foundation" {
		return false, nil
	}
	path := filepath.Join(n.Policy.StateDir, "restart-inflight.json")
	var progress nativeRestartProgress
	if err := config.Read(path, &progress); os.IsNotExist(err) {
		inbox, err := n.Mailbox(ctx, domain.MessageWorkerCommand{Restarts: true})
		if err != nil {
			return false, err
		}
		if len(inbox.Restarts) == 0 {
			return false, nil
		}
		progress.Permit, progress.Phase = inbox.Restarts[0], "queued"
		for _, target := range n.Policy.RestartTargets {
			if target.TargetID == progress.Permit.TargetID {
				copy := target
				progress.Target = &copy
				break
			}
		}
	} else if err != nil {
		return false, err
	}
	if err := ValidateRestartPermit(progress.Permit); err != nil {
		return true, err
	}
	if progress.Permit.InstanceID != n.Policy.InstanceID {
		return true, domain.Fail(domain.Denied, "native restart belongs to another Foundation")
	}
	saveReport := func(state, code string, runtime *domain.NativeRuntimeObservation) error {
		revision := int64(2)
		if progress.Phase == "queued" {
			revision = 1
		}
		progress.Report = &domain.AgentRestartReport{Command: domain.Command{
			OperationID: "native-restart." + progress.Permit.ID + "." + state, ExpectedRevision: revision},
			ID: progress.Permit.ID, State: state}
		if state != "checking" {
			progress.Report.Result = &domain.AgentOperationResult{Code: code, ModeAtCheck: "manual", NativeRuntime: runtime}
		}
		return config.Write(path, progress)
	}
	if progress.Report == nil {
		switch {
		case progress.Target == nil:
			if err := saveReport("rejected", "native-restart-unavailable", nil); err != nil {
				return true, err
			}
		case progress.Permit.ExpiresAt <= n.Now().Unix():
			if err := saveReport("expired", "operation-expired", nil); err != nil {
				return true, err
			}
		case progress.Phase == "queued":
			if err := saveReport("checking", "", nil); err != nil {
				return true, err
			}
		default:
			found := false
			for _, target := range n.Policy.RestartTargets {
				found = found || reflect.DeepEqual(target, *progress.Target)
			}
			if !found {
				return true, domain.Fail(domain.Conflict, "the exact native restart route changed; no new route was adopted")
			}
			action := "status"
			if progress.Phase == "checking" {
				reply, err := callRestart(ctx, n.Runner, *progress.Target, domain.AgentRestartNativeRequest{Action: "inspect", Permit: progress.Permit})
				if err != nil {
					if err := saveReport("rejected", "native-restart-unavailable", nil); err != nil {
						return true, err
					}
					break
				}
				c := reply.Challenge
				if reply.State != "planned" || c == nil || c.ID != progress.Permit.ID || c.TargetID != progress.Permit.TargetID ||
					c.InstanceID != progress.Permit.InstanceID || c.Generation != progress.Permit.TargetGeneration ||
					c.Role != "agent" || !sourceCommit.MatchString(c.CurrentCommit) || !domain.ValidHash(c.PolicySHA256) || len(c.Nonce) != 43 {
					return true, domain.Fail(domain.Denied, "native restart inspection did not prove the exact fixed target")
				}
				replyMailbox, err := n.Mailbox(ctx, domain.MessageWorkerCommand{RestartLease: &domain.AgentRestartLeaseRequest{
					Command: domain.Command{OperationID: "native-restart." + progress.Permit.ID + ".lease"},
					ID:      progress.Permit.ID, Nonce: c.Nonce, PermitSHA256: domain.Hash(progress.Permit.Signature)}})
				if err != nil {
					return true, err
				}
				if replyMailbox.RestartLease == nil {
					return true, domain.Fail(domain.Unavailable, "native restart lease was not returned")
				}
				progress.Lease, progress.Phase = replyMailbox.RestartLease, "dispatched"
				if err := config.Write(path, progress); err != nil {
					return true, err
				}
				action = "apply"
			}
			command := domain.AgentRestartNativeRequest{Action: action, Permit: progress.Permit}
			if action == "apply" {
				command.Lease = progress.Lease
			}
			reply, err := callRestart(ctx, n.Runner, *progress.Target, command)
			if err != nil && action == "apply" {
				return true, err
			}
			if err != nil || reply.State != "verified" || reply.Runtime == nil ||
				reply.Runtime.IdentityGeneration != progress.Permit.TargetGeneration || !reply.Runtime.ServicesRunning {
				if err := saveReport("operator-required", "native-restart-interrupted", nil); err != nil {
					return true, err
				}
			} else if err := saveReport("restarted", "native-services-restarted", reply.Runtime); err != nil {
				return true, err
			}
		}
	}
	reply, err := n.Mailbox(ctx, domain.MessageWorkerCommand{RestartReport: progress.Report})
	if err != nil && domain.AsFault(err).Code == domain.Denied && progress.Report.State == "checking" {
		progress.Report.State = "rejected"
		progress.Report.OperationID = "native-restart." + progress.Permit.ID + ".admission-rejected"
		progress.Report.Result = &domain.AgentOperationResult{Code: "authority-changed", ModeAtCheck: "manual"}
		if saveErr := config.Write(path, progress); saveErr != nil {
			return true, errors.Join(err, saveErr)
		}
		return true, err
	}
	if err != nil && domain.AsFault(err).Code == domain.Denied && progress.Report.State == "restarted" {
		progress.Report.State = "operator-required"
		progress.Report.OperationID = "native-restart." + progress.Permit.ID + ".authority-changed"
		progress.Report.Result.Code = "native-restart-interrupted"
		if saveErr := config.Write(path, progress); saveErr != nil {
			return true, errors.Join(err, saveErr)
		}
		return true, err
	}
	if err != nil {
		return true, err
	}
	if reply.Restart == nil || reply.Restart.ID != progress.Permit.ID || reply.Restart.Kind != "native-restart" ||
		reply.Restart.State != progress.Report.State || reply.Restart.Revision != progress.Report.ExpectedRevision+1 ||
		reply.Restart.TargetID != progress.Permit.TargetID || reply.Restart.RequesterID != progress.Permit.RequesterID ||
		reply.Restart.CreatedAt != progress.Permit.CreatedAt || reply.Restart.ExpiresAt != progress.Permit.ExpiresAt {
		return true, domain.Fail(domain.Unavailable, "native restart acknowledgement changed; preserve the exact report")
	}
	if progress.Report.State == "checking" {
		progress.Report, progress.Phase = nil, "checking"
		return true, config.Write(path, progress)
	}
	return true, os.Remove(path)
}
