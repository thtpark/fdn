package platform

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestManagedAccountPrepareReservedGroupAndServerMembership(t *testing.T) {
	manager, system := accountFixture(t)
	if _, err := manager.Prepare(context.Background(), false); !errors.Is(err, ErrAccountApplyRequired) {
		t.Fatal(err)
	}
	inventory, err := manager.Prepare(context.Background(), true)
	if err != nil || !inventory.GroupReserved || len(inventory.Accounts) != 0 {
		t.Fatal(inventory, err)
	}
	server := system.users["fdn"]
	if server.GID != manager.options.ServerGID || !slices.Contains(server.Groups, manager.options.IngressGID) {
		t.Fatal("pinned server did not receive ingress supplementary membership", server)
	}
	count := len(system.mutations)
	if _, err := manager.Prepare(context.Background(), true); err != nil || len(system.mutations) != count {
		t.Fatal("group preparation was not idempotent", err)
	}
	account := applyTestAccount(t, manager, accountTestAgent, accountTestKey(t))
	if slices.Contains(system.users[account.Username].Groups, manager.options.ServerGID) {
		t.Fatal("tunnel account received server DB group")
	}
}

func TestManagedFoundationServiceIngressGroupAndWritableDirectories(t *testing.T) {
	root := accountTestRoot(t)
	options := Options{OS: "linux", Role: "foundation", Binary: "/opt/fdn/fdn-service", ConfigFile: "/var/lib/fdn/server.json", StateDir: "/var/lib/fdn", Home: "/var/lib/fdn", User: "fdn", IngressGroup: "fdn_ingress", IngressDirs: []string{"/run/fdn-ingress"}, Definition: filepath.Join(root, "foundation.service")}
	definition, err := Render(options)
	if err != nil || !strings.Contains(definition.Content, "SupplementaryGroups=fdn_ingress\n") || !strings.Contains(definition.Content, `ReadWritePaths="/var/lib/fdn" "/run/fdn-ingress"`) {
		t.Fatal("service cannot traverse/write its configured ingress", definition, err)
	}
	for _, group := range []string{"fdn", "root", "fdn\nUser=root"} {
		invalid := options
		invalid.IngressGroup = group
		if _, err := Render(invalid); err == nil {
			t.Fatal("unsafe supplementary group accepted")
		}
	}
	invalid := options
	invalid.User = "root"
	if _, err := Render(invalid); err == nil {
		t.Fatal("Foundation API rendered a privileged root service")
	}
	invalid = options
	invalid.User = "root_alias"
	if _, err := renderService(invalid, func(string) (uint32, bool, error) { return 0, true, nil }); err == nil {
		t.Fatal("Foundation API rendered a root-UID alias service")
	}
	invalid = options
	invalid.IngressDirs = nil
	if _, err := Render(invalid); err == nil {
		t.Fatal("supplementary group omitted writable ingress paths")
	}
	options.IngressGroup = ""
	options.OS = runtime.GOOS
	record := &recorder{}
	if err := Install(context.Background(), options, record); err == nil || len(record.calls) != 0 {
		t.Fatal("incomplete native Foundation registration reached host manager", err)
	}
}
