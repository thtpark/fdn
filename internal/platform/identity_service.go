package platform

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

// IdentityServiceOptions describes two processes owned by one existing user.
type IdentityServiceOptions struct {
	OS                 string `json:"os"`
	Name               string `json:"name"`
	User               string `json:"user,omitempty"`
	UID                int    `json:"uid,omitempty"`
	GID                int    `json:"gid,omitempty"`
	Home               string `json:"home,omitempty"`
	IdentityBinary     string `json:"identity_binary"`
	WorkerBinary       string `json:"worker_binary"`
	IdentityConfig     string `json:"identity_config"`
	WorkerConfig       string `json:"worker_config"`
	StateDir           string `json:"state_dir"`
	WorkerStateDir     string `json:"worker_state_dir"`
	SocketDir          string `json:"socket_dir"`
	IdentityDefinition string `json:"identity_definition"`
	WorkerDefinition   string `json:"worker_definition"`
}

func ResolveIdentityServiceUser(o IdentityServiceOptions) (IdentityServiceOptions, error) {
	user, err := currentIdentityServiceUser()
	if err != nil {
		return o, err
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.User == "" {
		o.User = user.Username
	}
	if o.UID == 0 {
		o.UID = int(user.UID)
	}
	if o.GID == 0 {
		o.GID = int(user.GID)
	}
	if o.Home == "" {
		o.Home = user.Home
	}
	return o, nil
}

type IdentityAccountPlan struct {
	Name     string `json:"name"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Home     string `json:"home"`
	Existing bool   `json:"existing"`
}

type IdentityDirectoryPlan struct {
	Path string `json:"path"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Mode uint32 `json:"mode"`
}

type IdentityServicePlan struct {
	Accounts    []IdentityAccountPlan   `json:"accounts"`
	Directories []IdentityDirectoryPlan `json:"directories"`
	Identity    Definition              `json:"identity"`
	Worker      Definition              `json:"worker"`
	Notes       []string                `json:"notes"`
}

func identityPathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func RenderIdentityServices(o IdentityServiceOptions) (IdentityServicePlan, error) {
	var plan IdentityServicePlan
	if (o.OS != "linux" && o.OS != "darwin") || !username.MatchString(o.Name) || o.User == "" ||
		len(o.User) > 256 || strings.ContainsAny(o.User, "\x00\r\n") || o.UID <= 0 || o.UID > 1<<31-1 || o.GID < 0 || o.GID > 1<<31-1 {
		return plan, domain.Fail(domain.Invalid, "worker services require one existing non-root installing user on Linux or macOS; the local username is independent of the enrolled agent name")
	}
	for _, path := range []string{o.IdentityBinary, o.WorkerBinary, o.IdentityConfig, o.WorkerConfig, o.StateDir, o.WorkerStateDir,
		o.Home, o.SocketDir, o.IdentityDefinition, o.WorkerDefinition} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n\t") {
			return plan, domain.Fail(domain.Invalid, "identity service plan needs clean absolute single-line paths")
		}
	}
	for _, path := range []string{o.IdentityConfig, o.WorkerConfig, o.StateDir, o.WorkerStateDir, o.SocketDir} {
		if path == o.Home || !identityPathWithin(o.Home, path) {
			return plan, domain.Fail(domain.Denied, "worker configuration, identity state and IPC must be inside the installing user's existing HOME")
		}
	}
	if identityPathWithin(o.StateDir, o.WorkerStateDir) || identityPathWithin(o.WorkerStateDir, o.StateDir) ||
		identityPathWithin(o.StateDir, o.SocketDir) || identityPathWithin(o.SocketDir, o.StateDir) ||
		identityPathWithin(o.WorkerStateDir, o.SocketDir) || identityPathWithin(o.SocketDir, o.WorkerStateDir) ||
		o.IdentityConfig == o.WorkerConfig {
		return plan, domain.Fail(domain.Invalid, "identity state, runner state, configurations and IPC need distinct locations in the same user's HOME")
	}
	definitions := filepath.Join(o.Home, ".config", "systemd", "user")
	identityDefinition := filepath.Join(definitions, "fdn-identity-"+o.Name+".service")
	workerDefinition := filepath.Join(definitions, "fdn-worker-"+o.Name+".service")
	if o.OS == "darwin" {
		definitions = filepath.Join(o.Home, "Library", "LaunchAgents")
		identityDefinition = filepath.Join(definitions, "org.foundation.identity."+o.Name+".plist")
		workerDefinition = filepath.Join(definitions, "org.foundation.runner."+o.Name+".plist")
	}
	if o.IdentityDefinition != identityDefinition || o.WorkerDefinition != workerDefinition || filepath.Base(o.IdentityBinary) != "agent-identity" ||
		filepath.Base(o.WorkerBinary) != "agent-service" || filepath.Dir(o.IdentityBinary) != filepath.Dir(o.WorkerBinary) {
		return plan, domain.Fail(domain.Invalid, "both services must use this user's native manager paths and the paired identity/runner binaries")
	}
	plan.Accounts = []IdentityAccountPlan{{Name: o.User, UID: o.UID, GID: o.GID, Home: o.Home, Existing: true}}
	for _, path := range []string{o.StateDir, o.WorkerStateDir, o.SocketDir} {
		plan.Directories = append(plan.Directories, IdentityDirectoryPlan{Path: path, UID: o.UID, GID: o.GID, Mode: 0700})
	}
	plan.Notes = []string{
		"Both processes run as the existing installing user. No account, group, login shell, HOME permissions or supplementary membership is created or changed.",
		"The enrolled agent name and signed public profile choose the Foundation SSH identity; the local OS username need not match it.",
		"The installing user owns the whole worker, including its identity keys. Separate processes and state directories organize lifecycle, not isolation from that user's tools. Keep private state/configuration at 0700/0600 and IPC at 0700/0600.",
		"Installation requires explicit --apply as that user, never sudo, runuser, a system daemon or another account.",
	}
	if o.OS == "linux" {
		quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }
		unit := func(description, binary, configFile, workdir, path string) string {
			return "[Unit]\nDescription=Foundation " + description + " " + o.Name + "\n\n[Service]\nType=simple\n" +
				"ExecStart=" + quote(binary) + " --config " + quote(configFile) + " serve\n" +
				"WorkingDirectory=" + strings.ReplaceAll(workdir, "%", "%%") + "\nEnvironment=" + quote("HOME="+o.Home) + "\n" +
				"Environment=" + quote("PATH="+path) + "\nNoNewPrivileges=true\nUMask=0077\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=15\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n"
		}
		plan.Identity = Definition{Path: o.IdentityDefinition, Content: unit("identity", o.IdentityBinary, o.IdentityConfig, o.Home, "/usr/bin:/bin"),
			Commands: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", filepath.Base(o.IdentityDefinition)}}}
		plan.Worker = Definition{Path: o.WorkerDefinition, Content: unit("tool runner", o.WorkerBinary, o.WorkerConfig, o.WorkerStateDir,
			filepath.Join(o.Home, ".local", "bin")+":/usr/local/bin:/usr/bin:/bin"),
			Commands: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", filepath.Base(o.WorkerDefinition)}}}
		plan.Notes = append(plan.Notes, "The existing user's systemd manager and bus must already be available. Optional logout persistence is an explicit native prerequisite; installation never enables linger or starts a system user manager as root.")
		return plan, nil
	}
	escape := func(value string) string {
		var buffer bytes.Buffer
		_ = xml.EscapeText(&buffer, []byte(value))
		return buffer.String()
	}
	plist := func(label, binary, configFile, state, path string) string {
		return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n" +
			"<key>Label</key><string>" + escape(label) + "</string>\n" +
			"<key>ProgramArguments</key><array><string>" + escape(binary) + "</string><string>--config</string><string>" + escape(configFile) + "</string><string>serve</string></array>\n" +
			"<key>WorkingDirectory</key><string>" + escape(state) + "</string>\n<key>EnvironmentVariables</key><dict><key>HOME</key><string>" + escape(o.Home) +
			"</string><key>PATH</key><string>" + escape(path) + "</string></dict>\n<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>Umask</key><integer>63</integer>\n</dict></plist>\n"
	}
	target := "gui/" + strconv.Itoa(o.UID)
	plan.Identity = Definition{Path: o.IdentityDefinition, Content: plist("org.foundation.identity."+o.Name, o.IdentityBinary, o.IdentityConfig, o.Home, "/usr/bin:/bin"),
		Commands: [][]string{{"launchctl", "bootstrap", target, o.IdentityDefinition}}}
	plan.Worker = Definition{Path: o.WorkerDefinition, Content: plist("org.foundation.runner."+o.Name, o.WorkerBinary, o.WorkerConfig, o.WorkerStateDir,
		filepath.Join(o.Home, ".local", "bin")+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"),
		Commands: [][]string{{"launchctl", "bootstrap", target, o.WorkerDefinition}}}
	plan.Notes = append(plan.Notes, "Both macOS services are LaunchAgents in the existing user's GUI domain. A missing login domain is a prerequisite failure, never a reason to create a hidden user or install a system LaunchDaemon.")
	return plan, nil
}
