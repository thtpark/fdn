package cli

import (
	"context"
	"flag"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/display"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) screenCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "screen requires list or launch")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return display.Inventory(ctx, r.config)
	case "launch":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		flags := flag.NewFlagSet("screen launch", flag.ContinueOnError)
		application := flags.String("application", "", "configured application ID, not an executable or shell command")
		if err := parse(flags, tail); err != nil {
			return nil, err
		}
		if *application == "" {
			return nil, domain.Fail(domain.Invalid, "screen launch requires --application ID")
		}
		slot, err := terminalAdmission(ctx, r)
		if err != nil {
			return nil, err
		}
		defer slot.Close()
		err = display.RunApplication(ctx, r.config, id, *application)
		return map[string]any{"screen_id": id, "application_id": *application, "launched": err == nil}, err
	default:
		return nil, domain.Fail(domain.Invalid, "unknown screen command")
	}
}

func (a *app) publishScreens(ctx context.Context, r *resources) error {
	screens, err := display.Inventory(ctx, r.config)
	if err != nil {
		return err
	}
	_, err = a.write(ctx, r, api.Path("agents", r.client.Credential.AgentID, "screens"), domain.ScreensRequest{
		Command: domain.Command{OperationID: a.operation + ".screens"}, Screens: screens}, "")
	return err
}
