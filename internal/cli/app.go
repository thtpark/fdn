package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
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
	nativeOutput      bool
}

var AgentCommands = []string{
	"config init", "config show", "config set", "connect", "status", "doctor", "mode show", "mode set",
	"agent list", "agent status", "agent diagnose", "agent repair", "agent operation", "agent revoke", "agent register", "agent recover", "agent identity", "project list", "project show", "project create", "project change", "project delegate", "project authorize",
	"task list", "task show", "task create", "task change", "task history", "task runs",
	"comment add", "comment edit", "worklog add", "discussion list", "mention list", "mention acknowledge", "watch",
	"message send", "message show", "message read", "message diagnose", "message inbox", "message receipt", "session request", "session list", "session show", "session change", "session open",
	"knowledge put", "knowledge list", "knowledge show", "knowledge history", "inventory refresh", "inventory show",
	"workspace bind", "workspace list", "workspace enable", "workspace disable", "screen list", "screen launch",
	"provider status", "provider install", "provider update", "provider login", "provider link", "provider exec", "preferences show", "preferences set",
	"work prompt", "work status", "work run", "work recover", "service plan", "service install", "service status",
	"upgrade check", "upgrade plan", "upgrade apply", "upgrade status",
	"upgrade-lease",
}

var FoundationCommands = []string{"init", "serve", "doctor", "gateway", "accounts serve", "recover-founder", "tls-renew", "backup-key", "backup", "restore", "service plan", "service install", "upgrade-helper"}
var ServiceCommands = []string{"serve", "status", "upgrade-helper"}
var GuideCommands = []string{"guide list", "guide show", "guide install"}
var AutonomyCommands = []string{"agent show", "agent mode", "agent request-sign-in", "assistance request", "assistance grant", "assistance list", "assistance show", "assistance revoke", "assistance open"}

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
	if role == "foundation" {
		defaultConfig, homeErr = config.DefaultServerConfig()
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
		if item == "--" {
			break
		}
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
			usage := "see README command reference; JSON changes require a reason and current generations"
			switch key {
			case "upgrade check":
				usage = "upgrade check [--platform linux-amd64|linux-arm64|darwin-arm64] [--input TARGETS]; Founder-only read-only published-release discovery and optional actual native inventory; no mode, package, service or inference changes"
			case "upgrade plan":
				usage = "upgrade plan --input TARGETS --output PLAN; Founder-only native target challenges, exact version/commit/hash and off-host age recovery key; no service stop or installation"
			case "upgrade apply":
				usage = "upgrade apply --input PLAN; explicit Founder rollout through separately administrator-provisioned narrow updaters; pause, encrypted cold backups, Foundation first, exact source and retained identity/fence checks; leave automation paused, never retry uncertain installation or roll back databases"
			case "upgrade status":
				usage = "upgrade status --input PLAN; inspect exact native receipts after an uncertain rollout; never repeats installation"
			case "upgrade-helper":
				usage = "upgrade-helper --policy FILE; separately provisioned native OS updater, bounded protobuf stdin/stdout only; requires pinned Foundation-signed authorization and fixed local policy, not an ordinary agent message or generic root command"
			case "upgrade-lease":
				usage = "upgrade-lease; internal native updater start proof using bounded protobuf stdin/stdout; exact current target or issuer only, rechecks current Founder/generations/pause and starts no installation or inference"
			case "agent diagnose":
				usage = "agent diagnose AGENT_ID [--refresh]; default is read-only latest saved observations for Founder or self; --refresh queues a Founder-only check with a fixed 15-minute lifespan, not fresh success; inspect agent operation REQUEST_ID; no login, inference or mode change"
			case "agent repair":
				usage = "agent repair AGENT_ID --input FILE; Founder-only exact closed-session recovery using session_id, expected_fence SHA256 from a fresh diagnostic operation and a nonsecret reason; target independently requires durable native cleanup proof; no acknowledge-stopped, journal retry, native restart or mode change; inspect agent operation REQUEST_ID"
			case "agent operation":
				usage = "agent operation REQUEST_ID; read-only common state/outcome for diagnostic refresh and session recovery, visible to requester Founder or target; queued/checking is not fresh success; no leaf flags"
			case "agent status":
				usage = "agent status --input FILE; publish your own concise non-secret team-visible text (max160 characters), or explicit empty text to restore automatic activity;60-second normal coalescing, unchanged text suppressed, important controller transitions immediate; no target, priority, mode or timestamp override"
			case "comment add", "comment edit", "worklog add":
				usage = key + " ID --input FILE; body mentions accept @{NAME} (case-insensitive active agent name) or @{AGENT_ID}; names must be unambiguous, recipients are stored as agent IDs, and self/duplicate mentions are excluded; only comments create attention; edits require the creator and current revision"
			case "mention list":
				usage = "mention list [--cursor CURSOR --limit N]; pending comment mentions addressed to your agent ID, including those written as @{NAME}; mentioning is not assignment or authority"
			case "mention acknowledge":
				usage = "mention acknowledge COMMENT --input FILE; acknowledge your exact current mention generation with state handled/deferred; edited or removed mentions cannot be acknowledged using an old generation"
			case "message read":
				usage = "message read MESSAGE_ID; recipient-only explicit acknowledgement after finishing reading; records immutable first server read_at without changing disposition/revision; show, inbox, diagnostics and replies never mark automatically; no timestamp or leaf flags"
			case "message diagnose":
				usage = "message diagnose MESSAGE_ID; read-only observed lifecycle (state), phase (stage) and explicit read_at for sender/recipient; not proof of authoritative receipt or AI completion; absent read_at means no explicit reading acknowledgement, not proof the message was unseen"
			}
			return a.emit(map[string]any{"command": key, "usage": usage}, nil)
		}
	}
	if a.configFile == "" && tail[0] != "guide" {
		return a.emit(nil, errors.Join(domain.Fail(domain.Prerequisite, "default configuration unavailable; specify --config"), homeErr))
	}
	longRunning := tail[0] == "serve" || tail[0] == "gateway" || tail[0] == "upgrade-helper" ||
		(tail[0] == "upgrade" && len(tail) > 1 && tail[1] == "apply") ||
		(tail[0] == "accounts" && len(tail) > 1 && tail[1] == "serve") || (tail[0] == "watch" && !slices.Contains(tail, "--once"))
	if len(tail) > 1 && ((tail[0] == "provider" && tail[1] == "exec") || (tail[0] == "assistance" && tail[1] == "open")) {
		longRunning = true
		flags.Visit(func(option *flag.Flag) {
			if option.Name == "timeout" {
				longRunning = false
			}
		})
	}
	if !longRunning {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.timeout)*time.Second)
		defer cancel()
	}
	var value any
	if tail[0] == "guide" {
		value, err = a.guide(ctx, tail[1:])
		return a.emit(value, err)
	}
	switch role {
	case "foundation":
		value, err = a.foundation(ctx, tail)
	case "service":
		value, err = a.service(ctx, tail)
	default:
		value, err = a.agent(ctx, tail)
	}
	if a.nativeOutput {
		if err == nil {
			return 0
		}
		var native *exec.ExitError
		if errors.As(err, &native) {
			if native.ExitCode() >= 0 {
				return native.ExitCode()
			}
			if status, ok := native.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return 128 + int(status.Signal())
			}
		}
		if errors.Is(err, context.Canceled) {
			return 130
		}
		_, _ = fmt.Fprintln(a.diagnostic, domain.AsFault(err).Error())
		return domain.ExitCode(err)
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
	var entries []string
	switch role {
	case "foundation":
		entries = FoundationCommands
	case "service":
		entries = ServiceCommands
	default:
		entries = append(append([]string{}, AgentCommands...), AutonomyCommands...)
	}
	return append(append([]string{}, entries...), GuideCommands...)
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
