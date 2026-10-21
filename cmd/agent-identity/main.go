package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/identityservice"
	"github.com/thtpark/fdn/internal/secure"
)

const help = `agent-identity service plan --input OPTIONS
agent-identity service install --input OPTIONS --apply
agent-identity service status --input OPTIONS
agent-identity --config PRIVATE_CONFIG prepare --name NAME --output PUBLIC_FILE
agent-identity --config PRIVATE_CONFIG connect --profile APPROVED_PUBLIC_PROFILE
agent-identity --config PRIVATE_CONFIG serve

Run as the dedicated, non-root identity account, never the tool-runner UID.
prepare/connect are offline operations: stop this identity service first.
prepare retains one device and exports public material only; exact retries reuse it.
connect requires an integrity-protected public profile with pinned CA/known_hosts
and this device's provisioned SSH account. No bearer credentials are accepted.
serve exposes a peer-checked Unix worker proxy, never private keys or control APIs.
service plan is read-only; install/status require explicit native administrator execution.
Install never creates/adopts accounts: provision the exact plan's UID/GID/HOME,
managed comment/RealName markers, no-login identity shell and one-way worker group
membership first. It registers only the named identity daemon and runner service.
Recovery: stop the service; preserve its old private StateDir and all runner state.
Use a new empty 0700 StateDir inside the same identity HOME, keep the stable private
config file/IPC endpoint, prepare a fresh public request, obtain explicit recovery,
then connect its new approved profile and restart. Never delete runner journals,
reuse old keys, or rotate automatically during normal reconnect/restart.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

func result(writer io.Writer, value any, err error) int {
	envelope := api.Envelope{OK: err == nil, Warnings: []string{}}
	if err != nil {
		envelope.Error = domain.AsFault(err)
	} else {
		envelope.Data, err = json.Marshal(value)
		if err != nil {
			envelope.OK, envelope.Error = false, domain.AsFault(err)
		}
	}
	if encodeErr := json.NewEncoder(writer).Encode(envelope); encodeErr != nil {
		return domain.ExitCode(encodeErr)
	}
	return domain.ExitCode(err)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithServiceFactory(ctx, args, stdout, stderr, nativeServiceFactory)
}

func runWithServiceFactory(ctx context.Context, args []string, stdout, stderr io.Writer, factory serviceFactory) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help" || args[0] == "-h")) {
		_, _ = io.WriteString(stdout, help)
		return 0
	}
	global := flag.NewFlagSet("agent-identity", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	configFile := global.String("config", "", "identity-owned private configuration")
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		return result(stdout, nil, domain.Fail(domain.Invalid, "use agent-identity service plan|install|status or --config PRIVATE_CONFIG prepare|connect|serve; see --help"))
	}
	command, rest := global.Arg(0), global.Args()[1:]
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		_, _ = io.WriteString(stdout, help)
		return 0
	}
	if command == "service" {
		if *configFile != "" {
			return result(stdout, nil, domain.Fail(domain.Invalid, "service lifecycle takes --input OPTIONS, not the service-private --config"))
		}
		return runService(ctx, rest, stdout, factory)
	}
	if *configFile == "" {
		return result(stdout, nil, domain.Fail(domain.Invalid, "private prepare/connect/serve require --config PRIVATE_CONFIG"))
	}
	var c config.IdentityService
	if err := config.Read(*configFile, &c); err != nil {
		return result(stdout, nil, err)
	}
	if err := identityservice.CheckNativeAccount(c); err != nil {
		return result(stdout, nil, err)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	switch command {
	case "prepare":
		name := flags.String("name", "", "public device name")
		output := flags.String("output", "", "public-only registration file")
		if flags.Parse(rest) != nil || flags.NArg() != 0 || *name == "" || *output == "" {
			return result(stdout, nil, domain.Fail(domain.Invalid, "prepare requires --name NAME --output PUBLIC_FILE"))
		}
		path, err := filepath.Abs(*output)
		if err != nil {
			return result(stdout, nil, err)
		}
		public, err := identityservice.Prepare(c.StateDir, *name, path, identityservice.Options{})
		return result(stdout, public, err)
	case "connect":
		profileFile := flags.String("profile", "", "integrity-approved public connection profile")
		if flags.Parse(rest) != nil || flags.NArg() != 0 || *profileFile == "" {
			return result(stdout, nil, domain.Fail(domain.Invalid, "connect requires --profile APPROVED_PUBLIC_PROFILE"))
		}
		data, err := secure.ReadPublic(*profileFile, domain.MaxBody)
		if err != nil {
			return result(stdout, nil, err)
		}
		var profile config.ConnectionProfile
		if err := config.Decode(data, &profile); err != nil {
			return result(stdout, nil, err)
		}
		status, err := identityservice.Connect(ctx, c, profile, identityservice.Options{})
		return result(stdout, status, err)
	case "serve":
		if flags.Parse(rest) != nil || flags.NArg() != 0 {
			return result(stdout, nil, domain.Fail(domain.Invalid, "serve does not accept positional arguments"))
		}
		service, err := identityservice.New(c, identityservice.Options{})
		if err == nil {
			err = service.Serve(ctx)
		}
		return result(stdout, map[string]bool{"stopped": err == nil}, err)
	default:
		_, _ = fmt.Fprintln(stderr, "See agent-identity --help for implemented identity-account commands.")
		return result(stdout, nil, domain.Fail(domain.Invalid, "unknown identity service command"))
	}
}
