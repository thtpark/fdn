package platform

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

// IdentityServiceOptions describes two OS accounts, never a same-UID worker
// service. Rendering is a plan only: no user, daemon or GUI domain is changed.
type IdentityServiceOptions struct {
	OS                 string `json:"os"`
	Name               string `json:"name"`
	IdentityUser       string `json:"identity_user"`
	IdentityUID        int    `json:"identity_uid"`
	IdentityGID        int    `json:"identity_gid"`
	WorkerUser         string `json:"worker_user"`
	WorkerUID          int    `json:"worker_uid"`
	WorkerGID          int    `json:"worker_gid"`
	IdentityBinary     string `json:"identity_binary"`
	WorkerBinary       string `json:"worker_binary"`
	IdentityConfig     string `json:"identity_config"`
	WorkerConfig       string `json:"worker_config"`
	StateDir           string `json:"state_dir"`
	WorkerStateDir     string `json:"worker_state_dir"`
	IdentityHome       string `json:"identity_home"`
	WorkerHome         string `json:"worker_home"`
	SocketDir          string `json:"socket_dir"`
	IdentityDefinition string `json:"identity_definition"`
	WorkerDefinition   string `json:"worker_definition"`
}

type IdentityGroupPlan struct {
	Name string `json:"name"`
	GID  int    `json:"gid"`
}

