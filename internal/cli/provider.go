package cli

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"golang.org/x/term"
)

func (a *app) humanTerminal() (*os.File, *os.File, *os.File, error) {
	input, inOK := a.in.(*os.File)
	output, outOK := a.out.(*os.File)
	diagnostic, errOK := a.diagnostic.(*os.File)
	if a.json || a.nonInteractive || !inOK || !outOK || !errOK || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) || !term.IsTerminal(int(diagnostic.Fd())) {
		return nil, nil, nil, domain.Fail(domain.Prerequisite, "protected authentication/remote UI requires an explicitly invoked human terminal, without --json or --non-interactive; never send login secrets to chat")
	}
	return input, output, diagnostic, nil
}

func (a *app) providerCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "provider command required")
	}
	if args[0] == "exec" {
		return a.executeProvider(ctx, r, args[1:])
	}
	if args[0] == "link" {
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return a.linkProviders()
	}
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	name := fs.String("provider", r.config.Provider, "codex or copilot")
	version := fs.String("version", "", "exact version for installation")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	manager := provider.New(r.config)
	switch args[0] {
	case "status":
		return manager.Check(ctx, *name)
	case "install", "update":
		selected := *version
		if args[0] == "update" {
			if selected != "" {
				return nil, domain.Fail(domain.Invalid, "update resolves the latest version; use install to pin one")
			}
			var err error
			selected, err = provider.Latest(ctx, *name, &http.Client{Timeout: 15 * time.Second})
			if err != nil {
				return nil, err
			}
		}
		if selected == "" {
			return nil, domain.Fail(domain.Invalid, "install requires --version; update explicitly selects latest")
		}
		installed, err := manager.Install(ctx, *name, selected)
		if err != nil {
			return nil, err
		}
		_, err = a.linkProviders()
		return installed, err
	case "login":
		input, output, diagnostic, err := a.humanTerminal()
		if err != nil {
			return nil, err
		}
		slot, err := terminalAdmission(ctx, r)
		if err != nil {
			return nil, err
		}
		defer slot.Close()
		path, err := manager.Resolve(*name)
		if err != nil {
			return nil, err
		}
		err = (process.Native{}).Run(ctx, process.Spec{Path: path, Args: []string{"login"}, Dir: r.config.StateDir, Env: process.Environment(), Stdin: input, Stdout: output, Stderr: diagnostic, Terminal: input})
		return map[string]bool{"login_command_finished": err == nil, "authentication_validated": false}, err
	default:
		return nil, domain.Fail(domain.Invalid, "unknown provider command")
	}
}
