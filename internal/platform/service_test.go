package platform

import (
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/process"
)

type recorder struct{ calls []process.Spec }

func (r *recorder) Run(_ context.Context, s process.Spec) error {
	r.calls = append(r.calls, s)
	return nil
}

func TestNativeServiceContractsWithoutHostChanges(t *testing.T) {
	root := t.TempDir()
	options := Options{OS: "linux", Role: "foundation", Binary: "/opt/fdn/fdn-service", ConfigFile: "/var/lib/fdn/server.json", StateDir: "/var/lib/fdn", Home: "/var/lib/fdn", User: "fdn", Definition: filepath.Join(root, "fdn-foundation.service")}
	definition, err := Render(options)
	if err != nil || !strings.Contains(definition.Content, "User=fdn") || !strings.Contains(definition.Content, "KillMode=control-group") {
		t.Fatal(definition, err)
	}
	if !strings.Contains(definition.Content, "\nWorkingDirectory=/var/lib/fdn\n") {
		t.Fatal("systemd WorkingDirectory must be a path value, not a quoted argument")
	}
	options.OS = "darwin"
	options.Role = "agent"
	options.User = "runner"
	options.Binary = "/opt/fdn/a&b/agent-service"
	options.Definition = filepath.Join(root, "agent.plist")
	definition, err = Render(options)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(definition.Content))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	options.OS = runtime.GOOS
	options.Binary = "/opt/fdn/agent-service"
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1001")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1001/bus")
	t.Setenv("FDN_DEPLOYMENT_SECRET", "must-not-be-inherited")
	record := &recorder{}
	if err := Install(context.Background(), options, record); err != nil {
		t.Fatal(err)
	}
	if len(record.calls) == 0 {
		t.Fatal("manager not invoked")
	}
	for _, call := range record.calls {
		for _, variable := range call.Env {
			if strings.HasPrefix(variable, "FDN_DEPLOYMENT_SECRET=") {
				t.Fatal("service manager inherited an unrelated secret")
			}
		}
		if runtime.GOOS == "linux" {
			for _, required := range []string{"XDG_RUNTIME_DIR=/run/user/1001", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus"} {
				if !slices.Contains(call.Env, required) {
					t.Fatal("user service manager lost its bus environment", required)
				}
			}
		}
	}
	options.User = "root\nExecStart=bad"
	if _, err := Render(options); err == nil {
		t.Fatal("service injection accepted")
	}
}

func TestSystemdParsesRenderedUnitWithoutRegisteringServices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd parser is Linux-only")
	}
	parser, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	root := t.TempDir()
	state := filepath.Join(root, "state with spaces")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	options := Options{OS: "linux", Role: "agent", Binary: "/bin/true", ConfigFile: filepath.Join(state, "agent.json"),
		StateDir: state, Home: state, User: "runner", Definition: filepath.Join(root, "fdn-parser-regression.service")}
	definition, err := Render(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition.Path, []byte(definition.Content), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(parser, "verify", definition.Path).CombinedOutput()
	if err != nil {
		t.Fatalf("rendered service is rejected by the native parser: %v\n%s", err, output)
	}
	options.StateDir = filepath.Join(state, "100%literal")
	definition, err = Render(options)
	if err != nil || !strings.Contains(definition.Content, "WorkingDirectory="+strings.ReplaceAll(options.StateDir, "%", "%%")+"\n") {
		t.Fatal("literal path percent sign was interpreted as a systemd specifier", err)
	}
}
