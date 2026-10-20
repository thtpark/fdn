package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type app struct {
	in                io.Reader
	out               io.Writer
	diagnostic        io.Writer
	configFile        string
	json              bool
	nonInteractive    bool
	timeout           int
	operation         string
	explicitOperation bool
	expectedRevision  int64
}

var AgentCommands = []string{
	"config init", "config show", "config set", "connect", "status", "doctor", "mode show", "mode set",
	"agent list", "agent revoke", "invitation create", "project list", "project show", "project create", "project change", "project delegate", "project authorize",
	"task list", "task show", "task create", "task change", "task history", "task runs",
	"comment add", "comment edit", "worklog add", "discussion list", "mention list", "mention acknowledge", "watch",
	"message send", "message show", "message inbox", "message receipt", "session request", "session list", "session show", "session change", "session open",
	"knowledge put", "knowledge list", "knowledge show", "knowledge history", "inventory refresh", "inventory show",
	"workspace bind", "workspace list", "workspace enable", "workspace disable",
	"provider status", "provider install", "provider update", "provider login", "preferences show", "preferences set",
	"work prompt", "work status", "work run", "work recover", "service plan", "service install", "service status",
}

var FoundationCommands = []string{"init", "serve", "doctor", "bootstrap", "tls-renew", "backup-key", "backup", "restore", "service plan", "service install"}
var ServiceCommands = []string{"serve", "status"}

func RunAgent(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer) int {
	return run(ctx, "agent", args, in, out, diagnostic)
}
func RunFoundation(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer) int {
	return run(ctx, "foundation", args, in, out, diagnostic)
}
func RunService(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer) int {
	return run(ctx, "service", args, in, out, diagnostic)
}

func run(ctx context.Context, role string, args []string, in io.Reader, out, diagnostic io.Writer) int {
	a := &app{in: in, out: out, diagnostic: diagnostic}
	defaultConfig, homeErr := config.DefaultConfig()
	if role == "foundation" && defaultConfig != "" {
		defaultConfig = filepath.Join(filepath.Dir(defaultConfig), "server.json")
	}
	flags := flag.NewFlagSet(role, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&a.configFile, "config", defaultConfig, "private configuration path")
	flags.BoolVar(&a.json, "json", false, "structured output (all ordinary commands already emit JSON)")
	flags.BoolVar(&a.nonInteractive, "non-interactive", false, "never open protected terminal UI")
	flags.IntVar(&a.timeout, "timeout", 60, "command timeout in seconds (1-300)")
	flags.StringVar(&a.operation, "operation-id", "", "stable write/replay operation")
	flags.Int64Var(&a.expectedRevision, "expected-rev", 0, "explicit optimistic revision")
	version := flags.Bool("version", false, "print version")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return a.emit(map[string]any{"commands": commands(role), "global_options": "--config --json --non-interactive --timeout --operation-id --expected-rev --version"}, nil)
	}
	if err != nil {
		return a.emit(nil, domain.Fail(domain.Invalid, "invalid global options; flags precede the command"))
	}
	if *version {
		return a.emit(map[string]any{"version": domain.Version, "commit": domain.Commit, "api_version": domain.APIVersion, "schema_version": domain.SchemaVersion}, nil)
	}
	a.explicitOperation = a.operation != ""
	if a.operation == "" {
		a.operation = domain.NewID("op")
	}
	if !domain.ValidOperation(a.operation) || a.expectedRevision < 0 || a.timeout < 1 || a.timeout > 300 {
		return a.emit(nil, domain.Fail(domain.Invalid, "invalid operation, revision, or timeout (1-300 seconds)"))
	}
	tail := flags.Args()
	if len(tail) == 0 || tail[0] == "help" {
		return a.emit(map[string]any{"commands": commands(role), "usage": "global flags before command; IDs before leaf flags; --input FILE or - reads bounded JSON"}, nil)
	}
	for _, item := range tail {
		if item == "--help" || item == "-h" {
			key := tail[0]
			if len(tail) > 1 && !strings.HasPrefix(tail[1], "-") {
				key += " " + tail[1]
			}
			valid := false
			for _, command := range commands(role) {
				if command == key || strings.HasPrefix(command, key+" ") {
					valid = true
				}
			}
			if !valid {
				return a.emit(nil, domain.Fail(domain.Invalid, "unknown command"))
			}
			return a.emit(map[string]any{"command": key, "usage": "see README command reference; JSON changes require a reason and current generations"}, nil)
		}
	}
	if a.configFile == "" {
		return a.emit(nil, errors.Join(domain.Fail(domain.Prerequisite, "HOME unavailable; specify --config"), homeErr))
	}
	longRunning := tail[0] == "serve" || (tail[0] == "watch" && !slices.Contains(tail, "--once"))
	if !longRunning {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.timeout)*time.Second)
		defer cancel()
	}
	var value any
	switch role {
	case "foundation":
		value, err = a.foundation(ctx, tail)
	case "service":
		value, err = a.service(ctx, tail)
	default:
		value, err = a.agent(ctx, tail)
	}
	if errors.Is(err, context.Canceled) {
		err = domain.Fail(domain.Cancelled, "operation cancelled; persisted evidence retained")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = domain.Fail(domain.Unavailable, "command timed out; reconcile its operation ID before retrying a write")
	}
	return a.emit(value, err)
}

