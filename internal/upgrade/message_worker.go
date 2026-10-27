package upgrade

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
)

func (n *Native) maintenanceMessage(ctx context.Context, operation string, input any, output any) error {
	var server config.Server
	if err := n.readConfig(n.Policy.ConfigFile, &server); err != nil {
		return err
	}
	uid, _, err := n.Policy.account()
	if err != nil {
		return err
	}
	path := filepath.Join(server.StateDir, "maintenance.sock")
	if err := protected(filepath.Dir(path), uint32(uid)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(uid) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return domain.Fail(domain.Denied, "Foundation request inbox socket ownership changed")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	peer, ok := ingress.UID(ingress.ConnContext(ctx, conn))
	if !ok || peer != uint32(uid) {
		return domain.Fail(domain.Denied, "Foundation request handler is not the pinned service OS peer")
	}
	session, err := message.NewSession(ctx, conn, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	var payload []byte
	if input != nil {
		payload, err = message.MarshalPayload(input)
		if err != nil {
			return err
		}
	}
	data, err := session.Call(ctx, message.Request{Operation: operation, Payload: payload})
	if err != nil {
		return err
	}
	return message.UnmarshalPayload(data, output)
}

func (n *Native) messageMailbox(ctx context.Context, request domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
	reply := domain.MessageWorkerReply{Messages: []domain.Message{}}
	if n.Policy.Role == "foundation" {
		if request.Report == nil {
			err := n.maintenanceMessage(ctx, "query.maintenance.requests", nil, &reply.Messages)
			return reply, err
		}
		var updated domain.Message
		err := n.maintenanceMessage(ctx, "command.maintenance.request-result", request, &updated)
		reply.Message = &updated
		return reply, err
	}
	data, err := message.MarshalPayload(request)
	if err != nil {
		return reply, err
	}
	output := process.NewBuffer(domain.MaxBody)
	runErr := n.commandInput(ctx, filepath.Join(n.Policy.BinaryDir, "agent-cli"),
		[]string{"--config", n.Policy.ConfigFile, "message-worker"}, true, output, bytes.NewReader(data))
	raw, err := output.Bytes()
	if err != nil {
		return reply, err
	}
	if len(raw) == 0 {
		return reply, errors.Join(domain.Fail(domain.Unavailable, "target maintenance inbox response is unavailable"), runErr)
	}
	if err := message.UnmarshalPayload(raw, &reply); err != nil {
		return reply, err
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	return reply, runErr
}

type nativeMessageProgress struct {
	domain.Message
	PendingReport *domain.MessageActionReport `json:"pending_report,omitempty"`
}

func (n *Native) reportNativeMessage(ctx context.Context, progress *nativeMessageProgress, state, reason string) error {
	progress.PendingReport = &domain.MessageActionReport{Command: domain.Command{
		OperationID: "native-request." + progress.ID + "." + state, ExpectedRevision: progress.Revision}, State: state, Reason: reason}
	if err := config.Write(filepath.Join(n.Policy.StateDir, "message-inflight.json"), progress); err != nil {
		return err
	}
	return n.deliverNativeMessage(ctx, progress)
}

func (n *Native) deliverNativeMessage(ctx context.Context, progress *nativeMessageProgress) error {
	report := progress.PendingReport
	result, err := n.Mailbox(ctx, domain.MessageWorkerCommand{MessageID: progress.ID, Report: report})
	if err != nil {
		if report.State == "accepted" {
			switch domain.AsFault(err).Code {
			case domain.Denied:
				return n.reportNativeMessage(ctx, progress, "declined", "The request no longer has current issuer, target, policy or unexpired maintenance authority.")
			case domain.Prerequisite:
				return n.reportNativeMessage(ctx, progress, "deferred", "An earlier rollout request has not completed successfully. This target will not execute; resolve the prerequisite and submit a new exact authorized request.")
			}
		} else if (report.State == "executing" || report.State == "completed") && domain.AsFault(err).Code == domain.Denied {
			return n.reportNativeMessage(ctx, progress, "failed", "Current request authority changed before this progress/result could be acknowledged. Inspect retained native evidence; no installation or retry is being repeated.")
		}
		return err
	}
	updated := result.Message
	if updated == nil || updated.ID != progress.ID || updated.RecipientID != progress.RecipientID ||
		updated.Revision != report.ExpectedRevision+1 || updated.Request == nil || updated.Request.State != report.State ||
		!reflect.DeepEqual(updated.Request.Action, progress.Request.Action) {
		return domain.Fail(domain.Unavailable, "native request decision/result acknowledgement is missing or changed; retain and reconcile the exact pending report")
	}
	progress.Message, progress.PendingReport = *updated, nil
	path := filepath.Join(n.Policy.StateDir, "message-inflight.json")
	switch report.State {
	case "completed", "failed", "declined", "deferred":
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		return config.Write(path, progress)
	}
}

// MessageStep runs in the separately provisioned native maintenance service,
// never as a child of the identity/runner units it must stop.
func (n *Native) MessageStep(ctx context.Context) (bool, error) {
	if n.Mailbox == nil {
		return false, domain.Fail(domain.Prerequisite, "native request inbox transport is not configured")
	}
	var progress nativeMessageProgress
	resuming := true
	if err := config.Read(filepath.Join(n.Policy.StateDir, "message-inflight.json"), &progress); os.IsNotExist(err) {
		resuming = false
		inbox, err := n.Mailbox(ctx, domain.MessageWorkerCommand{})
		if err != nil {
			return false, err
		}
		for _, request := range inbox.Messages {
			if request.Request != nil && request.Request.Action.Kind == "upgrade" {
				progress.Message = request
				break
			}
		}
		if progress.ID == "" {
			return false, nil
		}
	} else if err != nil {
		return false, err
	}
	if progress.Request == nil || progress.Request.Action.Kind != "upgrade" || progress.Request.Action.Upgrade == nil {
		return false, domain.Fail(domain.Invalid, "retained maintenance request has no exact native upgrade descriptor")
	}
	permit := progress.Request.Action.Upgrade
	if permit.Proposal.Challenge.TargetID != n.Policy.TargetID || progress.RecipientID != n.Policy.TargetID {
		return false, domain.Fail(domain.Denied, "maintenance request is addressed to another fixed native policy")
	}
	if progress.PendingReport != nil {
		if err := n.deliverNativeMessage(ctx, &progress); err != nil {
			return true, err
		}
	}
	report := func(state, reason string) error { return n.reportNativeMessage(ctx, &progress, state, reason) }
	switch progress.Request.State {
	case "queued":
		if resuming {
			return true, domain.Fail(domain.Conflict, "retained maintenance decision has no replay proposal; an operator must reconcile it without native execution")
		}
		if err := report("accepted", "The target accepted this exact authenticated native maintenance request, subject to current safety and OS policy checks."); err != nil {
			return true, err
		}
		if progress.Request.State != "accepted" {
			return true, nil
		}
	case "accepted":
		return true, report("failed", "Maintenance was interrupted before a durable execution admission; review the original request rather than automatically retrying it.")
	case "executing":
		status, err := n.Handle(ctx, domain.UpgradeNativeRequest{Action: "status", ID: permit.Proposal.Challenge.ID})
		if err == nil && status.State == "verified" {
			return true, report("completed", "The target reconciled the exact verified native upgrade receipt; no installation was repeated.")
		}
		failure := report("failed", "Native maintenance was interrupted or has no verifiable native receipt. Inspect retained evidence; no automatic retry or completion is authorized.")
		return true, errors.Join(err, failure)
	case "completed", "failed", "declined", "deferred":
		return true, nil
	default:
		return true, domain.Fail(domain.Invalid, "native maintenance request has an unsupported lifecycle state")
	}
	if err := report("executing", "The target is beginning its admitted maintenance action; work remains paused and completion will be reported separately."); err != nil {
		return true, err
	}
	prepared, err := n.Handle(ctx, domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: permit})
	if err != nil || prepared.State != "prepared" {
		failure := report("failed", "The target could not safely prepare the exact native upgrade. Inspect native evidence; identity, work and recovery fences were not overridden.")
		return true, errors.Join(err, failure)
	}
	completed, err := n.Handle(ctx, domain.UpgradeNativeRequest{Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: permit})
	if err != nil || completed.State != "verified" {
		failure := report("failed", "Native installation did not reach a verified receipt. Inspect retained evidence; do not repeat installation or infer success.")
		return true, errors.Join(err, failure)
	}
	return true, report("completed", "The target completed and verified the exact requested software upgrade. Automation remains paused.")
}
