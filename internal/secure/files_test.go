package secure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFilesAndLocks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(root, "state", "secret")
	if err := Write(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, 3)
	if err != nil || string(got) != "two" {
		t.Fatal(string(got), err)
	}
	if _, err := Read(path, 2); err == nil {
		t.Fatal("size limit bypass")
	}
	if err := Create(path, []byte("overwrite")); err == nil {
		t.Fatal("exclusive create replaced file")
	}
	link := filepath.Join(root, "state", "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link, 100); err == nil {
		t.Fatal("followed symlink")
	}
	if err := Write(link, []byte("bad")); err == nil {
		t.Fatal("replaced symlink")
	}
	lockPath := filepath.Join(root, "slot")
	lock, err := Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(lockPath); err == nil {
		t.Fatal("parallel slot")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 100); err == nil {
		t.Fatal("insecure file accepted")
	}
	if err := Write(path, []byte("bad")); err == nil {
		t.Fatal("insecure replacement accepted")
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Dir(filepath.Dir(path)); err == nil {
		t.Fatal("insecure directory accepted")
	}
}
