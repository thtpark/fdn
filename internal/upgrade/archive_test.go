package upgrade

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func fixtureExecutable(version, commit, name string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"ok\":true,\"data\":{\"version\":\"%s\",\"commit\":\"%s\",\"binary\":\"%s\"}}'\n", version, commit, name))
}

func managedArchiveFixture(t *testing.T, n *Native) {
	t.Helper()
	root := archiveRoot(n.Policy.Prefix)
	old := filepath.Join(root, "fdn-original")
	if err := os.MkdirAll(filepath.Join(old, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	for _, name := range archiveBinaries {
		if err := os.WriteFile(filepath.Join(old, "bin", name), fixtureExecutable("0.3.2", strings.Repeat("b", 40), name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "current", "bin", name), filepath.Join(n.Policy.BinaryDir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertArchiveExecutables(t *testing.T, n *Native, version, commit string) {
	t.Helper()
	for _, name := range archiveBinaries {
		output, err := exec.Command(filepath.Join(n.Policy.BinaryDir, name), "--version").Output()
		if err != nil || !strings.Contains(string(output), `"version":"`+version+`"`) || !strings.Contains(string(output), `"commit":"`+commit+`"`) {
			t.Fatalf("actual managed wrapper %s: %s %v", name, output, err)
		}
		digest, err := fileHash(filepath.Join(n.Policy.BinaryDir, name))
		if err != nil || digest != domain.Hash(string(fixtureExecutable(version, commit, name))) {
			t.Fatal("actual managed executable bytes differ", name, err)
		}
	}
}

func TestManagedArchiveRefusesUnmanagedAndForeignLinks(t *testing.T) {
	for _, kind := range []string{"wrapper", "current", "foreign", "symlink-release"} {
		t.Run(kind, func(t *testing.T) {
			n, _, _, _ := nativeFixtureForUser(t)
			managedArchiveFixture(t, n)
			path := filepath.Join(n.Policy.BinaryDir, "agent-cli")
			if kind != "wrapper" {
				path = filepath.Join(archiveRoot(n.Policy.Prefix), "current")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if kind == "wrapper" || kind == "current" {
				if err := os.WriteFile(path, []byte("unmanaged"), 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				target := n.Policy.Prefix
				if kind == "symlink-release" {
					target = filepath.Join(archiveRoot(n.Policy.Prefix), "replaced")
					if err := os.Symlink(n.Policy.Prefix, target); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := managedArchive(n.Policy.Prefix); err == nil {
				t.Fatal("adopted unmanaged archive layout")
			}
		})
	}
}
