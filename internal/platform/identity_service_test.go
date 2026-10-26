package platform

import (
	"encoding/xml"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func identityOptions() IdentityServiceOptions {
	return IdentityServiceOptions{OS: "linux", Name: "agent1", User: "alice", UID: 1102, GID: 100, Home: "/home/alice",
		IdentityBinary: "/opt/fdn/agent-identity", WorkerBinary: "/opt/fdn/agent-service",
		IdentityConfig: "/home/alice/.config/fdn/identity.json", WorkerConfig: "/home/alice/.config/fdn/agent.json",
		StateDir: "/home/alice/.local/state/fdn-identity", WorkerStateDir: "/home/alice/.local/state/fdn",
		SocketDir:          "/home/alice/.local/state/fdn-ipc",
		IdentityDefinition: "/home/alice/.config/systemd/user/fdn-identity-agent1.service",
		WorkerDefinition:   "/home/alice/.config/systemd/user/fdn-worker-agent1.service"}
}

func darwinIdentityOptions() IdentityServiceOptions {
	o := identityOptions()
	o.OS, o.Home = "darwin", "/Users/alice"
	o.IdentityConfig, o.WorkerConfig = o.Home+"/.config/fdn/identity.json", o.Home+"/.config/fdn/agent.json"
	o.StateDir, o.WorkerStateDir, o.SocketDir = o.Home+"/.local/state/fdn-identity", o.Home+"/.local/state/fdn", o.Home+"/.local/state/fdn-ipc"
	o.IdentityDefinition = o.Home + "/Library/LaunchAgents/org.foundation.identity.agent1.plist"
	o.WorkerDefinition = o.Home + "/Library/LaunchAgents/org.foundation.runner.agent1.plist"
	return o
}

func TestIdentityServicesUseOneExistingUserIndependentOfAgentName(t *testing.T) {
	for _, o := range []IdentityServiceOptions{identityOptions(), darwinIdentityOptions()} {
		t.Run(o.OS, func(t *testing.T) {
			plan, err := RenderIdentityServices(o)
			if err != nil || len(plan.Accounts) != 1 || plan.Accounts[0].Name != "alice" ||
				!plan.Accounts[0].Existing || plan.Accounts[0].UID != o.UID || o.User == o.Name {
				t.Fatal("plan did not retain the existing, independently named OS user", plan, err)
			}
			for _, directory := range plan.Directories {
				if directory.UID != o.UID || directory.GID != o.GID || directory.Mode != 0700 || directory.Path == o.Home {
					t.Fatal("plan attempted another account/group or changed HOME permissions", directory)
				}
			}
			for _, definition := range []Definition{plan.Identity, plan.Worker} {
				for _, forbidden := range []string{"User=", "Group=", "SupplementaryGroups=", "ProtectHome=true", "<key>UserName</key>", "<key>GroupName</key>", "/LaunchDaemons/"} {
					if strings.Contains(definition.Content, forbidden) || strings.Contains(definition.Path, forbidden) {
						t.Fatal("user services retained a separate/system account requirement", forbidden)
					}
				}
				if o.OS == "linux" {
					if !reflect.DeepEqual(definition.Commands[0], []string{"systemctl", "--user", "daemon-reload"}) ||
						!strings.Contains(definition.Content, "WantedBy=default.target") || !strings.Contains(definition.Content, "UMask=0077") {
						t.Fatal("service did not use the existing user's systemd manager")
					}
				} else {
					decoder := xml.NewDecoder(strings.NewReader(definition.Content))
					for {
						_, err := decoder.Token()
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Fatal("invalid user LaunchAgent", err)
						}
					}
					if !reflect.DeepEqual(definition.Commands[0], []string{"launchctl", "bootstrap", "gui/1102", definition.Path}) {
						t.Fatal("identity and runner did not share the existing GUI domain")
					}
				}
			}
		})
	}
}

func TestIdentityServiceUserDefaultsIgnoreClaimedEnvironment(t *testing.T) {
	actual, err := currentIdentityServiceUser()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USER", "agent1")
	o, err := ResolveIdentityServiceUser(IdentityServiceOptions{})
	if err != nil || o.User != actual.Username || o.UID != os.Geteuid() || o.GID != os.Getegid() || o.Home != actual.Home {
		t.Fatal("service defaults trusted an agent name or environment instead of the installing OS user", err)
	}
}

func TestIdentityNativePlanningRejectsLegacyAccountsAndUnsafePaths(t *testing.T) {
	cases := map[string]func(*IdentityServiceOptions){
		"legacy-identity":     func(o *IdentityServiceOptions) { o.IdentityUser = "fdni_agent1" },
		"legacy-worker":       func(o *IdentityServiceOptions) { o.WorkerUID = o.UID },
		"root":                func(o *IdentityServiceOptions) { o.UID = 0 },
		"foreign-system-unit": func(o *IdentityServiceOptions) { o.IdentityDefinition = "/etc/systemd/system/identity.service" },
		"foreign-config":      func(o *IdentityServiceOptions) { o.IdentityConfig = "/another/home/identity.json" },
		"arbitrary-binary":    func(o *IdentityServiceOptions) { o.IdentityBinary = "/usr/bin/anything" },
		"oversized-uid":       func(o *IdentityServiceOptions) { o.UID = 1 << 31 },
		"shared-state":        func(o *IdentityServiceOptions) { o.WorkerStateDir = o.StateDir },
		"socket-in-state":     func(o *IdentityServiceOptions) { o.SocketDir = o.StateDir + "/ipc" },
		"shared-config":       func(o *IdentityServiceOptions) { o.WorkerConfig = o.IdentityConfig },
		"injected-path":       func(o *IdentityServiceOptions) { o.IdentityBinary += "\nExecStart=bad" },
		"injected-user":       func(o *IdentityServiceOptions) { o.User += "\n" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := identityOptions()
			mutate(&o)
			if _, err := RenderIdentityServices(o); err == nil {
				t.Fatal("unsafe or legacy service plan was accepted")
			}
		})
	}
	o := identityOptions()
	o.Home = "/home/Alice 100%"
	for _, path := range []*string{&o.IdentityConfig, &o.WorkerConfig, &o.StateDir, &o.WorkerStateDir, &o.SocketDir, &o.IdentityDefinition, &o.WorkerDefinition} {
		*path = strings.ReplaceAll(*path, "/home/alice", o.Home)
	}
	o.User = "Alice.Local"
	plan, err := RenderIdentityServices(o)
	if err != nil || !strings.Contains(plan.Identity.Content, "WorkingDirectory=/home/Alice 100%%\n") {
		t.Fatal("existing local usernames/HOMEs were treated as agent SSH usernames or shell fragments", err)
	}
}