type IdentityAccountPlan struct {
	Name                string              `json:"name"`
	UID                 int                 `json:"uid"`
	GID                 int                 `json:"gid"`
	Home                string              `json:"home"`
	System              bool                `json:"system"`
	GUI                 bool                `json:"gui"`
	Marker              string              `json:"marker"`
	Shell               string              `json:"shell"`
	SupplementaryGroups []IdentityGroupPlan `json:"supplementary_groups,omitempty"`
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
	if (o.OS != "linux" && o.OS != "darwin") || !username.MatchString(o.Name) || !username.MatchString(o.IdentityUser) || !username.MatchString(o.WorkerUser) ||
		o.IdentityUser == o.WorkerUser || o.IdentityUser == "root" || o.WorkerUser == "root" || o.IdentityUID <= 0 || o.WorkerUID <= 0 ||
		o.IdentityUID == o.WorkerUID || o.IdentityGID <= 0 || o.WorkerGID <= 0 || o.IdentityGID == o.WorkerGID ||
		o.IdentityUID > 1<<31-1 || o.WorkerUID > 1<<31-1 || o.IdentityGID > 1<<31-1 || o.WorkerGID > 1<<31-1 {
		return plan, domain.Fail(domain.Invalid, "native identity services require distinct non-root identity and tool-runner accounts on Linux or macOS")
	}
	for _, path := range []string{o.IdentityBinary, o.WorkerBinary, o.IdentityConfig, o.WorkerConfig, o.StateDir, o.WorkerStateDir,
		o.IdentityHome, o.WorkerHome, o.SocketDir, o.IdentityDefinition, o.WorkerDefinition} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n\t") {
			return plan, domain.Fail(domain.Invalid, "identity service plan needs clean absolute single-line paths")
		}
	}
	if !identityPathWithin(o.IdentityHome, o.IdentityConfig) || !identityPathWithin(o.IdentityHome, o.StateDir) ||
		!identityPathWithin(o.WorkerHome, o.WorkerConfig) || !identityPathWithin(o.WorkerHome, o.WorkerStateDir) ||
		identityPathWithin(o.IdentityHome, o.SocketDir) || identityPathWithin(o.SocketDir, o.IdentityHome) ||
		identityPathWithin(o.IdentityHome, o.WorkerHome) || identityPathWithin(o.WorkerHome, o.IdentityHome) ||
		identityPathWithin(o.StateDir, o.SocketDir) || identityPathWithin(o.SocketDir, o.StateDir) ||
		identityPathWithin(o.StateDir, o.WorkerStateDir) || identityPathWithin(o.WorkerStateDir, o.StateDir) ||
		identityPathWithin(o.WorkerHome, o.StateDir) || identityPathWithin(o.IdentityHome, o.WorkerStateDir) || o.IdentityDefinition == o.WorkerDefinition {
		return plan, domain.Fail(domain.Denied, "private identity configuration/state must be separate from runner state/home and the shared IPC directory")
	}
	identityDefinition := "/etc/systemd/system/fdn-identity-" + o.Name + ".service"
	workerDefinition := filepath.Join(o.WorkerHome, ".config", "systemd", "user", "fdn-worker-"+o.Name+".service")
	if o.OS == "darwin" {
		identityDefinition = "/Library/LaunchDaemons/org.foundation.identity." + o.Name + ".plist"
		workerDefinition = filepath.Join(o.WorkerHome, "Library", "LaunchAgents", "org.foundation.runner."+o.Name+".plist")
	}
	if o.IdentityDefinition != identityDefinition || o.WorkerDefinition != workerDefinition || filepath.Base(o.IdentityBinary) != "agent-identity" ||
		filepath.Base(o.WorkerBinary) != "agent-service" || filepath.Dir(o.IdentityBinary) != filepath.Dir(o.WorkerBinary) {
		return plan, domain.Fail(domain.Invalid, "service definitions must use their per-agent native manager locations and the paired immutable identity/runner binaries")
	}
	identityShell, workerShell := "/usr/sbin/nologin", "/bin/sh"
	if o.OS == "darwin" {
		identityShell, workerShell = "/usr/bin/false", "/bin/zsh"
	}
	plan.Accounts = []IdentityAccountPlan{
		{Name: o.IdentityUser, UID: o.IdentityUID, GID: o.IdentityGID, Home: o.IdentityHome, System: true, Marker: "fdn-identity=" + o.Name, Shell: identityShell,
			SupplementaryGroups: []IdentityGroupPlan{{Name: o.WorkerUser, GID: o.WorkerGID}}},
		{Name: o.WorkerUser, UID: o.WorkerUID, GID: o.WorkerGID, Home: o.WorkerHome, GUI: o.OS == "darwin", Marker: "fdn-runner=" + o.Name, Shell: workerShell},
	}
	plan.Directories = []IdentityDirectoryPlan{
		{Path: o.StateDir, UID: o.IdentityUID, GID: o.IdentityGID, Mode: 0700},
		{Path: o.SocketDir, UID: o.IdentityUID, GID: o.WorkerGID, Mode: 0710},
		{Path: o.WorkerStateDir, UID: o.WorkerUID, GID: o.WorkerGID, Mode: 0700},
	}
	for _, home := range []IdentityDirectoryPlan{{Path: o.IdentityHome, UID: o.IdentityUID, GID: o.IdentityGID, Mode: 0700}, {Path: o.WorkerHome, UID: o.WorkerUID, GID: o.WorkerGID, Mode: 0700}} {
		if home.Path != o.StateDir && home.Path != o.WorkerStateDir {
			plan.Directories = append(plan.Directories, home)
		}
	}
	plan.Notes = []string{
		"Administrator provisions the exact named accounts, UIDs, primary groups, private HOMEs and managed account comment/RealName markers explicitly; missing or mismatched accounts remain a prerequisite, never adoption or successful installation. This plan executes nothing.",
		"Private identity configuration, keys and device certificates stay identity-UID-owned 0600 under 0700 state; worker configuration contains only the Unix socket and public identity reference.",
		"Provision the identity account's explicit supplementary membership in the worker group before native serving, so its unprivileged IPC chgrp succeeds. Linux also declares SupplementaryGroups and macOS LaunchDaemon sets GroupName to the worker group. No reverse membership grants the worker access to identity state; the IPC parent remains identity-owned 0710 and kernel peer UID/GID checks still apply.",
		"No root generic-command helper, shared bearer credential, identity-home tool runner, or worker-readable signing key is installed.",
	}
	if o.OS == "linux" {
		if identityPathWithin("/home", o.IdentityHome) || identityPathWithin("/root", o.IdentityHome) || identityPathWithin("/run/user", o.IdentityHome) {
			return IdentityServicePlan{}, domain.Fail(domain.Invalid, "Linux system identity HOME must be outside user homes hidden by ProtectHome")
		}
		quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }
		identity := "[Unit]\nDescription=Foundation isolated identity " + o.Name + "\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n" +
			"User=" + o.IdentityUser + "\nGroup=" + o.IdentityUser + "\nSupplementaryGroups=" + o.WorkerUser + "\n" +
			"ExecStart=" + quote(o.IdentityBinary) + " --config " + quote(o.IdentityConfig) + " serve\n" +
			"WorkingDirectory=" + strings.ReplaceAll(o.IdentityHome, "%", "%%") + "\nEnvironment=" + quote("HOME="+o.IdentityHome) + "\nEnvironment=PATH=/usr/bin:/bin\n" +
			"ProtectSystem=strict\nProtectHome=true\nReadWritePaths=" + quote(o.IdentityHome) + " " + quote(o.SocketDir) + "\n" +
			"PrivateTmp=true\nNoNewPrivileges=true\nUMask=0077\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=15\nKillMode=control-group\n\n[Install]\nWantedBy=multi-user.target\n"
		worker := "[Unit]\nDescription=Foundation tool runner " + o.Name + "\n\n[Service]\nType=simple\n" +
			"ExecStart=" + quote(o.WorkerBinary) + " --config " + quote(o.WorkerConfig) + " serve\n" +
			"WorkingDirectory=" + strings.ReplaceAll(o.WorkerStateDir, "%", "%%") + "\nEnvironment=" + quote("HOME="+o.WorkerHome) + "\n" +
			"Environment=" + quote("PATH="+filepath.Join(o.WorkerHome, ".local", "bin")+":/usr/local/bin:/usr/bin:/bin") + "\n" +
			"NoNewPrivileges=true\nUMask=0077\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=15\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n"
		plan.Identity = Definition{Path: o.IdentityDefinition, Content: identity,
			Commands: [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "--now", filepath.Base(o.IdentityDefinition)}}}
		plan.Worker = Definition{Path: o.WorkerDefinition, Content: worker,
			Commands: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", filepath.Base(o.WorkerDefinition)}}}
		plan.Notes = append(plan.Notes, "Create the identity account as a no-login Linux system user. Provision runner user-manager persistence separately; execute --user registration as that runner, not root or the identity account.")
		return plan, nil
	}
	escape := func(value string) string {
		var buffer bytes.Buffer
		_ = xml.EscapeText(&buffer, []byte(value))
		return buffer.String()
	}
	plist := func(label, binary, configFile, home, state, account string) string {
		content := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>" + escape(label) + "</string>\n"
		if account != "" {
			content += "<key>UserName</key><string>" + escape(account) + "</string><key>GroupName</key><string>" + escape(o.WorkerUser) + "</string>\n"
		}
		content += "<key>ProgramArguments</key><array><string>" + escape(binary) + "</string><string>--config</string><string>" + escape(configFile) + "</string><string>serve</string></array>\n" +
			"<key>WorkingDirectory</key><string>" + escape(state) + "</string>\n<key>EnvironmentVariables</key><dict><key>HOME</key><string>" + escape(home) + "</string><key>PATH</key><string>"
		if account == "" {
			content += escape(filepath.Join(home, ".local", "bin") + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
		} else {
			content += "/usr/bin:/bin"
		}
		return content + "</string></dict>\n<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>Umask</key><integer>63</integer>\n</dict></plist>\n"
	}
	plan.Identity = Definition{Path: o.IdentityDefinition,
		Content:  plist("org.foundation.identity."+o.Name, o.IdentityBinary, o.IdentityConfig, o.IdentityHome, o.IdentityHome, o.IdentityUser),
		Commands: [][]string{{"launchctl", "bootstrap", "system", o.IdentityDefinition}}}
	plan.Worker = Definition{Path: o.WorkerDefinition,
		Content:  plist("org.foundation.runner."+o.Name, o.WorkerBinary, o.WorkerConfig, o.WorkerHome, o.WorkerStateDir, ""),
		Commands: [][]string{{"launchctl", "bootstrap", fmt.Sprintf("gui/%d", o.WorkerUID), o.WorkerDefinition}}}
	plan.Notes = append(plan.Notes, "Provision a dedicated hidden native macOS identity account and a system LaunchDaemon. The provider/tool runner is a separate account's GUI LaunchAgent; daemon and GUI/TCC domains are not interchangeable.")
	return plan, nil
}
