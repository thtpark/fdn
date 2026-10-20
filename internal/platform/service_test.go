package platform

import (
	"context"
	"encoding/xml"
	"io"
	"path/filepath"
	"runtime"
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
	record := &recorder{}
	if err := Install(context.Background(), options, record); err != nil {
		t.Fatal(err)
	}
	if len(record.calls) == 0 {
		t.Fatal("manager not invoked")
	}
	options.User = "root\nExecStart=bad"
	if _, err := Render(options); err == nil {
		t.Fatal("service injection accepted")
	}
}
