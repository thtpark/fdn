package transport

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/secure"
)

func TestStrictTunnelArguments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	identity := filepath.Join(root, "identity")
	known := filepath.Join(root, "known_hosts")
	if err := secure.Create(identity, []byte("test-only-key")); err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(known, []byte("test-only-host-pin")); err != nil {
		t.Fatal(err)
	}
	c := config.SSH{Host: "foundation.example", User: "worker", Port: 22, IdentityFile: identity, KnownHostsFile: known, LocalPort: 18443, RemotePort: 8443}
	args, err := Arguments(c)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, " ")
	for _, required := range []string{"StrictHostKeyChecking=yes", "BatchMode=yes", "ExitOnForwardFailure=yes", "127.0.0.1:18443:127.0.0.1:8443"} {
		if !strings.Contains(text, required) {
			t.Fatal(required)
		}
	}
	c.Host = "-oProxyCommand=bad"
	if _, err := Arguments(c); err == nil {
		t.Fatal("SSH option injection")
	}
}
