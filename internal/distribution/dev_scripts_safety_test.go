package distribution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentScriptsRejectUnsafeBinaryDestinations(t *testing.T) {
	for _, kind := range []string{"symlink-file", "symlink-directory", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root, home := developmentFixture(t)
			bin := filepath.Join(root, ".local", ".bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(home, "retained")
			if err := os.WriteFile(sentinel, []byte("retained fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(bin, "agent-cli")
			switch kind {
			case "symlink-file":
				if err := os.Symlink(sentinel, destination); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				if err := os.Symlink(home, destination); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
			output, err := command.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 4 ||
				!strings.Contains(string(output), "development binary destination") {
				t.Fatal("unsafe binary destination was adopted", string(output), err)
			}
			content, err := os.ReadFile(sentinel)
			if err != nil || string(content) != "retained fixture" {
				t.Fatal("source runner changed an unrelated file", err)
			}
			if entries, err := os.ReadDir(home); err != nil || len(entries) != 1 {
				t.Fatal("source runner wrote outside its isolated HOME", err)
			}
		})
	}
}

func TestDevelopmentScriptsPreserveExistingIdentityConfiguration(t *testing.T) {
	root, _ := developmentFixture(t)
	configDir := filepath.Join(root, ".local", ".config", "fdn")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "identity.json")
	content := []byte("{\"state_dir\":\"retained-fixture\",\"socket\":\"retained-fixture.sock\"}\n")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(root, "scripts", "agent-identity.sh"), "--help")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(content) {
		t.Fatal("source runner replaced existing identity configuration", err)
	}
}
