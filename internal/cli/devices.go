package cli

import (
	"context"
	"encoding/json"
	"flag"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) deviceCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "device requires list, register or revoke")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.AgentIdentity](ctx, r.client, "GET", "/v1/identity/devices", nil)
	case "register":
		fs := flag.NewFlagSet("device register", flag.ContinueOnError)
		input := fs.String("input", "", "new Founder's computer signed public identity request")
		output := fs.String("output", "", "public pinned device connection profile")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		if *input == "" {
			return nil, domain.Fail(domain.Invalid, "--input with this computer's signed public request is required")
		}
		var req domain.RegisterIdentityRequest
		if err := inputFile(*input, a.in, &req.Identity); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		raw, err := a.write(ctx, r, "/v1/identity/devices", req, "")
		if err != nil {
			return nil, err
		}
		var identity domain.AgentIdentity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, err
		}
		if err := exportIdentityProfile(ctx, r, identity, *output); err != nil {
			return nil, err
		}
		return identity, nil
	case "revoke":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.RevokeDeviceRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("identity", "devices", id, "revoke"), req, "")
	default:
		return nil, domain.Fail(domain.Invalid, "unknown device command")
	}
}
