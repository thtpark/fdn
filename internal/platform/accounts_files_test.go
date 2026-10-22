package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedAccountLogicalFilesystemPrivateHomeAndNativeBoundary(t *testing.T) {
	manager, system := accountFixture(t)
	root := manager.filesystemRoot
	info, err := os.Lstat(filepath.Dir(root))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("fixture must model a private host home", info, err)
	}
	owner := uint32(os.Geteuid())
	if err := accountAncestors(root, owner, true); err == nil {
		t.Fatal("fixture did not reproduce a non-traversable host ancestor")
	}
	account := applyTestAccount(t, manager, accountTestAgent, accountTestKey(t))
	if account.State != AccountActive {
		t.Fatal("injected account did not activate within its logical root", account)
	}
	if err := manager.verifyActive(context.Background(), account); err != nil {
		t.Fatal("logical root did not retain public policy protections", err)
	}
	if err := manager.accountAncestors(root, owner+1, false); err == nil {
		t.Fatal("logical root bypassed ancestor ownership")
	}

	ordinary, err := NewAccountManager(manager.options, system)
	if err != nil {
		t.Fatal(err)
	}
	if err := ordinary.accountAncestors(root, owner, true); err == nil {
		t.Fatal("ordinary construction implicitly selected a logical root")
	}
	runner := &accountRecordingRunner{}
	native, err := NewNativeAccountManager(manager.options, runner)
	if err != nil || native.filesystemRoot != "/" || !native.native {
		t.Fatal("native construction did not retain the real filesystem root", err)
	}
	native.filesystemRoot = root
	if err := native.accountAncestors(root, owner, true); err == nil {
		t.Fatal("native manager accepted a logical filesystem root")
	}
	if _, err := newAccountManager(manager.options, &nativeAccountSystem{options: manager.options, runner: runner}, root); err == nil {
		t.Fatal("native adapter accepted injected filesystem construction")
	}
	if len(runner.calls) != 0 {
		t.Fatal("filesystem boundary checks invoked native host tools")
	}
}

func TestManagedAccountLogicalFilesystemAncestorProtection(t *testing.T) {
	for _, test := range []struct {
		name         string
		ancestor     string
		mode         os.FileMode
		symlink      bool
		allowPrivate bool
		allowPublic  bool
	}{
		{"private-host-home", "host-home", 0700, false, true, true},
		{"group-writable-host-home", "host-home", 0770, false, false, false},
		{"world-writable-host-home", "host-home", 0777, false, false, false},
		{"symlinked-host-home", "host-home", 0700, true, false, false},
		{"protected-logical-root", "root", 0755, false, true, true},
		{"private-logical-root", "root", 0700, false, true, false},
		{"group-writable-logical-root", "root", 0770, false, false, false},
		{"world-writable-logical-root", "root", 0777, false, false, false},
		{"symlinked-logical-root", "root", 0755, true, false, false},
		{"protected-parent", "parent", 0755, false, true, true},
		{"private-parent", "parent", 0700, false, true, false},
		{"group-writable-parent", "parent", 0770, false, false, false},
		{"world-writable-parent", "parent", 0777, false, false, false},
		{"symlinked-parent", "parent", 0755, true, false, false},
	} {
		for _, visibility := range []string{"private", "public"} {
			t.Run(test.name+"/"+visibility, func(t *testing.T) {
				manager, _ := accountFixture(t)
				root := manager.filesystemRoot
				parent := filepath.Join(root, "parent")
				if err := os.Mkdir(parent, 0755); err != nil {
					t.Fatal(err)
				}
				traversable := visibility == "public"
				mode, allowed := os.FileMode(0700), test.allowPrivate
				if traversable {
					mode, allowed = 0755, test.allowPublic
				}
				leaf := filepath.Join(parent, "leaf")
				if err := os.Mkdir(leaf, mode); err != nil {
					t.Fatal(err)
				}
				ancestor := parent
				switch test.ancestor {
				case "host-home":
					ancestor = filepath.Dir(root)
				case "root":
					ancestor = root
				}
				if err := os.Chmod(ancestor, test.mode); err != nil {
					t.Fatal(err)
				}
				if test.symlink {
					real := ancestor + ".real"
					if err := os.Rename(ancestor, real); err != nil {
						t.Fatal(err)
					}
					if test.ancestor == "host-home" {
						t.Cleanup(func() {
							if err := os.Remove(ancestor); err != nil && !errors.Is(err, os.ErrNotExist) {
								t.Error(err)
							}
							if err := os.Rename(real, ancestor); err != nil {
								t.Error(err)
							}
						})
					}
					if err := os.Symlink(real, ancestor); err != nil {
						t.Fatal(err)
					}
				}
				err := manager.accountDirectory(leaf, mode, false, traversable)
				if (err == nil) != allowed {
					t.Fatalf("ancestor allowed=%t, want %t: %v", err == nil, allowed, err)
				}
			})
		}
	}
}

func TestManagedAccountLogicalFilesystemRejectsEscapes(t *testing.T) {
	manager, system := accountFixture(t)
	root := manager.filesystemRoot
	outside := filepath.Join(filepath.Dir(root), "root-sibling")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, filepath.Dir(root), root + "/../root-sibling", "relative"} {
		for _, traversable := range []bool{false, true} {
			if err := manager.accountAncestors(path, uint32(os.Geteuid()), traversable); err == nil {
				t.Fatal("logical filesystem accepted an escaped or noncanonical path")
			}
		}
	}
	for _, invalidRoot := range []string{"", ".", root + "/.", outside} {
		if _, err := newAccountManager(manager.options, system, invalidRoot); err == nil {
			t.Fatal("constructor accepted an invalid or unrelated logical root")
		}
	}
	for _, state := range []bool{false, true} {
		options := manager.options
		if state {
			options.StateDir = filepath.Join(outside, "state")
		} else {
			options.PolicyDir = filepath.Join(outside, "policy")
		}
		if _, err := newAccountManager(options, system, root); err == nil {
			t.Fatal("constructor accepted managed paths outside its logical root")
		}
	}
}
