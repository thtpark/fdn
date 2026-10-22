package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type identityInstallHost struct {
	users       map[string]AccountUser
	ids         map[uint32]AccountUser
	groups      map[string]AccountGroup
	gids        map[uint32]AccountGroup
	checks      int
	writes      int
	domain      int
	layoutError error
	domainError error
	written     IdentityServicePlan
}

func identityInstallFixture(o IdentityServiceOptions) *identityInstallHost {
	h := &identityInstallHost{users: map[string]AccountUser{}, ids: map[uint32]AccountUser{}, groups: map[string]AccountGroup{}, gids: map[uint32]AccountGroup{}}
	for _, user := range []AccountUser{
		{Username: o.IdentityUser, UID: uint32(o.IdentityUID), GID: uint32(o.IdentityGID), Home: o.IdentityHome, Shell: "/usr/sbin/nologin", Marker: "fdn-identity=" + o.Name, Groups: []uint32{uint32(o.IdentityGID), uint32(o.WorkerGID)}},
		{Username: o.WorkerUser, UID: uint32(o.WorkerUID), GID: uint32(o.WorkerGID), Home: o.WorkerHome, Shell: "/bin/sh", Marker: "fdn-runner=" + o.Name, Groups: []uint32{uint32(o.WorkerGID)}},
	} {
		h.users[user.Username], h.ids[user.UID] = user, user
		group := AccountGroup{Name: user.Username, GID: user.GID}
		h.groups[group.Name], h.gids[group.GID] = group, group
	}
	return h
}