func commands(role string) []string {
	switch role {
	case "foundation":
		return FoundationCommands
	case "service":
		return ServiceCommands
	default:
		return AgentCommands
	}
}

func (a *app) emit(value any, err error) int {
	envelope := api.Envelope{OK: err == nil, OperationID: a.operation, Warnings: []string{}}
	if err != nil {
		envelope.Error = domain.AsFault(err)
	} else {
		var encodeErr error
		envelope.Data, encodeErr = json.Marshal(value)
		if encodeErr != nil {
			envelope.OK = false
			envelope.Error = domain.AsFault(encodeErr)
			err = encodeErr
		}
	}
	if encodeErr := json.NewEncoder(a.out).Encode(envelope); encodeErr != nil {
		_, _ = io.WriteString(a.diagnostic, "could not write structured command output\n")
		return 9
	}
	return domain.ExitCode(err)
}

func parse(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return domain.Fail(domain.Invalid, "invalid command options")
	}
	if fs.NArg() != 0 {
		return domain.Fail(domain.Invalid, "unexpected positional arguments")
	}
	return nil
}

func inputFile(path string, in io.Reader, value any) error {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(in, domain.MaxBody+1))
	} else {
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		data, err = io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
		closeErr := file.Close()
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		return err
	}
	if len(data) > domain.MaxBody {
		return domain.Fail(domain.Invalid, "input exceeds 1 MiB")
	}
	return config.Decode(data, value)
}

func (a *app) input(args []string, value any) error {
	fs := flag.NewFlagSet("input", flag.ContinueOnError)
	path := fs.String("input", "", "JSON file or -")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *path == "" {
		return domain.Fail(domain.Invalid, "--input FILE or - is required")
	}
	return inputFile(*path, a.in, value)
}

func (a *app) metadata(command *domain.Command) error {
	if a.explicitOperation && command.OperationID != "" && command.OperationID != a.operation {
		return domain.Fail(domain.Conflict, "input and --operation-id disagree")
	}
	if command.OperationID == "" {
		command.OperationID = a.operation
	} else {
		a.operation = command.OperationID
	}
	if a.expectedRevision != 0 {
		if command.ExpectedRevision != 0 && command.ExpectedRevision != a.expectedRevision {
			return domain.Fail(domain.Conflict, "input and --expected-rev disagree")
		}
		command.ExpectedRevision = a.expectedRevision
	}
	if !domain.ValidOperation(command.OperationID) {
		return domain.Fail(domain.Invalid, "invalid operation ID")
	}
	return nil
}

func idArgs(args []string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, domain.Fail(domain.Invalid, "entity ID is required before leaf flags")
	}
	return args[0], args[1:], nil
}
