package identityservice

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func TestContainerStoragePinsPrivateRootWithoutWeakeningNativeAncestors(t *testing.T) {
	base := testDir(t)
	shared := filepath.Join(base, "apple-managed")
	if err := os.Mkdir(shared, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0775); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(shared, "app")
	if err := secure.Dir(root); err != nil {
		t.Fatal(err)
	}
	container, err := newContainerStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "ios" {
		if _, err := NewContainerStorage(root); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("native desktop entrypoints could select app-container storage", err)
		}
	}
	state := filepath.Join(root, "identity")
	output := filepath.Join(root, "public", "request.json")
	if err := secure.Dir(filepath.Dir(output)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(state, "Phone fixture", output, Options{}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native private-state ancestry was weakened", err)
	}
	first, err := Prepare(state, "Phone fixture", output, Options{Container: container})
	if err != nil {
		t.Fatal("pinned sandbox storage rejected its owner-only subtree", err)
	}
	replayed, err := Prepare(state, "Phone fixture", output, Options{Container: container})
	if err != nil || replayed.RequestID != first.RequestID || replayed.AuthKey != first.AuthKey {
		t.Fatal("app storage changed the retained device", err)
	}
	if _, err := Prepare(filepath.Join(base, "escaped"), "Escaped", output, Options{Container: container}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("container capability admitted outside identity state", err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(output), link); err != nil {
		t.Fatal(err)
	}
	if err := container.checkAncestors(filepath.Join(link, "request.json")); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("container capability followed a substituted child directory", err)
	}
	if err := os.Rename(root, root+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := secure.Dir(root); err != nil {
		t.Fatal(err)
	}
	if err := container.checkAncestors(filepath.Join(root, "identity")); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("container capability adopted a replacement root inode", err)
	}
}
