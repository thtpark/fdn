package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type currentUserFileFixture struct {
	*nativeIdentityServiceEnvironment
	user AccountUser
}

func (h *currentUserFileFixture) CurrentUser(context.Context) (AccountUser, error) {
	return h.user, nil
}

func (*currentUserFileFixture) RunnerDomain(IdentityServiceOptions) error { return nil }

func TestNativeWorkerServiceInstallerRejectsOtherUserBeforeManagerCalls(t *testing.T) {
	actual, err := currentIdentityServiceUser()
	if err != nil {
		t.Fatal(err)
	}
	o := identityOptions()
	if runtime.GOOS == "darwin" {
		o = darwinIdentityOptions()
	}
	oldHome := o.Home
	o.User, o.UID, o.GID, o.Home = actual.Username, int(actual.UID)+1, int(actual.GID), actual.Home
	for _, path := range []*string{&o.IdentityConfig, &o.WorkerConfig, &o.StateDir, &o.WorkerStateDir, &o.SocketDir, &o.IdentityDefinition, &o.WorkerDefinition} {
		*path = strings.ReplaceAll(*path, oldHome, o.Home)
	}
	record := &identityInstallRecorder{t: t, os: o.OS}
	installer, err := NewNativeIdentityServiceInstaller(o, record)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Conflict
	if os.Geteuid() == 0 {
		want = domain.Denied
	}
	if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != want || len(record.calls) != 0 {
		t.Fatal("native installer selected another OS user instead of the installing user", err)
	}
}

func TestCurrentUserServiceFilesPreserveExistingHomeAndLegacyEvidence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("actual-file fixture represents an existing non-root installing user")
	}
	root := identityInstallTestDir(t)
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0755); err != nil {
		t.Fatal(err)
	}
	o := identityOptions()
	o.UID, o.GID, o.Home = os.Geteuid(), os.Getegid(), home
	o.IdentityConfig, o.WorkerConfig = filepath.Join(home, "config", "identity.json"), filepath.Join(home, "config", "agent.json")
	o.StateDir, o.WorkerStateDir, o.SocketDir = filepath.Join(home, "identity"), filepath.Join(home, "worker"), filepath.Join(home, "ipc")
	definitions := filepath.Join(home, ".config", "systemd", "user")
	o.IdentityDefinition = filepath.Join(definitions, "fdn-identity-agent1.service")
	o.WorkerDefinition = filepath.Join(definitions, "fdn-worker-agent1.service")
	binaries := filepath.Join(home, "bin")
	if err := os.Mkdir(binaries, 0755); err != nil {
		t.Fatal(err)
	}
	o.IdentityBinary, o.WorkerBinary = filepath.Join(binaries, "agent-identity"), filepath.Join(binaries, "agent-service")
	for _, path := range []string{o.IdentityBinary, o.WorkerBinary} {
		if err := os.WriteFile(path, []byte("synthetic executable fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(home, "config"), o.StateDir, o.WorkerStateDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	retained := []byte("retained identity and uncertain runner evidence")
	evidence := []string{filepath.Join(o.StateDir, "device.json"), filepath.Join(o.WorkerStateDir, "retained-journal")}
	for _, path := range evidence {
		if err := os.WriteFile(path, retained, 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity := config.IdentityService{StateDir: o.StateDir, Socket: filepath.Join(o.SocketDir, "i.sock")}
	worker := config.DefaultWorker(o.WorkerStateDir)
	worker.IdentitySocket = identity.Socket
	if err := config.Write(o.IdentityConfig, identity); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(o.WorkerConfig, worker); err != nil {
		t.Fatal(err)
	}
	host := &currentUserFileFixture{nativeIdentityServiceEnvironment: &nativeIdentityServiceEnvironment{},
		user: AccountUser{Username: o.User, UID: uint32(o.UID), GID: uint32(o.GID), Home: home}}
	record := &identityInstallRecorder{t: t, os: o.OS}
	installer, err := NewIdentityServiceInstaller(o, host, record)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		status, err := installer.Install(context.Background(), true)
		if err != nil || !status.Identity.Running || !status.Worker.Running {
			t.Fatal("existing-user file layout required root, another account or managed account markers", status, err)
		}
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("installation changed the existing user's HOME permissions", err)
	}
	for _, path := range evidence {
		if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, retained) {
			t.Fatal("installation changed private keys or retained runner state", err)
		}
	}
	calls := len(record.calls)
	if err := os.Chmod(o.SocketDir, 0710); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(context.Background(), true); domain.AsFault(err).Code != domain.Denied || len(record.calls) != calls {
		t.Fatal("legacy shared IPC was adopted or crossed a manager boundary", err)
	}
	info, err = os.Stat(o.SocketDir)
	if err != nil || info.Mode().Perm() != 0710 {
		t.Fatal("legacy IPC permissions were silently rewritten", err)
	}
}
