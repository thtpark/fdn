package platform

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

// IdentityServiceEnvironment is a recording-test seam, not a runtime setting or
// privileged command endpoint. Native CLI execution always uses the native host.
type IdentityServiceEnvironment interface {
	CurrentUser(context.Context) (AccountUser, error)
	ValidateLayout(IdentityServiceOptions) error
	WriteDefinitions(IdentityServiceOptions, IdentityServicePlan) error
	RunnerDomain(IdentityServiceOptions) error
}

type IdentityUnitStatus struct {
	Registered bool `json:"registered"`
	Running    bool `json:"running"`
}

type IdentityServiceStatus struct {
	OS       string             `json:"os"`
	Name     string             `json:"name"`
	Identity IdentityUnitStatus `json:"identity"`
	Worker   IdentityUnitStatus `json:"worker"`
}

type IdentityServiceInstaller struct {
	options IdentityServiceOptions
	host    IdentityServiceEnvironment
	runner  process.Runner
	native  bool
}

// NewIdentityServiceInstaller embeds a recording host. Production must use
// NewNativeIdentityServiceInstaller, whose current-user/OS guard cannot be configured.
func NewIdentityServiceInstaller(o IdentityServiceOptions, host IdentityServiceEnvironment, runner process.Runner) (*IdentityServiceInstaller, error) {
	if _, err := RenderIdentityServices(o); err != nil {
		return nil, err
	}
	if host == nil || runner == nil {
		return nil, domain.Fail(domain.Invalid, "identity service installation requires native or recording boundaries")
	}
	return &IdentityServiceInstaller{options: o, host: host, runner: runner}, nil
}

func (i *IdentityServiceInstaller) guard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.native {
		return nativeIdentityInstallGuard(i.options.OS)
	}
	return nil
}

func (i *IdentityServiceInstaller) checkAccounts(ctx context.Context) error {
	account, err := i.host.CurrentUser(ctx)
	if err != nil {
		return err
	}
	o := i.options
	if account.UID == 0 {
		return domain.Fail(domain.Denied, "run worker service installation/status as the existing worker user, not root or sudo")
	}
	if account.Username != o.User || int(account.UID) != o.UID || int(account.GID) != o.GID || account.Home != o.Home {
		return domain.Fail(domain.Conflict, "service plan must match the actual installing user's UID/GID/HOME; the local username need not match the enrolled agent name")
	}
	return nil
}

func (i *IdentityServiceInstaller) run(ctx context.Context, path string, args []string) ([]byte, error) {
	o := i.options
	environment := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + o.Home, "USER=" + o.User, "LOGNAME=" + o.User}
	if o.OS == "linux" {
		runtimeDir := "/run/user/" + strconv.Itoa(o.UID)
		environment = append(environment, "XDG_RUNTIME_DIR="+runtimeDir, "DBUS_SESSION_BUS_ADDRESS=unix:path="+runtimeDir+"/bus")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stdout, stderr := process.NewBuffer(64<<10), process.NewBuffer(8<<10)
	err := i.runner.Run(ctx, process.Spec{Path: path, Args: args, Env: environment, Stdout: stdout, Stderr: stderr})
	data, outputErr := stdout.Bytes()
	_, diagnosticErr := stderr.Bytes()
	return data, errors.Join(err, outputErr, diagnosticErr)
}

func (i *IdentityServiceInstaller) probe(ctx context.Context, worker bool) (IdentityUnitStatus, error) {
	o := i.options
	if o.OS == "linux" {
		name := "fdn-identity-" + o.Name + ".service"
		args := []string{"--user", "show", name, "--property=LoadState", "--property=ActiveState"}
		if worker {
			args = []string{"--user", "show", "fdn-worker-" + o.Name + ".service", "--property=LoadState", "--property=ActiveState"}
		}
		data, err := i.run(ctx, "/usr/bin/systemctl", args)
		if err != nil {
			return IdentityUnitStatus{}, err
		}
		values := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok || (key != "LoadState" && key != "ActiveState") || values[key] != "" {
				return IdentityUnitStatus{}, domain.Fail(domain.Unavailable, "native service status did not match its bounded contract")
			}
			values[key] = value
		}
		if len(values) != 2 {
			return IdentityUnitStatus{}, domain.Fail(domain.Unavailable, "native service status omitted state")
		}
		return IdentityUnitStatus{Registered: values["LoadState"] == "loaded", Running: values["LoadState"] == "loaded" && values["ActiveState"] == "active"}, nil
	}
	target := "gui/" + strconv.Itoa(o.UID) + "/org.foundation.identity." + o.Name
	if worker {
		target = "gui/" + strconv.Itoa(o.UID) + "/org.foundation.runner." + o.Name
	}
	data, err := i.run(ctx, "/bin/launchctl", []string{"print", target})
	if err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) && status.ExitCode() == 113 {
			return IdentityUnitStatus{}, nil
		}
		return IdentityUnitStatus{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if state, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return IdentityUnitStatus{Registered: true, Running: state == "running"}, nil
		}
	}
	return IdentityUnitStatus{}, domain.Fail(domain.Unavailable, "native launchd status omitted service state")
}

