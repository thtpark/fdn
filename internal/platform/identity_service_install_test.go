package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type identityInstallHost struct {
	user        AccountUser
	checks      int
	writes      int
	domain      int
	layoutError error
	domainError error
	written     IdentityServicePlan
}

func identityInstallFixture(o IdentityServiceOptions) *identityInstallHost {
	return &identityInstallHost{user: AccountUser{Username: o.User, UID: uint32(o.UID), GID: uint32(o.GID), Home: o.Home,
		Shell: "/bin/zsh", Marker: "Existing human user", Groups: []uint32{0, uint32(o.GID), 80}}}
}

func (h *identityInstallHost) CurrentUser(context.Context) (AccountUser, error) {
	h.checks++
	return h.user, nil
}
func (h *identityInstallHost) ValidateLayout(IdentityServiceOptions) error { return h.layoutError }
func (h *identityInstallHost) WriteDefinitions(_ IdentityServiceOptions, plan IdentityServicePlan) error {
	h.writes++
	h.written = plan
	return nil
}
func (h *identityInstallHost) RunnerDomain(IdentityServiceOptions) error {
	h.domain++
	return h.domainError
}

type identityInstallRecorder struct {
	t      *testing.T
	calls  []process.Spec
	fail   string
	active map[string]bool
	os     string
}

type identityNativeExit int

func (e identityNativeExit) Error() string { return "recorded native exit" }
func (e identityNativeExit) ExitCode() int { return int(e) }

func (r *identityInstallRecorder) Run(ctx context.Context, spec process.Spec) error {
	if _, ok := ctx.Deadline(); !ok {
		r.t.Error("native manager call has no bounded context")
	}
	r.calls = append(r.calls, spec)
	joined := strings.Join(spec.Args, " ")
	if r.fail != "" && strings.Contains(joined, r.fail) {
		return errors.New("recorded native manager failure")
	}
	if r.os == "linux" {
		if strings.Contains(joined, "--property=LoadState") {
			_, err := io.WriteString(spec.Stdout, "LoadState=loaded\nActiveState=active\n")
			return err
		}
		return nil
	}
	args := spec.Args
	if len(args) == 0 {
		return errors.New("missing native action")
	}
	switch args[0] {
	case "print":
		if strings.HasPrefix(args[1], "gui/") && !strings.Contains(args[1], "org.foundation") {
			_, err := io.WriteString(spec.Stdout, "type = login\n")
			return err
		}
		if !r.active[args[1]] {
			return identityNativeExit(113)
		}
		_, err := io.WriteString(spec.Stdout, "state = running\n")
		return err
	case "bootstrap":
		name := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		r.active[args[1]+"/"+name] = true
		return nil
	case "kickstart":
		r.active[args[1]] = true
		return nil
	}
	return errors.New("unexpected native command")
}

func TestIdentityNativeInstallUsesOnlyExistingUserManager(t *testing.T) {
	o := identityOptions()
	host := identityInstallFixture(o)
	record := &identityInstallRecorder{t: t, os: o.OS}
	installer, err := NewIdentityServiceInstaller(o, host, record)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FDN_PRIVATE_PROVIDER_SECRET", "never-inherit")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "untrusted-user-bus")
	status, err := installer.Install(context.Background(), true)
	if err != nil || !status.Identity.Registered || !status.Worker.Running || host.writes != 1 {
		t.Fatal(status, host.writes, err)
	}
	for _, call := range record.calls {
		for _, variable := range call.Env {
			if strings.Contains(variable, "FDN_PRIVATE_PROVIDER_SECRET") || strings.Contains(variable, "untrusted-user-bus") {
				t.Fatal("native lifecycle inherited private/untrusted environment")
			}
		}
		if call.Path != "/usr/bin/systemctl" || len(call.Args) == 0 || call.Args[0] != "--user" ||
			!slices.Contains(call.Env, "HOME="+o.Home) || !slices.Contains(call.Env, "XDG_RUNTIME_DIR=/run/user/1102") ||
			!slices.Contains(call.Env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1102/bus") {
			t.Fatal("service command switched users or invoked a system manager", call.Path, call.Args)
		}
	}
	if len(record.calls) != 6 || host.written.Identity.Path != o.IdentityDefinition || host.written.Worker.Path != o.WorkerDefinition {
		t.Fatal("missing paired current-user service registration/status")
	}
	if _, err := installer.Install(context.Background(), true); err != nil {
		t.Fatal("same approved installation could not be retried", err)
	}
}