func (h *identityInstallHost) LookupUser(_ context.Context, name string) (AccountUser, bool, error) {
	h.checks++
	u, ok := h.users[name]
	return u, ok, nil
}
func (h *identityInstallHost) LookupUID(_ context.Context, uid uint32) (AccountUser, bool, error) {
	u, ok := h.ids[uid]
	return u, ok, nil
}
func (h *identityInstallHost) LookupGroup(_ context.Context, name string) (AccountGroup, bool, error) {
	g, ok := h.groups[name]
	return g, ok, nil
}
func (h *identityInstallHost) LookupGID(_ context.Context, gid uint32) (AccountGroup, bool, error) {
	g, ok := h.gids[gid]
	return g, ok, nil
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
	if spec.Path == "/bin/launchctl" && len(args) > 0 && args[0] == "asuser" {
		if len(args) < 10 || args[2] != "/usr/bin/sudo" || args[3] != "-n" || args[4] != "-H" || args[5] != "-u" || args[7] != "--" || args[8] != "/bin/launchctl" {
			r.t.Error("macOS runner operation did not drop to its exact account", args)
		}
		args = args[9:]
	}
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

func TestIdentityNativeInstallUsesRootSystemAndExactRunnerManagerContexts(t *testing.T) {
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
	if !reflect.DeepEqual(record.calls[0].Args, []string{"enable-linger", o.WorkerUser}) || record.calls[0].Path != "/usr/bin/loginctl" ||
		record.calls[1].Path != "/usr/bin/systemctl" || !reflect.DeepEqual(record.calls[1].Args, []string{"start", "user@1102.service"}) {
		t.Fatal("runner user-manager persistence was not installed explicitly")
	}
	workerCalls := 0
	for _, call := range record.calls {
		for _, variable := range call.Env {
			if strings.Contains(variable, "FDN_PRIVATE_PROVIDER_SECRET") || strings.Contains(variable, "untrusted-user-bus") {
				t.Fatal("native lifecycle inherited private/untrusted environment")
			}
		}
		if call.Path == "/usr/sbin/runuser" {
			workerCalls++
			if !reflect.DeepEqual(call.Args[:5], []string{"--user", o.WorkerUser, "--", "/usr/bin/env", "-i"}) ||
				!strings.Contains(strings.Join(call.Args, "\n"), "XDG_RUNTIME_DIR=/run/user/1102") ||
				!strings.Contains(strings.Join(call.Args, "\n"), "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1102/bus") ||
				!strings.Contains(strings.Join(call.Args, "\n"), "/usr/bin/systemctl\n--user") {
				t.Fatal("runner service command used the caller/root user manager", call.Args)
			}
		}
	}
	if workerCalls != 3 || host.written.Identity.Path != o.IdentityDefinition || host.written.Worker.Path != o.WorkerDefinition {
		t.Fatal("missing explicit per-account service registration/status")
	}
	if _, err := installer.Install(context.Background(), true); err != nil {
		t.Fatal("same approved installation could not be retried", err)
	}
}

func darwinIdentityOptions() IdentityServiceOptions {
	o := identityOptions()
	o.OS = "darwin"
	o.IdentityHome, o.StateDir = "/Library/Application Support/fdn-identity/agent1", "/Library/Application Support/fdn-identity/agent1/state"
	o.IdentityConfig = o.IdentityHome + "/identity.json"
	o.WorkerHome, o.WorkerStateDir = "/Users/fdnw_agent1", "/Users/fdnw_agent1/.local/state/fdn"
	o.WorkerConfig = "/Users/fdnw_agent1/.config/fdn/agent.json"
	o.IdentityDefinition = "/Library/LaunchDaemons/org.foundation.identity.agent1.plist"
	o.WorkerDefinition = "/Users/fdnw_agent1/Library/LaunchAgents/org.foundation.runner.agent1.plist"
	return o
}

func TestIdentityNativeInstallUsesMacOSDaemonAndRunnerGUIWithUIDDrop(t *testing.T) {
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
		if len(call.Args) > 0 && call.Args[0] == "asuser" && call.Args[1] != "1102" {
			t.Fatal("launchd GUI domain used a caller or identity UID")
		}
	}
	if bootstraps != 2 || host.writes != 1 {
		t.Fatal("native installation did not bootstrap one identity daemon and one runner GUI service")
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

func TestIdentityNativeInstallNeverAdoptsMissingConflictingOrOverprivilegedAccounts(t *testing.T) {
	cases := map[string]func(*identityInstallHost, IdentityServiceOptions){
		"missing-identity": func(h *identityInstallHost, o IdentityServiceOptions) { delete(h.users, o.IdentityUser) },
		"wrong-marker": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.IdentityUser]
			u.Marker = "unrelated"
			h.users[o.IdentityUser] = u
		},
		"resolved-root-runner-alias": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.WorkerUser]
			u.UID = 0
			h.users[o.WorkerUser] = u
		},
		"resolved-root-identity-alias": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.IdentityUser]
			u.UID = 0
			h.users[o.IdentityUser] = u
		},
		"uid-collision": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.ids[uint32(o.IdentityUID)]
			u.Username = "unrelated"
			h.ids[u.UID] = u
		},
		"wrong-home": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.WorkerUser]
			u.Home = "/another/home"
			h.users[o.WorkerUser] = u
		},
		"login-identity": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.IdentityUser]
			u.Shell = "/bin/sh"
			h.users[o.IdentityUser] = u
		},
		"missing-worker-membership": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.IdentityUser]
			u.Groups = []uint32{u.GID}
			h.users[o.IdentityUser] = u
		},
		"reverse-private-membership": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.WorkerUser]
			u.Groups = append(u.Groups, uint32(o.IdentityGID))
			h.users[o.WorkerUser] = u
		},
		"unrelated-privileged-group": func(h *identityInstallHost, o IdentityServiceOptions) {
			u := h.users[o.IdentityUser]
			u.Groups = append(u.Groups, 666)
			h.users[o.IdentityUser] = u
		},
		"missing-primary-group": func(h *identityInstallHost, o IdentityServiceOptions) { delete(h.groups, o.IdentityUser) },
		"gid-collision": func(h *identityInstallHost, o IdentityServiceOptions) {
			g := h.gids[uint32(o.WorkerGID)]
			g.Name = "unrelated"
			h.gids[g.GID] = g
		},
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
	record.fail = "enable-linger"
	if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("missing native user-manager prerequisites looked successful", err)
	}
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
		t.Fatal("native installer bypassed its actual OS/root guard", err)
	}
}

func TestIdentityNativeConfigurationBindingAndStoppedRecoveryPreserveRunnerState(t *testing.T) {
	o := identityOptions()
	identity := config.IdentityService{StateDir: o.StateDir, Socket: filepath.Join(o.SocketDir, "i.sock"), WorkerUID: o.WorkerUID, WorkerGID: o.WorkerGID}
	worker := config.DefaultWorker(o.WorkerStateDir)
	worker.IdentitySocket = identity.Socket
	if err := validateIdentityServiceConfiguration(o, identity, worker); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config.IdentityService, *config.Worker){
		func(i *config.IdentityService, _ *config.Worker) { i.WorkerUID++ },
		func(i *config.IdentityService, _ *config.Worker) { i.Socket = "/wrong/i.sock" },
		func(_ *config.IdentityService, w *config.Worker) { w.SSH = &config.SSH{IdentityFile: "/private/key"} },
		func(_ *config.IdentityService, w *config.Worker) { w.CredentialFile = o.IdentityHome + "/device.json" },
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
	o.StateDir = filepath.Join(o.IdentityHome, "recovered-state")
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
