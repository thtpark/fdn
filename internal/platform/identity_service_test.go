package platform

import (
	"encoding/xml"
	"io"
	"reflect"
	"strings"
	"testing"
)

func identityOptions() IdentityServiceOptions {
	return IdentityServiceOptions{OS: "linux", Name: "agent1", IdentityUser: "fdni_agent1", IdentityUID: 1101, IdentityGID: 1101,
		WorkerUser: "fdnw_agent1", WorkerUID: 1102, WorkerGID: 1102, IdentityBinary: "/opt/fdn/agent-identity", WorkerBinary: "/opt/fdn/agent-service",
		IdentityConfig: "/var/lib/fdn-identity/agent1/identity.json", WorkerConfig: "/home/fdnw_agent1/.config/fdn/agent.json",
		StateDir: "/var/lib/fdn-identity/agent1", WorkerStateDir: "/home/fdnw_agent1/.local/state/fdn", IdentityHome: "/var/lib/fdn-identity/agent1", WorkerHome: "/home/fdnw_agent1",
		SocketDir: "/run/fdn-agent1", IdentityDefinition: "/etc/systemd/system/fdn-identity-agent1.service", WorkerDefinition: "/home/fdnw_agent1/.config/systemd/user/fdn-worker-agent1.service"}
}

func TestIdentityServicePlanKeepsDaemonAndRunnerAccountsSeparate(t *testing.T) {
	options := identityOptions()
	plan, err := RenderIdentityServices(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"User=fdni_agent1\n", "Group=fdni_agent1\n", "SupplementaryGroups=fdnw_agent1\n", "--config \"/var/lib/fdn-identity/agent1/identity.json\" serve", "UMask=0077", "NoNewPrivileges=true", "KillMode=control-group", "ProtectHome=true"} {
		if !strings.Contains(plan.Identity.Content, required) {
			t.Fatal("identity system unit lost its isolation", required)
		}
	}
	if !strings.Contains(plan.Worker.Content, "agent-service") || strings.Contains(plan.Worker.Content, options.IdentityConfig) || strings.Contains(plan.Worker.Content, "agent-identity") {
		t.Fatal("tool-runner definition contains identity control or private configuration")
	}
	if !plan.Accounts[0].System || plan.Accounts[0].GUI || plan.Accounts[0].UID == plan.Accounts[1].UID {
		t.Fatal("dedicated identity system account is not separate")
	}
	if !reflect.DeepEqual(plan.Accounts[0].SupplementaryGroups, []IdentityGroupPlan{{Name: options.WorkerUser, GID: options.WorkerGID}}) || len(plan.Accounts[1].SupplementaryGroups) != 0 {
		t.Fatal("IPC publication requires explicit one-way group membership, never worker membership in the identity group")
	}
	if plan.Directories[0].Mode != 0700 || plan.Directories[1].Mode != 0710 || plan.Directories[1].GID != options.WorkerGID || plan.Directories[1].UID != options.IdentityUID {
		t.Fatal("private state/shared IPC ownership was not planned correctly")
	}
	if !reflect.DeepEqual(plan.Worker.Commands[0], []string{"systemctl", "--user", "daemon-reload"}) {
		t.Fatal("runner registration would use the identity/root service manager")
	}
	options.OS = "darwin"
	options.StateDir, options.IdentityHome = "/Library/Application Support/fdn-identity/agent1", "/Library/Application Support/fdn-identity/agent1"
	options.IdentityConfig = options.StateDir + "/identity.json"
	options.WorkerHome, options.WorkerStateDir = "/Users/fdnw_agent1", "/Users/fdnw_agent1/.local/state/fdn"
	options.WorkerConfig = "/Users/fdnw_agent1/.config/fdn/agent.json"
	options.IdentityDefinition, options.WorkerDefinition = "/Library/LaunchDaemons/org.foundation.identity.agent1.plist", "/Users/fdnw_agent1/Library/LaunchAgents/org.foundation.runner.agent1.plist"
	plan, err = RenderIdentityServices(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range []Definition{plan.Identity, plan.Worker} {
		decoder := xml.NewDecoder(strings.NewReader(definition.Content))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal("invalid native plist", err)
			}
		}
	}
	if !strings.Contains(plan.Identity.Content, "<key>UserName</key><string>fdni_agent1</string>") ||
		!strings.Contains(plan.Identity.Content, "<key>GroupName</key><string>fdnw_agent1</string>") ||
		!reflect.DeepEqual(plan.Accounts[0].SupplementaryGroups, []IdentityGroupPlan{{Name: options.WorkerUser, GID: options.WorkerGID}}) || strings.Contains(plan.Worker.Content, "<key>UserName</key>") {
		t.Fatal("macOS daemon and GUI runner domains were conflated")
	}
	if !reflect.DeepEqual(plan.Identity.Commands[0], []string{"launchctl", "bootstrap", "system", options.IdentityDefinition}) ||
		!reflect.DeepEqual(plan.Worker.Commands[0], []string{"launchctl", "bootstrap", "gui/1102", options.WorkerDefinition}) {
		t.Fatal("macOS plan used the caller UID rather than configured runner GUI UID")
	}
	if !plan.Accounts[1].GUI || strings.Contains(plan.Identity.Content, "/opt/homebrew") {
		t.Fatal("identity daemon inherited the provider GUI/tool environment")
	}
}

