package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) restartHelper(ctx context.Context, args []string) (any, error) {
	a.nativeOutput = true
	flags := flag.NewFlagSet("restart-helper", flag.ContinueOnError)
	path := flags.String("policy", "", "administrator-provisioned fixed worker restart policy")
	if err := parse(flags, args); err != nil {
		return nil, err
	}
	if *path == "" {
		return nil, domain.Fail(domain.Invalid, "restart-helper requires an explicit private native policy")
	}
	policy, err := upgrade.ReadPolicy(*path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native restart request exceeds its bounded input")
	}
	var request domain.AgentRestartNativeRequest
	if err := message.UnmarshalPayload(data, &request); err != nil {
		return nil, err
	}
	reply, actionErr := upgrade.NewNative(policy).RestartHandle(ctx, request)
	if actionErr != nil {
		reply.Error = domain.AsFault(domain.Fail(domain.AsFault(actionErr).Code, "Native service restart failed; inspect the exact retained operation"))
	}
	output, err := message.MarshalPayload(reply)
	if err != nil {
		return nil, err
	}
	_, writeErr := a.out.Write(output)
	return nil, errors.Join(actionErr, writeErr)
}
