package cli

import (
	"context"
	"flag"
	"os"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) guide(_ context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "guide requires list, show, or install")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return guidance.Catalog()
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		catalog, err := guidance.Catalog()
		if err != nil {
			return nil, err
		}
		if !guidance.Contains(catalog, args[1]) {
			return nil, domain.Fail(domain.NotFound, "guide file is not in the embedded catalog")
		}
		data, err := guidance.Read(args[1])
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": args[1], "version": catalog.Version, "commit": catalog.Commit, "content_sha256": catalog.ContentSHA256, "content": string(data)}, nil
	case "install":
		flags := flag.NewFlagSet("guide install", flag.ContinueOnError)
		home := flags.String("home", "", "actual account HOME for guide and skill discovery")
		if err := parse(flags, args[1:]); err != nil {
			return nil, err
		}
		if *home == "" {
			var err error
			*home, err = os.UserHomeDir()
			if err != nil {
				return nil, err
			}
		}
		return guidance.Install(*home)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown guide command")
	}
}
