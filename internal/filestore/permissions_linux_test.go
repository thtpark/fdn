package filestore

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxNativeACLActuallyAddsAndRevokesExactUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("native fixture intentionally runs without root")
	}
	store, _ := fixtureStore(t, nil)
	if err := store.Create("file_1", "original.txt"); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	if err := store.SetReaders(t.Context(), "file_1", "original.txt", []uint32{uid}); err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenFile("file_1", "original.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := readACL(file, accessACL)
	if err != nil {
		t.Fatal(err)
	}
	readers, err := decodeAccessACL(data)
	if err != nil || !slices.Equal(readers, []uint32{uid}) {
		t.Fatal("native named ACL was not installed", readers, err)
	}
	if err := store.SetReaders(t.Context(), "file_1", "original.txt", nil); err != nil {
		t.Fatal(err)
	}
	data, err = readACL(file, accessACL)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		readers, err := decodeAccessACL(data)
		if err != nil || len(readers) != 0 {
			t.Fatal("native named ACL was not revoked", readers, err)
		}
	}
}

func TestLinuxClearsInheritedACLWithoutChangingPrivateAncestors(t *testing.T) {
	base := fixtureDirectory(t)
	dir, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	data, err := encodeAccessACL([]uint32{uint32(os.Geteuid())})
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(data[6:], 7)
	binary.LittleEndian.PutUint16(data[14:], 7)
	binary.LittleEndian.PutUint16(data[30:], 7)
	if err := unix.Fsetxattr(int(dir.Fd()), defaultACL, data, 0); err != nil {
		t.Fatal("native default ACL unsupported", err)
	}
	store, err := Open(filepath.Join(base, "files"), fixturePermissions)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create("file_1", "original.txt"); err != nil {
		t.Fatal(err)
	}
	requireMode(t, base, 0700)
	root, err := os.Open(store.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, key := range []string{accessACL, defaultACL} {
		acl, err := readACL(root, key)
		if err != nil || len(acl) != 0 {
			t.Fatal("inherited root ACL retained", key, acl, err)
		}
	}
	path, _ := store.Path("file_1", "original.txt")
	requireMode(t, path, 0600)
}
