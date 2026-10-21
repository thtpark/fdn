package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

func (a *app) linkProviders() (provider.ShellCommands, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return provider.ShellCommands{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return provider.ShellCommands{}, err
	}
	if filepath.Base(executable) == "agent-service" {
		executable = filepath.Join(filepath.Dir(executable), "agent-cli")
	}
	configFile, err := filepath.Abs(a.configFile)
	if err != nil {
		return provider.ShellCommands{}, err
	}
	return provider.InstallShellCommands(home, configFile, executable, os.Getenv("SHELL"))
}

func terminalAdmission(ctx context.Context, r *resources) (*secure.Lock, error) {
	lock, err := secure.Acquire(filepath.Join(r.config.StateDir, "work.lock"))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*secure.Lock, error) { return nil, errors.Join(err, lock.Close()) }
	journals, err := r.state.Journals(ctx, true)
	if err != nil {
		return fail(err)
	}
	if len(journals) != 0 {
		return fail(domain.Fail(domain.Prerequisite, "reconcile interrupted invocations before an interactive provider launch"))
	}
	for _, key := range []string{"maintenance", "session-fence"} {
		var fence struct {
			Active bool `json:"active"`
		}
		if _, err := r.state.Get(ctx, key, &fence); err != nil {
			return fail(err)
		}
		if fence.Active {
			return fail(domain.Fail(domain.Prerequisite, "current maintenance/session fence blocks an interactive provider launch"))
		}
	}
	return lock, nil
}

func (a *app) executeProvider(ctx context.Context, r *resources, args []string) (value any, err error) {
	flags := flag.NewFlagSet("provider exec", flag.ContinueOnError)
	flags.SetOutput(a.diagnostic)
	name := flags.String("provider", r.config.Provider, "codex or copilot")
	if err := flags.Parse(args); err != nil {
		return nil, domain.Fail(domain.Invalid, "provider exec requires --provider NAME -- NATIVE_ARGUMENTS")
	}
	if a.json {
		return nil, domain.Fail(domain.Invalid, "provider exec preserves native terminal output; use provider status for structured checks")
	}
	arguments := flags.Args()
	nativeArguments, err := provider.NativeArguments(*name, arguments)
	if err != nil {
		return nil, err
	}
	path, err := provider.New(r.config).Resolve(*name)
	if err != nil {
		return nil, err
	}
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	spec := process.Spec{Path: path, Args: nativeArguments, Dir: directory, Env: process.Environment(), Stdin: a.in, Stdout: a.out, Stderr: a.diagnostic}
	probe := len(arguments) == 1 && slices.Contains([]string{"--version", "--help", "-h", "help"}, arguments[0])
	if !probe {
		input, output, diagnostic, terminalErr := a.humanTerminal()
		if terminalErr != nil {
			return nil, terminalErr
		}
		lock, admissionErr := terminalAdmission(ctx, r)
		if admissionErr != nil {
			return nil, admissionErr
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
		spec.Stdin, spec.Stdout, spec.Stderr, spec.Terminal = input, output, diagnostic, input
	}
	a.nativeOutput = true
	return nil, (process.Native{}).Run(ctx, spec)
}
