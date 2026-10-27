package distribution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func developmentFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-cli.sh", "agent-service.sh", "agent-identity.sh", "dev-agent.sh"} {
		source, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "scripts", name), source, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
	}
	tools := filepath.Join(t.TempDir(), "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	mock := `#!/bin/sh
set -eu
if [ "$1" = env ]; then printf '/fixture/cache/%s\n' "$2"; exit; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; output=$1; break; fi
  shift
done
cat > "$output" <<'STUB'
#!/bin/sh
printf 'PID=%s\nHOME=%s\n' "$$" "$HOME"
for variable in XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME XDG_CACHE_HOME CODEX_HOME GH_CONFIG_DIR CLOUDSDK_CONFIG GH_TOKEN SSH_AUTH_SOCK; do
  if value=$(printenv "$variable"); then
    printf '%s=%s\n' "$variable" "$value"
  else
    printf '%s=unset\n' "$variable"
  fi
done
printf 'ARG=%s\n' "$@"
cat
exit "${FDN_TEST_EXIT:-0}"
STUB
chmod 0700 "$output"
if [ "${FDN_TEST_BUILD_FAIL:-}" = yes ]; then exit 19; fi
`
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CODEX_HOME", "GH_CONFIG_DIR", "CLOUDSDK_CONFIG", "GH_TOKEN", "SSH_AUTH_SOCK"} {
		t.Setenv(name, "fixture-preserved-"+name)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root, home
}

func TestDevelopmentScriptsIsolateAllEntrypointsAndPreserveArgumentsInputAndPID(t *testing.T) {
	for _, name := range []string{"agent-cli.sh", "agent-service.sh", "agent-identity.sh"} {
		t.Run(name, func(t *testing.T) {
			root, home := developmentFixture(t)
			command := exec.Command(filepath.Join(root, "scripts", name), "show", "literal argument with spaces")
			command.Stdin = strings.NewReader("bounded fixture input\n")
			var output strings.Builder
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			pid := command.Process.Pid
			if err := command.Wait(); err != nil {
				t.Fatal(output.String(), err)
			}
			for _, expected := range []string{
				"PID=" + strconv.Itoa(pid), "HOME=" + filepath.Join(root, ".local"),
				"ARG=literal argument with spaces", "bounded fixture input",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Fatal("source runner lost isolation/exec/arguments/stdin", expected, output.String())
				}
			}
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CODEX_HOME", "GH_CONFIG_DIR", "CLOUDSDK_CONFIG", "GH_TOKEN", "SSH_AUTH_SOCK"} {
				if !strings.Contains(output.String(), name+"=fixture-preserved-"+name) {
					t.Fatal("source runner changed an existing tool setting", name, output.String())
				}
			}
			identity := filepath.Join(root, ".local", ".foundation", "config", "identity.json")
			if info, err := os.Stat(identity); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("testing identity configuration is not private", err)
			}
			if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
				t.Fatal("source runner modified the real HOME", err)
			}
		})
	}
}

func TestDevelopmentScriptsPreserveExitStatusAndCleanFailedBuild(t *testing.T) {
	root, _ := developmentFixture(t)
	t.Setenv("FDN_TEST_EXIT", "37")
	command := exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 37 {
		t.Fatal("source runner lost native exit status", string(output), err)
	}
	t.Setenv("FDN_TEST_BUILD_FAIL", "yes")
	command = exec.Command(filepath.Join(root, "scripts", "agent-cli.sh"), "--help")
	output, err = command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 19 {
		t.Fatal("source runner swallowed a build failure", string(output), err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".local", ".foundation", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".build.") {
			t.Fatal("source runner retained a failed temporary build")
		}
	}
}

func TestDevelopmentScriptsRejectSymlinkedConfigurationAndRetainIdentity(t *testing.T) {
	root, home := developmentFixture(t)
	local := filepath.Join(root, ".local")
	if err := os.Mkdir(local, 0700); err != nil {
		t.Fatal(err)
	}
	foundation := filepath.Join(local, ".foundation")
	if err := os.Mkdir(foundation, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(foundation, "config")); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(root, "scripts", "agent-identity.sh"), "--help")
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "symlink") {
		t.Fatal("testing configuration escaped into another HOME", string(output), err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatal("unsafe testing configuration modified the real HOME", err)
	}
}
