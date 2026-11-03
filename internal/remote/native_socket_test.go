package remote

import (
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

func TestTmuxSocketLocationPreservesCanonicalNamespaceAtKernelPathLimit(t *testing.T) {
	limit := len(syscall.RawSockaddrUnix{}.Path)
	for _, length := range []int{limit - 1, limit, limit + 64} {
		socket := "/" + strings.Repeat("d", length-len("/s.sock")-1) + "/s.sock"
		if len(socket) != length {
			t.Fatal("fixture did not bind the exact kernel byte threshold")
		}
		argument, directory := tmuxSocketLocation(socket)
		if length < limit {
			if argument != socket || directory != "" {
				t.Fatal("short native socket behavior changed", argument, directory)
			}
		} else if len(argument) >= limit || filepath.Join(directory, argument) != socket {
			t.Fatal("long socket moved or exceeded its native address namespace", argument, directory)
		}
	}
}

func TestSharingNativeLongStatePathPreservesDirectoryAndOwnedCleanup(t *testing.T) {
	ctx, c, _, backend := sharingNativeFixture(t)
	c.StateDir = filepath.Join(filepath.Dir(c.StateDir), strings.Repeat("long-state-", 12), "state")
	state, err := local.Open(ctx, c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	inventory, err := backend.Inventory(ctx, c, state)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := backend.Open(ctx, c, state,
		domain.Session{ID: "viewer_long_state", TargetID: "agent_fixture", Mode: "tmux", Purpose: "sharing"}, inventory[0])
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	if _, err := io.WriteString(endpoint, "printf 'FDN_LONG_CWD:%s\\n' \"$PWD\"\n"); err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, endpoint, "FDN_LONG_CWD:"+filepath.Join(c.StateDir, "sharing"))
	for _, resource := range backend.managed {
		record, path, socket, err := readNativeSession(resource.config, resource.session, resource.generation)
		if err != nil {
			t.Fatal(err)
		}
		if len(socket) < 108 || record.Server == nil || filepath.Dir(path) != filepath.Dir(socket) {
			t.Fatal("fixture did not exercise a long unchanged native socket/record namespace", len(socket), record)
		}
		server, err := (Native{}).serverIdentity(ctx, socket)
		if err != nil || server == nil || *server != *record.Server {
			t.Fatal("long socket observation lost exact native ownership", server, err)
		}
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal("long socket cleanup was not proved", err)
	}
}

func TestSharingNativeLongSocketPreservesExplicitWorkspaceDirectory(t *testing.T) {
	ctx, c, _, _ := sharingNativeFixture(t)
	root := filepath.Dir(c.StateDir)
	c.StateDir = filepath.Join(root, strings.Repeat("long-native-", 12), "state")
	workspace := filepath.Join(root, "approved workspace")
	if err := secure.Dir(c.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := secure.Dir(workspace); err != nil {
		t.Fatal(err)
	}
	native := Native{Command: []string{"/bin/sh", "-i"}, Directory: workspace}
	endpoint, err := native.OpenOwned(ctx, domain.Session{ID: "workspace_long", TargetID: "agent_fixture", Mode: "tmux", Purpose: "terminal"}, c, "native_workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	if _, err := io.WriteString(endpoint, "printf 'FDN_WORKSPACE:%s\\n' \"$PWD\"\n"); err != nil {
		t.Fatal(err)
	}
	readNativeSharing(t, endpoint, "FDN_WORKSPACE:"+workspace)
	if err := endpoint.Close(); err != nil {
		t.Fatal("long native workspace cleanup was not proved", err)
	}
}
