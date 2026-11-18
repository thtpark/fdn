package cli

import (
	"context"
	"flag"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) recoverSoftwareBeforeInstall(ctx context.Context, args []string) (any, error) {
	flags := flag.NewFlagSet("software-recover", flag.ContinueOnError)
	policy := flags.String("updater-policy", "", "existing protected Agent updater policy")
	input := flags.String("input", "", "exact inspected native job, permit/journal hashes and non-secret reason")
	if err := parse(flags, args); err != nil {
		return nil, err
	}
	if *policy == "" || *input == "" || !a.explicitOperation {
		return nil, domain.Fail(domain.Invalid, "software-recover requires --updater-policy, --input and an explicit original --operation-id")
	}
	var request upgrade.SoftwareRecoveryRequest
	if err := inputFile(*input, a.in, &request); err != nil {
		return nil, err
	}
	if request.OperationID != "" && request.OperationID != a.operation {
		return nil, domain.Fail(domain.Conflict, "recovery input belongs to a different explicit operation")
	}
	request.OperationID = a.operation
	receiver, channel, _, cleanup, err := a.foundationSoftwareRuntime(ctx, *policy, "agent")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	bridge, ok := receiver.(*upgrade.SoftwareBridge)
	if !ok {
		return nil, domain.Fail(domain.Denied, "pre-install recovery is not available for a generic product receiver")
	}
	recover := bridge.AbortBeforeInstall
	if request.BaselineReleaseFile != "" {
		recover = bridge.AbortBeforeManagedActivation
	}
	receipt, err := recover(ctx, request)
	if err != nil {
		return nil, err
	}
	result := domain.SoftwareResultRequest{Command: domain.Command{
		OperationID: "op_" + domain.Hash(receipt.Result.PermitID+":"+receipt.Result.PermitSHA256+":native-result"),
	}, Result: receipt.Result}
	reply, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "result", Result: &result})
	if err != nil {
		return nil, err
	}
	if reply.Deployment == nil || reply.Deployment.State != "failed" || reply.Deployment.Result == nil ||
		*reply.Deployment.Result != receipt.Result {
		return nil, domain.Fail(domain.Unavailable, "native abort is retained but its exact failed outcome was not acknowledged")
	}
	if err := bridge.AcknowledgeResult(receipt.Result.PermitID, receipt.Result.PermitSHA256); err != nil {
		return nil, err
	}
	return map[string]any{"receipt": receipt, "reported_state": reply.Deployment.State,
		"services_started": false, "mode_changed": false, "backup_or_install_repeated": false}, nil
}
