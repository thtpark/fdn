package platform

import (
	"context"
	"encoding/xml"
	"io"
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
