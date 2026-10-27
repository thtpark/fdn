package distribution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentScriptsDoNotInventExternalToolDirectories(t *testing.T) {
	root, _ := developmentFixture(t)
	names := []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CODEX_HOME", "GH_CONFIG_DIR", "CLOUDSDK_CONFIG"}
	command := exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		keep := true
		for _, name := range names {
			if key == name {
				keep = false
				break
			}
		}
		if keep {
			command.Env = append(command.Env, value)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(string(output), err)
	}
	for _, name := range names {
		if !strings.Contains(string(output), name+"=unset") {
			t.Fatal("source runner invented a tool override", name, string(output))
		}
	}
}

func TestDevelopmentScriptsIgnoreOtherLayoutsWithoutChangingThem(t *testing.T) {
	for _, name := range []string{".config/fdn/agent.json", ".config/fdn/identity.json", ".state/identity/device.json"} {
		t.Run(name, func(t *testing.T) {
			root, _ := developmentFixture(t)
			legacy := filepath.Join(root, ".local", filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte("retained fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatal("unrelated files blocked the current source runner", string(output), err)
			}
			if data, err := os.ReadFile(legacy); err != nil || string(data) != "retained fixture" {
				t.Fatal("unrelated files were modified", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".local", ".foundation", "config", "identity.json")); err != nil {
				t.Fatal("current source configuration was not initialized", err)
			}
		})
	}
}

func TestDevelopmentScriptsDoNotAdoptPermissiveFoundationRoot(t *testing.T) {
	root, _ := developmentFixture(t)
	foundation := filepath.Join(root, ".local", ".foundation")
	if err := os.MkdirAll(foundation, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foundation, 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 4 ||
		!strings.Contains(string(output), "Foundation directory must be private") {
		t.Fatal("permissive root was adopted", string(output), err)
	}
	if info, err := os.Stat(foundation); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("existing directory permissions changed", err)
	}
	if entries, err := os.ReadDir(foundation); err != nil || len(entries) != 0 {
		t.Fatal("unsafe root received partial configuration", err)
	}
}
