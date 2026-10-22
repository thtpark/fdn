package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type screenIdentityInstallHost struct {
	*identityInstallHost
	identity config.IdentityService
	worker   config.Worker
}

func (h *screenIdentityInstallHost) ValidateLayout(o IdentityServiceOptions) error {
	return validateIdentityServiceConfiguration(o, h.identity, h.worker)
}

func TestIdentityNativeScreenConfigurationAllowsRunnerAuthorityForLifecycle(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the real-file recording fixture uses the current non-root UID; synthetic UID 1004 metadata is tested without root")
	}
	root := identityInstallTestDir(t)
	authority := filepath.Join(root, "authority")
	if err := os.WriteFile(authority, []byte("synthetic Xauthority"), 0600); err != nil {
		t.Fatal(err)
	}
	o := identityOptions()
	o.WorkerUID, o.WorkerGID = os.Geteuid(), os.Geteuid()
	if o.WorkerUID == o.IdentityUID {
		o.IdentityUID++
	}
	if o.WorkerGID == o.IdentityGID {
		o.IdentityGID++
	}
	identity := config.IdentityService{StateDir: o.StateDir, Socket: filepath.Join(o.SocketDir, "i.sock"), WorkerUID: o.WorkerUID, WorkerGID: o.WorkerGID}
	worker := config.DefaultWorker(o.WorkerStateDir)
	worker.IdentitySocket = identity.Socket
	worker.Screens = []config.Screen{{ID: "runner", Name: "Runner screen", Scope: "screen", Width: 1440, Height: 900,
		Display: ":0.0", XAuthority: authority, VNCAddress: "127.0.0.1:5901", VNCVerified: true}}
	host := &screenIdentityInstallHost{identityInstallHost: identityInstallFixture(o), identity: identity, worker: worker}
	record := &identityInstallRecorder{t: t, os: o.OS}
	installer, err := NewIdentityServiceInstaller(o, host, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderIdentityServices(o); err != nil {
		t.Fatal("screen-qualified service plan failed", err)
	}
	if status, err := installer.Status(context.Background()); err != nil || !status.Worker.Running {
		t.Fatal("screen-qualified runner status failed", status, err)
	}
	if status, err := installer.Install(context.Background(), true); err != nil || !status.Worker.Running {
		t.Fatal("screen-qualified runner installation failed", status, err)
	}
	before := len(record.calls)
	link := filepath.Join(root, "authority-link")
	if err := os.Symlink(authority, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, root} {
		host.worker.Screens[0].XAuthority = path
		if _, err := installer.Status(context.Background()); domain.AsFault(err).Code != domain.Denied || len(record.calls) != before {
			t.Fatal("unsafe screen configuration crossed the native status boundary", err)
		}
		if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != domain.Denied || len(record.calls) != before {
			t.Fatal("unsafe screen configuration crossed the native install boundary", err)
		}
	}
}

func TestIdentityNativeScreenConfigurationRejectsInspectorOwnedAuthority(t *testing.T) {
	root := identityInstallTestDir(t)
	authority := filepath.Join(root, "authority")
	if err := os.WriteFile(authority, []byte("synthetic Xauthority"), 0600); err != nil {
		t.Fatal(err)
	}
	o := identityOptions()
	o.WorkerUID, o.WorkerGID = 1004, 1004
	if o.WorkerUID == os.Geteuid() {
		o.WorkerUID++
		o.WorkerGID++
	}
	identity := config.IdentityService{StateDir: o.StateDir, Socket: filepath.Join(o.SocketDir, "i.sock"), WorkerUID: o.WorkerUID, WorkerGID: o.WorkerGID}
	worker := config.DefaultWorker(o.WorkerStateDir)
	worker.IdentitySocket = identity.Socket
	worker.Screens = []config.Screen{{ID: "runner", Name: "Runner screen", Scope: "screen", Width: 1440, Height: 900,
		Display: ":0.0", XAuthority: authority, VNCAddress: "127.0.0.1:5901", VNCVerified: true}}
	if err := worker.Validate(); err != nil {
		t.Fatal("fixture must be valid for its actual inspecting UID", err)
	}
	if err := validateIdentityServiceConfiguration(o, identity, worker); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native helper used the inspector's UID instead of the bound runner UID", err)
	}
}