func TestIdentityNativeInstallUsesOnlyExistingMacOSUserGUI(t *testing.T) {
	o := darwinIdentityOptions()
	host := identityInstallFixture(o)
	record := &identityInstallRecorder{t: t, os: o.OS, active: map[string]bool{}}
	installer, err := NewIdentityServiceInstaller(o, host, record)
	if err != nil {
		t.Fatal(err)
	}
	status, err := installer.Install(context.Background(), true)
	if err != nil || !status.Identity.Running || !status.Worker.Running {
		t.Fatal(status, err)
	}
	bootstraps := 0
	for _, call := range record.calls {
		if strings.Contains(strings.Join(call.Args, " "), "bootstrap") {
			bootstraps++
		}
		if call.Path != "/bin/launchctl" || len(call.Args) < 2 || !strings.HasPrefix(call.Args[1], "gui/1102") {
			t.Fatal("LaunchAgent registration switched user or used a system daemon", call.Path, call.Args)
		}
	}
	if bootstraps != 2 || host.writes != 1 {
		t.Fatal("native installation did not bootstrap both existing-user LaunchAgents")
	}
	before := len(record.calls)
	if _, err := installer.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, call := range record.calls[before:] {
		if strings.Contains(strings.Join(call.Args, " "), "bootstrap") || strings.Contains(strings.Join(call.Args, " "), "kickstart") {
			t.Fatal("exact retry restarted a live owner/service")
		}
	}
}

func TestIdentityNativeInstallRejectsAnotherUserWithoutAccountMutation(t *testing.T) {
	cases := map[string]func(*identityInstallHost, IdentityServiceOptions){
		"root":       func(h *identityInstallHost, _ IdentityServiceOptions) { h.user.UID = 0 },
		"other-uid":  func(h *identityInstallHost, _ IdentityServiceOptions) { h.user.UID++ },
		"other-user": func(h *identityInstallHost, _ IdentityServiceOptions) { h.user.Username = "unrelated" },
		"other-gid":  func(h *identityInstallHost, _ IdentityServiceOptions) { h.user.GID++ },
		"other-home": func(h *identityInstallHost, _ IdentityServiceOptions) { h.user.Home = "/another/home" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := identityOptions()
			host := identityInstallFixture(o)
			mutate(host, o)
			record := &identityInstallRecorder{t: t, os: o.OS}
			installer, err := NewIdentityServiceInstaller(o, host, record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Install(context.Background(), true); err == nil || host.writes != 0 || len(record.calls) != 0 {
				t.Fatal("installation adopted an unprovisioned or unrelated account/group", err)
			}
		})
	}
}

func TestIdentityNativeInstallApplyAndPrerequisiteFailuresRemainObservable(t *testing.T) {
	o := identityOptions()
	host := identityInstallFixture(o)
	record := &identityInstallRecorder{t: t, os: o.OS}
	installer, err := NewIdentityServiceInstaller(o, host, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), false); domain.AsFault(err).Code != domain.Denied || host.checks != 0 || host.writes != 0 || len(record.calls) != 0 {
		t.Fatal("missing --apply crossed a mutation boundary", err)
	}
	host.layoutError = domain.Fail(domain.Denied, "recorded private path mismatch")
	if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != domain.Denied || host.writes != 0 {
		t.Fatal("untrusted private layout was installed", err)
	}
	host.layoutError = nil
	host.domainError = domain.Fail(domain.Prerequisite, "no existing user bus")
	if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("missing native user-manager prerequisites looked successful", err)
	}
	if host.writes != 0 || len(record.calls) != 0 {
		t.Fatal("missing user manager caused definitions or system/account mutations")
	}
	host.domainError = nil
	other := o
	if runtime.GOOS == "linux" {
		other = darwinIdentityOptions()
	}
	installer, err = NewNativeIdentityServiceInstaller(other, record)
	if err != nil {
		t.Fatal(err)
	}
	before := len(record.calls)
	if _, err := installer.Install(context.Background(), true); err == nil || len(record.calls) != before {
		t.Fatal("native installer bypassed its actual OS/current-user guard", err)
	}
}