func (i *IdentityServiceInstaller) Status(ctx context.Context) (IdentityServiceStatus, error) {
	status := IdentityServiceStatus{OS: i.options.OS, Name: i.options.Name}
	if err := i.guard(ctx); err != nil {
		return status, err
	}
	if err := i.checkAccounts(ctx); err != nil {
		return status, err
	}
	if err := i.host.ValidateLayout(i.options); err != nil {
		return status, err
	}
	if err := i.host.RunnerDomain(i.options); err != nil {
		return status, err
	}
	var err error
	status.Identity, err = i.probe(ctx, false)
	if err != nil {
		return status, domain.Fail(domain.Unavailable, "cannot inspect the named identity service")
	}
	status.Worker, err = i.probe(ctx, true)
	if err != nil {
		return status, domain.Fail(domain.Unavailable, "cannot inspect the named runner service")
	}
	return status, nil
}

func (i *IdentityServiceInstaller) Install(ctx context.Context, apply bool) (IdentityServiceStatus, error) {
	status := IdentityServiceStatus{OS: i.options.OS, Name: i.options.Name}
	if !apply {
		return status, domain.Fail(domain.Denied, "service install requires explicit --apply as the installing user; service plan is read-only")
	}
	if err := i.guard(ctx); err != nil {
		return status, err
	}
	if err := i.checkAccounts(ctx); err != nil {
		return status, err
	}
	if err := i.host.ValidateLayout(i.options); err != nil {
		return status, err
	}
	plan, err := RenderIdentityServices(i.options)
	if err != nil {
		return status, err
	}
	o := i.options
	if err := i.host.RunnerDomain(o); err != nil {
		return status, err
	}
	if o.OS == "darwin" {
		if _, err := i.run(ctx, "/bin/launchctl", []string{"print", "gui/" + strconv.Itoa(o.UID)}); err != nil {
			return status, domain.Fail(domain.Prerequisite, "the installing user's GUI domain is unavailable; log in as that existing user before installing both LaunchAgents")
		}
	}
	if err := i.host.WriteDefinitions(o, plan); err != nil {
		return status, err
	}
	if o.OS == "linux" {
		for _, definition := range []Definition{plan.Identity, plan.Worker} {
			for _, command := range definition.Commands {
				if _, err := i.run(ctx, "/usr/bin/systemctl", command[1:]); err != nil {
					return status, domain.Fail(domain.Unavailable, "native service registration failed; installed definitions are retained for exact retry")
				}
			}
		}
	} else {
		for n, definition := range []Definition{plan.Identity, plan.Worker} {
			state, err := i.probe(ctx, n == 1)
			if err != nil {
				return status, domain.Fail(domain.Unavailable, "cannot reconcile the named launchd service before registration")
			}
			if state.Running {
				continue
			}
			command := definition.Commands[0][1:]
			if state.Registered {
				target := "gui/" + strconv.Itoa(o.UID) + "/org.foundation.identity." + o.Name
				if n == 1 {
					target = "gui/" + strconv.Itoa(o.UID) + "/org.foundation.runner." + o.Name
				}
				command = []string{"kickstart", target}
			}
			if _, err := i.run(ctx, "/bin/launchctl", command); err != nil {
				return status, domain.Fail(domain.Unavailable, "native launchd registration remains pending; retry the same approved install")
			}
		}
	}
	status, err = i.Status(ctx)
	if err != nil {
		return status, err
	}
	if !status.Identity.Running || !status.Worker.Running {
		return status, domain.Fail(domain.Prerequisite, "service definitions are installed but both named services are not running; reconcile private preparation/runner attachment before retrying")
	}
	return status, nil
}
