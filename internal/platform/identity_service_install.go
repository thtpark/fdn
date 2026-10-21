package platform

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

// IdentityServiceEnvironment is a recording-test seam, not a runtime setting or
// privileged command endpoint. Native CLI execution always uses the native host.
type IdentityServiceEnvironment interface {
	LookupUser(context.Context, string) (AccountUser, bool, error)
	LookupUID(context.Context, uint32) (AccountUser, bool, error)
	LookupGroup(context.Context, string) (AccountGroup, bool, error)
	LookupGID(context.Context, uint32) (AccountGroup, bool, error)
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
// NewNativeIdentityServiceInstaller, whose root/OS guard cannot be configured.
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
	o := i.options
	for _, expected := range []struct {
		name, home, marker string
		uid, gid           int
		identity           bool
	}{{o.IdentityUser, o.IdentityHome, "fdn-identity=" + o.Name, o.IdentityUID, o.IdentityGID, true},
		{o.WorkerUser, o.WorkerHome, "fdn-runner=" + o.Name, o.WorkerUID, o.WorkerGID, false}} {
		account, found, err := i.host.LookupUser(ctx, expected.name)
		if err != nil {
			return err
		}
		if !found {
			return domain.Fail(domain.Prerequisite, "dedicated local identity and runner accounts must be provisioned exactly as service plan specifies; no account was created or adopted")
		}
		byID, found, err := i.host.LookupUID(ctx, uint32(expected.uid))
		if err != nil {
			return err
		}
		if account.UID == 0 || found && byID.UID == 0 {
			return domain.Fail(domain.Denied, "resolved identity and runner accounts must both have non-root UIDs; root aliases are not service accounts")
		}
		if !found || account.Username != expected.name || account.UID != uint32(expected.uid) || account.GID != uint32(expected.gid) ||
			account.Home != expected.home || account.Marker != expected.marker || byID.Username != account.Username || byID.UID != account.UID ||
			byID.GID != account.GID || byID.Home != account.Home || byID.Marker != account.Marker {
			return domain.Fail(domain.Conflict, "local account name/UID/group/HOME/managed marker does not match the explicit service plan; unrelated accounts are never adopted")
		}
		if expected.identity && !slices.Contains([]string{"/usr/sbin/nologin", "/sbin/nologin", "/usr/bin/false", "/bin/false"}, account.Shell) {
			return domain.Fail(domain.Prerequisite, "dedicated identity account must have a no-login shell before service installation")
		}
		if !slices.Contains(account.Groups, account.GID) || slices.Contains(account.Groups, uint32(0)) ||
			expected.identity && !slices.Contains(account.Groups, uint32(o.WorkerGID)) ||
			!expected.identity && slices.Contains(account.Groups, uint32(o.IdentityGID)) {
			return domain.Fail(domain.Prerequisite, "provision explicit identity-to-worker supplementary membership and exclude reverse/root group membership before installation")
		}
		if expected.identity {
			for _, group := range account.Groups {
				if group != uint32(o.IdentityGID) && group != uint32(o.WorkerGID) && !(o.OS == "darwin" && (group == 12 || group == 61)) {
					return domain.Fail(domain.Denied, "identity account must not inherit unrelated supplementary groups")
				}
			}
		}
		group, found, err := i.host.LookupGroup(ctx, expected.name)
		if err != nil {
			return err
		}
		if !found {
			return domain.Fail(domain.Prerequisite, "service plan requires each dedicated account's named primary group")
		}
		byGID, found, err := i.host.LookupGID(ctx, uint32(expected.gid))
		if err != nil {
			return err
		}
		if !found || group.Name != expected.name || group.GID != uint32(expected.gid) || byGID.Name != group.Name || byGID.GID != group.GID {
			return domain.Fail(domain.Conflict, "dedicated primary group name/GID reservation conflicts with the service plan")
		}
	}
	return nil
}

func (i *IdentityServiceInstaller) run(ctx context.Context, path string, args []string, worker bool) ([]byte, error) {
	o := i.options
	environment := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}
	if worker {
		uid := strconv.Itoa(o.WorkerUID)
		if o.OS == "linux" {
			args = append([]string{"--user", o.WorkerUser, "--", "/usr/bin/env", "-i", "HOME=" + o.WorkerHome, "USER=" + o.WorkerUser,
				"LOGNAME=" + o.WorkerUser, "PATH=/usr/bin:/bin", "LC_ALL=C", "XDG_RUNTIME_DIR=/run/user/" + uid,
				"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus", path}, args...)
			path = "/usr/sbin/runuser"
		} else {
			args = append([]string{"asuser", uid, "/usr/bin/sudo", "-n", "-H", "-u", o.WorkerUser, "--", path}, args...)
			path = "/bin/launchctl"
		}
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
		args := []string{"show", name, "--property=LoadState", "--property=ActiveState"}
		if worker {
			args = []string{"--user", "show", "fdn-worker-" + o.Name + ".service", "--property=LoadState", "--property=ActiveState"}
		}
		data, err := i.run(ctx, "/usr/bin/systemctl", args, worker)
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
	target := "system/org.foundation.identity." + o.Name
	if worker {
		target = "gui/" + strconv.Itoa(o.WorkerUID) + "/org.foundation.runner." + o.Name
	}
	data, err := i.run(ctx, "/bin/launchctl", []string{"print", target}, worker)
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
		return status, domain.Fail(domain.Denied, "service install requires explicit --apply and administrator execution; service plan is read-only")
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
	if o.OS == "darwin" {
		if _, err := i.run(ctx, "/bin/launchctl", []string{"print", "gui/" + strconv.Itoa(o.WorkerUID)}, true); err != nil {
			return status, domain.Fail(domain.Prerequisite, "runner GUI domain is not available; provision its native login domain before installing the LaunchAgent")
		}
	}
	if err := i.host.WriteDefinitions(o, plan); err != nil {
		return status, err
	}
	if o.OS == "linux" {
		for _, command := range []struct {
			path string
			args []string
		}{
			{"/usr/bin/loginctl", []string{"enable-linger", o.WorkerUser}},
			{"/usr/bin/systemctl", []string{"start", "user@" + strconv.Itoa(o.WorkerUID) + ".service"}},
		} {
			if _, err := i.run(ctx, command.path, command.args, false); err != nil {
				return status, domain.Fail(domain.Prerequisite, "runner user-manager persistence/start remains pending; retry the same service install after correcting the native prerequisite")
			}
		}
		if err := i.host.RunnerDomain(o); err != nil {
			return status, err
		}
		for n, definition := range []Definition{plan.Identity, plan.Worker} {
			for _, command := range definition.Commands {
				if _, err := i.run(ctx, "/usr/bin/systemctl", command[1:], n == 1); err != nil {
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
				target := "system/org.foundation.identity." + o.Name
				if n == 1 {
					target = "gui/" + strconv.Itoa(o.WorkerUID) + "/org.foundation.runner." + o.Name
				}
				command = []string{"kickstart", target}
			}
			if _, err := i.run(ctx, "/bin/launchctl", command, n == 1); err != nil {
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