func TestIdentityNativePlanningRejectsUnsafeOrSameAccountPaths(t *testing.T) {
	cases := map[string]func(*IdentityServiceOptions){
		"same-uid":             func(o *IdentityServiceOptions) { o.WorkerUID = o.IdentityUID },
		"root-runner-alias":    func(o *IdentityServiceOptions) { o.WorkerUser = "root_alias"; o.WorkerUID = 0 },
		"root-identity-alias":  func(o *IdentityServiceOptions) { o.IdentityUser = "root_alias"; o.IdentityUID = 0 },
		"root":                 func(o *IdentityServiceOptions) { o.IdentityUID = 0 },
		"shared-private-group": func(o *IdentityServiceOptions) { o.WorkerGID = o.IdentityGID },
		"foreign-system-unit":  func(o *IdentityServiceOptions) { o.IdentityDefinition = "/etc/systemd/system/unrelated.service" },
		"arbitrary-binary":     func(o *IdentityServiceOptions) { o.IdentityBinary = "/usr/bin/anything" },
		"oversized-uid":        func(o *IdentityServiceOptions) { o.IdentityUID = 1 << 31 },
		"same-user":            func(o *IdentityServiceOptions) { o.WorkerUser = o.IdentityUser },
		"shared-state":         func(o *IdentityServiceOptions) { o.WorkerStateDir = o.StateDir },
		"socket-in-private":    func(o *IdentityServiceOptions) { o.SocketDir = o.StateDir + "/ipc" },
		"private-in-runner-home": func(o *IdentityServiceOptions) {
			o.StateDir = o.WorkerHome + "/private"
			o.IdentityConfig = o.StateDir + "/identity.json"
		},
		"injected-path": func(o *IdentityServiceOptions) { o.IdentityBinary += "\nExecStart=bad" },
		"injected-user": func(o *IdentityServiceOptions) { o.IdentityUser += "\n" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := identityOptions()
			mutate(&options)
			if _, err := RenderIdentityServices(options); err == nil {
				t.Fatal("unsafe native identity service plan was accepted")
			}
		})
	}
	options := identityOptions()
	options.StateDir = "/var/lib/fdn-identity/100%literal with spaces"
	options.IdentityHome = options.StateDir
	options.IdentityConfig = options.StateDir + "/identity.json"
	plan, err := RenderIdentityServices(options)
	if err != nil || !strings.Contains(plan.Identity.Content, "WorkingDirectory=/var/lib/fdn-identity/100%%literal with spaces\n") ||
		!strings.Contains(plan.Identity.Content, "--config \"/var/lib/fdn-identity/100%%literal with spaces/identity.json\"") {
		t.Fatal("systemd interpreted a path as specifiers or command arguments", err)
	}
}
