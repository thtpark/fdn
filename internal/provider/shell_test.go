package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedShellCommandsPreserveProfilesAndFollowConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with spaces")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, ".profile")
	existing := []byte("# keep existing settings\nexport MY_SETTING=preserved\n")
	if err := os.WriteFile(profile, existing, 0644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "private config's.json")
	cli := filepath.Join(t.TempDir(), "agent-cli")
	first, err := InstallShellCommands(home, config, cli, "/bin/bash")
	if err != nil || !first.Changed || first.Profile != profile || len(first.Commands) != 2 {
		t.Fatal(first, err)
	}
	body, err := os.ReadFile(profile)
	if err != nil || !bytes.HasPrefix(body, existing) || bytes.Count(body, []byte(pathStart)) != 1 {
		t.Fatal("existing login settings were not preserved", err)
	}
	for _, name := range []string{"codex", "copilot"} {
		file := filepath.Join(bin, name)
		data, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(data), "provider exec --provider "+name+" -- \"$@\"") ||
			!strings.Contains(string(data), shellQuote(config)) || !strings.Contains(string(data), shellQuote(cli)) {
			t.Fatal("launcher does not resolve through the configured managed provider", name, err)
		}
		if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("launcher permissions are not private/executable", err)
		}
		if strings.Contains(string(data), "versions/") {
			t.Fatal("shell command was pinned to an obsolete provider version directory")
		}
	}
	second, err := InstallShellCommands(home, config, cli, "/bin/bash")
	if err != nil || second.Changed {
		t.Fatal("shell installation is not idempotent", second, err)
	}
	if _, err := InstallShellCommands(home, filepath.Join(home, "another-identity.json"), cli, "/bin/bash"); err == nil {
		t.Fatal("silently rebound a shell launcher to another configuration")
	}
	if _, err := InstallShellCommands(home, config, cli+"-new", "/bin/bash"); err != nil {
		t.Fatal("could not refresh the managed CLI path", err)
	}
	if info, err := os.Stat(profile); err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("login profile permissions changed", err)
	}
}

func TestShellCommandsRejectUnmanagedPathsAndAmbiguousProfiles(t *testing.T) {
	for _, kind := range []string{"unmanaged", "empty", "symlink", "profile-symlink", "profile-block"} {
		t.Run(kind, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			bin := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(bin, "codex")
			switch kind {
			case "unmanaged", "empty":
				data := []byte("# user's command\n")
				if kind == "empty" {
					data = nil
				}
				if err := os.WriteFile(target, data, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("/usr/bin/true", target); err != nil {
					t.Fatal(err)
				}
			case "profile-symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "profile"), filepath.Join(home, ".profile")); err != nil {
					t.Fatal(err)
				}
			case "profile-block":
				if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(pathStart+"\nnot a complete managed block"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := InstallShellCommands(home, filepath.Join(home, "agent.json"), "/usr/bin/agent-cli", "/bin/bash"); err == nil {
				t.Fatal("unmanaged command/profile was accepted")
			}
			if _, err := os.Lstat(filepath.Join(bin, "copilot")); !os.IsNotExist(err) {
				t.Fatal("preflight failure installed a partial command set")
			}
		})
	}
}

func TestShellProfilePrecedence(t *testing.T) {
	for shell, preferred := range map[string]string{"/bin/bash": ".bash_profile", "/bin/zsh": ".zprofile", "/bin/sh": ".profile"} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("# existing bash profile"), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := InstallShellCommands(home, filepath.Join(home, "agent.json"), "/usr/bin/agent-cli", shell)
		if err != nil || filepath.Base(result.Profile) != preferred {
			t.Fatal("wrong login startup file", shell, result.Profile, err)
		}
	}
	if _, err := InstallShellCommands("/", "/config", "/usr/bin/agent-cli", "/bin/sh"); err == nil {
		t.Fatal("filesystem root accepted as an account HOME")
	}
}
