package filestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

var fixturePermissions = PermissionFunc(func(context.Context, string, []uint32) error { return nil })

// Keep fixture state under this package, never the host's default temporary tree.
func fixtureDirectory(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp(".", ".filestore-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func fixtureStore(t *testing.T, permissions Permissions) (*Store, string) {
	t.Helper()
	base := fixtureDirectory(t)
	store, err := Open(filepath.Join(base, "files"), permissions)
	if err != nil {
		t.Fatal(err)
	}
	return store, base
}

func requireCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if err == nil || domain.AsFault(err).Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func requireBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, expected) {
		t.Fatalf("file bytes = %q, error = %v, want %q", data, err, expected)
	}
}

func requireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("mode of %s = %v, error = %v, want %o", path, info, err, mode)
	}
}

func TestCreateOwnedOriginalNameAndReadOnlyOpen(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	const id, name = "file_0123456789", "original photo.png"
	if err := store.Create(id, name); err != nil {
		t.Fatal(err)
	}
	path, err := store.Path(id, name)
	if err != nil || path != filepath.Join(store.root, id, name) {
		t.Fatal(path, err)
	}
	requireMode(t, store.root, 0711)
	requireMode(t, filepath.Dir(path), 0711)
	requireMode(t, path, 0600)
	requireBytes(t, path, nil)
	requireCode(t, store.Create(id, name), domain.Conflict)
	requireCode(t, store.Create(id, "different.png"), domain.Conflict)

	file, err := store.OpenFile(id, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("forbidden")); err == nil {
		t.Error("OpenFile returned a writable descriptor")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(context.Background(), id, name, 0, []byte("durable"), 0); err != nil {
		t.Fatal(err)
	}
	chunk, err := store.ReadChunk(context.Background(), id, name, 1, 4)
	if err != nil || string(chunk) != "urab" {
		t.Fatal(string(chunk), err)
	}
	chunk, err = store.ReadChunk(context.Background(), id, name, 7, 0)
	if err != nil || len(chunk) != 0 {
		t.Fatal(chunk, err)
	}
	reopened, err := Open(store.root, fixturePermissions)
	if err != nil {
		t.Fatal(err)
	}
	file, err = reopened.OpenFile(id, name)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	if err := errors.Join(readErr, file.Close()); err != nil || string(data) != "durable" {
		t.Fatal(string(data), err)
	}
}

func TestPathRejectsTraversalAndControls(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	for _, id := range []string{"", ".", "..", "../escape", "/absolute", "a/b", `a\b`, "a\nb", "two words", strings.Repeat("x", 129)} {
		t.Run("id-"+strings.ReplaceAll(id, "/", "_"), func(t *testing.T) {
			_, err := store.Path(id, "safe.txt")
			requireCode(t, err, domain.Invalid)
			requireCode(t, store.Create(id, "safe.txt"), domain.Invalid)
		})
	}
	for _, name := range []string{"", " ", ".", "..", "../escape", "/absolute", "a/b", `a\b`, "a\nb", "a\tb", "a\x00b", "a\x7fb", "\xff", strings.Repeat("x", 256)} {
		_, err := store.Path("file_1", name)
		requireCode(t, err, domain.Invalid)
		requireCode(t, store.Create("file_1", name), domain.Invalid)
	}
	for _, name := range []string{"-original.txt", ".original", "한글 문서.txt", strings.Repeat("x", 255)} {
		if _, err := store.Path("file_1", name); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestOpenRejectsUnsafeRootAndSymlinkAncestors(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0701, 0710, 0755, 0771, 0777, 0711 | os.ModeSetgid, 0711 | os.ModeSticky} {
		t.Run(mode.String(), func(t *testing.T) {
			base := fixtureDirectory(t)
			root := filepath.Join(base, "unsafe")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, mode); err != nil {
				t.Fatal(err)
			}
			_, err := Open(root, fixturePermissions)
			requireCode(t, err, domain.Denied)
			info, err := os.Stat(root)
			if err != nil || info.Mode() != mode|os.ModeDir {
				t.Fatal("Open changed an existing unsafe root", info, err)
			}
		})
	}
	base := fixtureDirectory(t)
	root := filepath.Join(base, "real")
	if _, err := Open(root, fixturePermissions); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, fixturePermissions); err == nil {
		t.Fatal("symlink root accepted")
	}
	if _, err := Open(filepath.Join(link, "child"), fixturePermissions); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	for _, path := range []string{"relative", root + "/../escape", "/", root + "\n"} {
		_, err := Open(path, fixturePermissions)
		requireCode(t, err, domain.Invalid)
	}
	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(regular, fixturePermissions); err == nil {
		t.Fatal("regular root accepted")
	}
	if _, err := Open(filepath.Join(base, "missing", "files"), fixturePermissions); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing parent should fail without creating arbitrary ancestors", err)
	}
}