func TestIdentityNativeConfigurationBindingAndStoppedRecoveryPreserveRunnerState(t *testing.T) {
	o := identityOptions()
	identity := config.IdentityService{StateDir: o.StateDir, Socket: filepath.Join(o.SocketDir, "i.sock"), WorkerUID: o.UID, WorkerGID: o.GID}
	worker := config.DefaultWorker(o.WorkerStateDir)
	worker.IdentitySocket = identity.Socket
	if err := validateIdentityServiceConfiguration(o, identity, worker); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config.IdentityService, *config.Worker){
		func(i *config.IdentityService, _ *config.Worker) { i.WorkerUID++ },
		func(i *config.IdentityService, _ *config.Worker) { i.Socket = "/wrong/i.sock" },
		func(_ *config.IdentityService, w *config.Worker) { w.SSH = &config.SSH{IdentityFile: "/private/key"} },
		func(_ *config.IdentityService, w *config.Worker) { w.CredentialFile = o.StateDir + "/device.json" },
		func(_ *config.IdentityService, w *config.Worker) { w.IdentitySocket = "/another/socket" },
	} {
		i, w := identity, worker
		mutate(&i, &w)
		if err := validateIdentityServiceConfiguration(o, i, w); err == nil {
			t.Fatal("installer accepted a mismatched or private-key-bearing runner configuration")
		}
	}
	before, err := RenderIdentityServices(o)
	if err != nil {
		t.Fatal(err)
	}
	o.StateDir = filepath.Join(o.Home, ".local", "state", "recovered-identity")
	identity.StateDir = o.StateDir
	after, err := RenderIdentityServices(o)
	if err != nil || before.Identity.Content != after.Identity.Content || before.Worker.Content != after.Worker.Content || validateIdentityServiceConfiguration(o, identity, worker) != nil {
		t.Fatal("fresh private StateDir recovery requires deleting old keys, changing runner journal paths or adopting a new account", err)
	}
}

func identityInstallTestDir(t *testing.T) string {
	t.Helper()
	return platformTestRoot(t, ".identity-install-test-")
}

func TestIdentityNativeFilesPreserveOwnershipAndRefuseForeignDefinitions(t *testing.T) {
	root := identityInstallTestDir(t)
	definition := Definition{Path: filepath.Join(root, "identity.service"), Content: "[Service]\nUser=dedicated\n"}
	if err := identityInstallDefinition(definition, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(definition.Path)
	if err != nil || first.Mode().Perm() != 0644 {
		t.Fatal(err)
	}
	if err := identityInstallDefinition(definition, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal("exact definition retry failed", err)
	}
	second, _ := os.Stat(definition.Path)
	if !os.SameFile(first, second) {
		t.Fatal("exact retry replaced an existing owned definition unnecessarily")
	}
	foreign := definition
	foreign.Content = "foreign definition must survive"
	if err := identityInstallDefinition(foreign, os.Geteuid(), os.Getegid()); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("existing definition was silently adopted", err)
	}
	data, _ := os.ReadFile(definition.Path)
	if !bytes.Equal(data, []byte(definition.Content)) {
		t.Fatal("unrelated native definition was modified")
	}
	link := filepath.Join(root, "linked.service")
	if err := os.Symlink(definition.Path, link); err != nil {
		t.Fatal(err)
	}
	if err := identityInstallDefinition(Definition{Path: link, Content: definition.Content}, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("symlinked native definition was accepted")
	}
	private := filepath.Join(root, "private.json")
	if err := os.WriteFile(private, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := identityInstallRead(private, uint32(os.Geteuid()), true, false); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("nonprivate account configuration was accepted", err)
	}
	if err := os.Chmod(private, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := identityInstallRead(private, uint32(os.Geteuid()), true, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := identityInstallRead(private, uint32(os.Geteuid()), true, false); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("worker-writable ancestor was trusted", err)
	}
}