func TestWriteChunkResumesMatchingPartialTailAndFullReplayWithoutRewrite(t *testing.T) {
	ctx := context.Background()
	store, _ := fixtureStore(t, fixturePermissions)
	const id, name = "file_replay", "original.txt"
	if err := store.Create(id, name); err != nil {
		t.Fatal(err)
	}
	prefix, pending := []byte("committed:"), []byte("pending original bytes")
	if err := store.WriteChunk(ctx, id, name, 0, prefix, 0); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path(id, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(pending[:7])
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		t.Fatal(err)
	}
	committed := int64(len(prefix))
	if err := store.WriteChunk(ctx, id, name, committed, pending, committed); err != nil {
		t.Fatal(err)
	}
	expected := append(slices.Clone(prefix), pending...)
	requireBytes(t, path, expected)
	oldTime := time.Unix(1000, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(ctx, id, name, committed, pending, committed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(oldTime) {
		t.Fatal("exact pending replay rewrote existing bytes", info, err)
	}
	requireCode(t, store.WriteChunk(ctx, id, name, 0, []byte("changed"), committed), domain.Conflict)
	requireBytes(t, path, expected)
}

func TestWriteChunkConflictingTailNeverChangesBytes(t *testing.T) {
	for _, test := range []struct {
		name      string
		stored    string
		pending   string
		committed int64
	}{
		{"changed-partial-tail", "prefix:bad", "good tail", 7},
		{"changed-full-tail", "prefix:wrong", "right", 7},
		{"longer-tail", "prefix:too long", "tiny", 7},
		{"missing-committed-prefix", "short", "tail", 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := fixtureStore(t, fixturePermissions)
			if err := store.Create("file_1", "safe.txt"); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path("file_1", "safe.txt")
			if err := os.WriteFile(path, []byte(test.stored), 0600); err != nil {
				t.Fatal(err)
			}
			err := store.WriteChunk(context.Background(), "file_1", "safe.txt", test.committed, []byte(test.pending), test.committed)
			requireCode(t, err, domain.Conflict)
			requireBytes(t, path, []byte(test.stored))
		})
	}
}

func TestChunkBoundsAndCancellation(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	if err := store.Create("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, test := range []struct {
		offset, committed int64
		data              []byte
	}{
		{-1, 0, []byte("x")},
		{0, -1, []byte("x")},
		{0, 0, nil},
		{0, 0, make([]byte, domain.FileChunkSize+1)},
		{domain.MaxFileSize, domain.MaxFileSize, []byte("x")},
		{1<<63 - 1, 0, []byte("x")},
		{0, domain.MaxFileSize + 1, []byte("x")},
	} {
		requireCode(t, store.WriteChunk(ctx, "file_1", "safe.txt", test.offset, test.data, test.committed), domain.Invalid)
	}
	for _, test := range []struct {
		offset int64
		length int
	}{
		{-1, 1}, {0, -1}, {0, domain.FileChunkSize + 1},
		{domain.MaxFileSize, 1}, {1<<63 - 1, 1}, {0, 1},
	} {
		_, err := store.ReadChunk(ctx, "file_1", "safe.txt", test.offset, test.length)
		requireCode(t, err, domain.Invalid)
	}
	data := bytes.Repeat([]byte{0xa5}, domain.FileChunkSize)
	if err := store.WriteChunk(ctx, "file_1", "safe.txt", 0, data, 0); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadChunk(ctx, "file_1", "safe.txt", 0, len(data))
	if err != nil || !bytes.Equal(read, data) {
		t.Fatal("maximum chunk did not round-trip", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.WriteChunk(canceled, "file_1", "safe.txt", int64(len(data)), []byte("x"), int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.ReadChunk(canceled, "file_1", "safe.txt", 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.SetReaders(canceled, "file_1", "safe.txt", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	path, _ := store.Path("file_1", "safe.txt")
	requireBytes(t, path, data)
}

func TestExactOneGiBFileBound(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	if err := store.Create("file_1", "sparse.bin"); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("file_1", "sparse.bin")
	if err := os.Truncate(path, domain.MaxFileSize); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data, err := store.ReadChunk(ctx, "file_1", "sparse.bin", domain.MaxFileSize-1, 1)
	if err != nil || !bytes.Equal(data, []byte{0}) {
		t.Fatal(data, err)
	}
	if err := store.WriteChunk(ctx, "file_1", "sparse.bin", domain.MaxFileSize-1, []byte{0}, domain.MaxFileSize-1); err != nil {
		t.Fatal(err)
	}
	requireCode(t, store.WriteChunk(ctx, "file_1", "sparse.bin", domain.MaxFileSize, []byte{0}, domain.MaxFileSize), domain.Invalid)
	if err := os.Truncate(path, domain.MaxFileSize+1); err != nil {
		t.Fatal(err)
	}
	_, err = store.OpenFile("file_1", "sparse.bin")
	requireCode(t, err, domain.Denied)
}

func TestRejectsSymlinkReplacedAndHardLinkedFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "owned-replacement", "directory-replacement", "hard-link", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			store, base := fixtureStore(t, PermissionFunc(func(context.Context, string, []uint32) error {
				calls++
				return nil
			}))
			const id, name = "file_1", "safe.txt"
			if err := store.Create(id, name); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path(id, name)
			retained := filepath.Join(base, "retained")
			target := filepath.Join(base, "unrelated.txt")
			if err := os.WriteFile(target, []byte("unrelated"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink", "owned-replacement", "fifo":
				if err := os.Rename(path, retained); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(target, path)
				case "owned-replacement":
					err = os.WriteFile(path, []byte("replacement"), 0600)
				case "fifo":
					err = unix.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "directory-replacement":
				if err := os.Rename(filepath.Dir(path), retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Dir(path), 0711); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(path, retained); err != nil {
					t.Fatal(err)
				}
			}
			for _, operation := range []func() error{
				func() error {
					file, err := store.OpenFile(id, name)
					if file != nil {
						file.Close()
					}
					return err
				},
				func() error { return store.WriteChunk(context.Background(), id, name, 0, []byte("bad"), 0) },
				func() error { _, err := store.ReadChunk(context.Background(), id, name, 0, 0); return err },
				func() error { return store.SetReaders(context.Background(), id, name, []uint32{42}) },
				func() error { return store.Remove(id, name) },
			} {
				if err := operation(); err == nil {
					t.Error("unsafe resource was adopted")
				}
			}
			if calls != 0 {
				t.Fatal("permissions were applied to a substituted resource")
			}
			requireBytes(t, target, []byte("unrelated"))
			requireMode(t, target, 0600)
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unexpected resource was removed", err)
			}
		})
	}
}

func TestRejectsReplacedRootAndSymlinkDirectory(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "root-replacement", true: "directory-symlink"}[symlink], func(t *testing.T) {
			store, base := fixtureStore(t, fixturePermissions)
			if err := store.Create("file_1", "safe.txt"); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path("file_1", "safe.txt")
			if symlink {
				dir := filepath.Dir(path)
				retained := filepath.Join(base, "retained")
				if err := os.Rename(dir, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(retained, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(store.root, filepath.Join(base, "retained")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(store.root, 0711); err != nil {
					t.Fatal(err)
				}
				requireCode(t, store.Create("file_2", "new.txt"), domain.Conflict)
			}
			if _, err := store.OpenFile("file_1", "safe.txt"); err == nil {
				t.Fatal("substituted tree accepted")
			}
			if err := store.Remove("file_1", "safe.txt"); err == nil {
				t.Fatal("substituted tree removed")
			}
		})
	}
}

func TestRejectsUnsafeFileModesAndUnexpectedResources(t *testing.T) {
	for _, mode := range []os.FileMode{0644, 0640, 0660, 0700, 0400, 0601, 0600 | os.ModeSetuid} {
		t.Run(mode.String(), func(t *testing.T) {
			store, _ := fixtureStore(t, fixturePermissions)
			if err := store.Create("file_1", "safe.txt"); err != nil {
				t.Fatal(err)
			}
			path, _ := store.Path("file_1", "safe.txt")
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			_, err := store.OpenFile("file_1", "safe.txt")
			requireCode(t, err, domain.Denied)
			requireCode(t, store.Remove("file_1", "safe.txt"), domain.Denied)
		})
	}
	store, _ := fixtureStore(t, fixturePermissions)
	if err := store.Create("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
	path, _ := store.Path("file_1", "safe.txt")
	extra := filepath.Join(filepath.Dir(path), "unexpected.txt")
	if err := os.WriteFile(extra, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	requireCode(t, store.Remove("file_1", "safe.txt"), domain.Conflict)
	requireBytes(t, extra, []byte("preserve"))
	requireBytes(t, path, nil)
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestSetReadersPinnedExactRevocationAndFailure(t *testing.T) {
	var calls [][]uint32
	var actualPath string
	store, _ := fixtureStore(t, PermissionFunc(func(ctx context.Context, path string, uids []uint32) error {
		if path == actualPath {
			t.Fatal("permissions received an unpinned pathname")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		pinned, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.Stat(actualPath)
		if err != nil || !os.SameFile(pinned, actual) {
			t.Fatal("permissions did not receive the exact pinned file", err)
		}
		calls = append(calls, slices.Clone(uids))
		return nil
	}))
	if err := store.Create("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
	actualPath, _ = store.Path("file_1", "safe.txt")
	input := []uint32{91, 42, 91}
	if err := store.SetReaders(context.Background(), "file_1", "safe.txt", input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, []uint32{91, 42, 91}) || !reflect.DeepEqual(calls, [][]uint32{{42, 91}}) {
		t.Fatal(input, calls)
	}
	if err := store.SetReaders(context.Background(), "file_1", "safe.txt", nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(calls[1]) != 0 {
		t.Fatal("revocation was not applied", calls)
	}
	failed := errors.New("fixture ACL failure")
	store.permissions = PermissionFunc(func(context.Context, string, []uint32) error { return failed })
	if err := store.SetReaders(context.Background(), "file_1", "safe.txt", nil); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	requireMode(t, actualPath, 0600)
}

func TestPermissionsCannotMutateReplacementThroughUnpinnedPath(t *testing.T) {
	var actualPath, retained string
	store, base := fixtureStore(t, PermissionFunc(func(_ context.Context, path string, _ []uint32) error {
		if err := os.Rename(actualPath, retained); err != nil {
			return err
		}
		if err := os.WriteFile(actualPath, []byte("replacement"), 0600); err != nil {
			return err
		}
		return os.Chmod(path, 0600)
	}))
	if err := store.Create("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
	actualPath, _ = store.Path("file_1", "safe.txt")
	retained = filepath.Join(base, "retained")
	requireCode(t, store.SetReaders(context.Background(), "file_1", "safe.txt", []uint32{42}), domain.Conflict)
	requireBytes(t, actualPath, []byte("replacement"))
	requireMode(t, actualPath, 0600)
	requireBytes(t, retained, nil)
}

func TestRemoveMissingContentAndDirectoryIsIdempotent(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	if err := store.Remove("file_absent", "original.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("file_absent", "original.txt"); err != nil {
		t.Fatal(err)
	}
	for _, missingFile := range []bool{false, true} {
		id := map[bool]string{false: "file_present", true: "file_missing"}[missingFile]
		if err := store.Create(id, "original.txt"); err != nil {
			t.Fatal(err)
		}
		path, _ := store.Path(id, "original.txt")
		if missingFile {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Remove(id, "original.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("empty ID directory retained", err)
		}
		if err := store.Remove(id, "original.txt"); err != nil {
			t.Fatal(err)
		}
		requireCode(t, store.Create(id, "original.txt"), domain.Conflict)
	}
	empty := filepath.Join(store.root, "file_empty")
	if err := os.Mkdir(empty, 0711); err != nil {
		t.Fatal(err)
	}
	requireCode(t, store.Create("file_empty", "original.txt"), domain.Conflict)
	if err := store.Remove("file_empty", "original.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentExactPendingReplay(t *testing.T) {
	store, _ := fixtureStore(t, fixturePermissions)
	if err := store.Create("file_1", "safe.txt"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			failures <- store.WriteChunk(context.Background(), "file_1", "safe.txt", 0, []byte("same pending bytes"), 0)
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	path, _ := store.Path("file_1", "safe.txt")
	requireBytes(t, path, []byte("same pending bytes"))
}

func TestNativeCurrentUIDReadGrantRevocationAndUnknownUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("native fixture intentionally runs without root")
	}
	store, _ := fixtureStore(t, nil)
	if err := store.Create("file_native", "original.txt"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	uid := uint32(os.Geteuid())
	if err := store.SetReaders(ctx, "file_native", "original.txt", []uint32{uid, uid}); err != nil {
		t.Fatal("current-user native read ACL unsupported", err)
	}
	path, _ := store.Path("file_native", "original.txt")
	mode := os.FileMode(0600)
	if runtime.GOOS == "linux" {
		mode = 0640
	}
	requireMode(t, path, mode)
	if err := store.WriteChunk(ctx, "file_native", "original.txt", 0, []byte("native"), 0); err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenFile("file_native", "original.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	requireCode(t, store.SetReaders(ctx, "file_native", "original.txt", []uint32{4294967293}), domain.Prerequisite)
	requireMode(t, path, mode)
	requireCode(t, store.SetReaders(ctx, "file_native", "original.txt", []uint32{^uint32(0)}), domain.Invalid)
	if err := store.SetReaders(ctx, "file_native", "original.txt", nil); err != nil {
		t.Fatal("native empty reader ACL failed", err)
	}
	requireMode(t, path, 0600)
	requireCode(t, store.SetReaders(ctx, "file_native", "original.txt", []uint32{0}), domain.Prerequisite)
	requireMode(t, path, 0600)
	requireBytes(t, path, []byte("native"))
	if err := NativePermissions().Apply(ctx, path, nil); err != nil {
		t.Fatal("native direct empty readers failed", err)
	}
}
