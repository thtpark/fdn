package filestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
	"golang.org/x/sys/unix"
)

type identity struct {
	name      string
	directory os.FileInfo
	file      os.FileInfo
	removed   bool
}

type Store struct {
	root        string
	permissions Permissions
	rootInfo    os.FileInfo
	mu          sync.Mutex
	identities  map[string]*identity
}

// Open opens or creates the final root directory. Its parent must already exist.
// Existing roots must have the same protected 0711 layout as new roots.
func Open(root string, permissions Permissions) (*Store, error) {
	if err := validAbsolutePath(root); err != nil {
		return nil, err
	}
	root, err := canonicalAbsolutePath(root)
	if err != nil {
		return nil, err
	}
	if root == "/" {
		return nil, domain.Fail(domain.Invalid, "filesystem root cannot be a file store")
	}
	parent, err := openAbsoluteDirectory(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(root)
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0711); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, filesystemError("create file store root", err)
	}
	dir, err := openDirectoryAt(parent, name, root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if created {
		if err := initializeACL(context.Background(), dir, true); err != nil {
			return nil, err
		}
	}
	if err := checkDirectory(context.Background(), dir); err != nil {
		return nil, err
	}
	if err := checkLinkedAt(parent, name, dir); err != nil {
		return nil, err
	}
	if created {
		if err := errors.Join(dir.Sync(), parent.Sync()); err != nil {
			return nil, err
		}
	}
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	if permissions == nil {
		permissions = NativePermissions()
	}
	return &Store{
		root: root, permissions: permissions, rootInfo: info,
		identities: make(map[string]*identity),
	}, nil
}

// Path returns the validated canonical content path for diagnostics only.
func (s *Store) Path(id, name string) (string, error) {
	if !domain.ValidOperation(id) {
		return "", domain.Fail(domain.Invalid, "file ID must be a safe immutable ID")
	}
	if err := domain.CheckText(name, "file name", 255, true); err != nil {
		return "", err
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", domain.Fail(domain.Invalid, "file name must be a single safe name, not a path")
	}
	return filepath.Join(s.root, id, name), nil
}

// Create exclusively creates a new ID directory and its empty 0600 file.
func (s *Store) Create(id, name string) (err error) {
	path, err := s.Path(id, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.identities[id]; exists {
		return domain.Fail(domain.Conflict, "file ID has already been used")
	}
	root, err := s.openRoot(context.Background())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := unix.Mkdirat(int(root.Fd()), id, 0711); err != nil {
		return filesystemError("create immutable file directory", err)
	}
	dir, err := openDirectoryAt(root, id, filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err := initializeACL(context.Background(), dir, true); err != nil {
		return err
	}
	if err := checkDirectory(context.Background(), dir); err != nil {
		return err
	}
	file, err := openFileAt(dir, name, path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := initializeACL(context.Background(), file, false); err != nil {
		return err
	}
	if err := checkFile(context.Background(), file); err != nil {
		return err
	}
	if err := s.remember(id, name, dir, file); err != nil {
		return err
	}
	if err := errors.Join(checkLinkedPath(root), checkLinkedAt(root, id, dir), checkLinkedAt(dir, name, file)); err != nil {
		return err
	}
	return errors.Join(file.Sync(), dir.Sync(), root.Sync())
}

// WriteChunk durably appends to the authoritative committed prefix. A matching
// partial or complete uncommitted tail is resumed without rewriting those bytes.
func (s *Store) WriteChunk(ctx context.Context, id, name string, offset int64, data []byte, committed int64) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if offset < 0 || committed < 0 || committed > domain.MaxFileSize || len(data) == 0 ||
		len(data) > domain.FileChunkSize || offset > domain.MaxFileSize-int64(len(data)) {
		return domain.Fail(domain.Invalid, "file chunk exceeds the 1 GiB file or 256 KiB chunk bounds")
	}
	if offset != committed {
		return domain.Fail(domain.Conflict, "file chunk offset must equal the committed prefix")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	opened, err := s.open(ctx, id, name, os.O_RDWR)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, opened.close()) }()
	info, err := opened.file.Stat()
	if err != nil {
		return err
	}
	end := offset + int64(len(data))
	if info.Size() < committed || info.Size() > end {
		return domain.Fail(domain.Conflict, "stored file does not match the committed prefix and pending chunk")
	}
	tail := make([]byte, int(info.Size()-committed))
	if _, err := opened.file.ReadAt(tail, committed); err != nil {
		return fmt.Errorf("read pending file chunk: %w", err)
	}
	if !bytes.Equal(tail, data[:len(tail)]) {
		return domain.Fail(domain.Conflict, "pending file chunk bytes differ from the upload replay")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(tail) < len(data) {
		n, writeErr := opened.file.WriteAt(data[len(tail):], info.Size())
		if writeErr == nil && n != len(data)-len(tail) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			return errors.Join(fmt.Errorf("append file chunk: %w", writeErr), opened.file.Sync())
		}
	}
	if err := errors.Join(opened.file.Sync(), opened.dir.Sync(), opened.linked(id, name)); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Store) ReadChunk(ctx context.Context, id, name string, offset int64, length int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || length > domain.FileChunkSize ||
		offset > domain.MaxFileSize-int64(length) {
		return nil, domain.Fail(domain.Invalid, "file read exceeds the 1 GiB file or 256 KiB chunk bounds")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	opened, err := s.open(ctx, id, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer opened.close()
	info, err := opened.file.Stat()
	if err != nil {
		return nil, err
	}
	if offset+int64(length) > info.Size() {
		return nil, domain.Fail(domain.Invalid, "file read extends beyond stored bytes")
	}
	data := make([]byte, length)
	if _, err := opened.file.ReadAt(data, offset); err != nil {
		return nil, fmt.Errorf("read file chunk: %w", err)
	}
	if err := errors.Join(ctx.Err(), opened.linked(id, name)); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Store) OpenFile(id, name string) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	opened, err := s.open(context.Background(), id, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(opened.dir.Close(), opened.root.Close()); err != nil {
		opened.file.Close()
		return nil, err
	}
	return opened.file, nil
}

func (s *Store) SetReaders(ctx context.Context, id, name string, uids []uint32) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	readers, err := readerUIDs(uids)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	opened, err := s.open(ctx, id, name, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, opened.close()) }()
	if native, ok := s.permissions.(interface {
		applyFile(context.Context, *os.File, []uint32) error
	}); ok {
		err = native.applyFile(ctx, opened.file, readers)
	} else {
		err = s.permissions.Apply(ctx, descriptorPath(opened.file), readers)
	}
	if err != nil {
		return err
	}
	if err := checkFile(ctx, opened.file); err != nil {
		return err
	}
	return errors.Join(opened.file.Sync(), opened.dir.Sync(), opened.linked(id, name), ctx.Err())
}

// Remove removes only this file and its otherwise empty directory. Missing
// content is a successful tombstone replay; unexpected resources are preserved.
func (s *Store) Remove(id, name string) (err error) {
	path, err := s.Path(id, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if known := s.identities[id]; known != nil && known.name != name {
		return domain.Fail(domain.Conflict, "immutable file ID has a different name")
	}
	root, err := s.openRoot(context.Background())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	dir, err := openDirectoryAt(root, id, filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		s.identities[id] = &identity{name: name, removed: true}
		return root.Sync()
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err := s.checkIdentity(id, name, dir, nil); err != nil {
		return err
	}
	if err := checkDirectory(context.Background(), dir); err != nil {
		return err
	}
	if err := onlyFile(dir, name); err != nil {
		return err
	}
	file, err := openFileAt(dir, name, path, os.O_RDONLY, 0)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if file != nil {
		defer func() { err = errors.Join(err, file.Close()) }()
		if err := checkFile(context.Background(), file); err != nil {
			return err
		}
		if err := s.checkIdentity(id, name, dir, file); err != nil {
			return err
		}
		if err := errors.Join(checkLinkedPath(root), checkLinkedAt(root, id, dir), checkLinkedAt(dir, name, file)); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
			return filesystemError("remove file content", err)
		}
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	if err := errors.Join(checkLinkedPath(root), checkLinkedAt(root, id, dir)); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(root.Fd()), id, unix.AT_REMOVEDIR); err != nil {
		return filesystemError("remove empty file directory", err)
	}
	s.identities[id] = &identity{name: name, removed: true}
	return errors.Join(root.Sync(), checkLinkedPath(root))
}

type openedFile struct {
	root *os.File
	dir  *os.File
	file *os.File
}

func (o *openedFile) close() error {
	return errors.Join(o.file.Close(), o.dir.Close(), o.root.Close())
}

func (o *openedFile) linked(id, name string) error {
	return errors.Join(checkLinkedPath(o.root), checkLinkedAt(o.root, id, o.dir), checkLinkedAt(o.dir, name, o.file))
}

func (s *Store) open(ctx context.Context, id, name string, flags int) (_ *openedFile, err error) {
	path, err := s.Path(id, name)
	if err != nil {
		return nil, err
	}
	root, err := s.openRoot(ctx)
	if err != nil {
		return nil, err
	}
	opened := &openedFile{root: root}
	defer func() {
		if err != nil {
			if opened.file != nil {
				opened.file.Close()
			}
			if opened.dir != nil {
				opened.dir.Close()
			}
			root.Close()
		}
	}()
	opened.dir, err = openDirectoryAt(root, id, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if err := s.checkIdentity(id, name, opened.dir, nil); err != nil {
		return nil, err
	}
	if err := checkDirectory(ctx, opened.dir); err != nil {
		return nil, err
	}
	if err := onlyFile(opened.dir, name); err != nil {
		return nil, err
	}
	opened.file, err = openFileAt(opened.dir, name, path, flags, 0)
	if err != nil {
		return nil, err
	}
	if err := checkFile(ctx, opened.file); err != nil {
		return nil, err
	}
	if err := s.remember(id, name, opened.dir, opened.file); err != nil {
		return nil, err
	}
	if err := opened.linked(id, name); err != nil {
		return nil, err
	}
	return opened, nil
}

func (s *Store) openRoot(ctx context.Context) (*os.File, error) {
	root, err := openAbsoluteDirectory(s.root)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat()
	if err == nil && !os.SameFile(info, s.rootInfo) {
		err = domain.Fail(domain.Conflict, "file store root was replaced")
	}
	if err == nil {
		err = checkDirectory(ctx, root)
	}
	if err == nil {
		err = checkLinkedPath(root)
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func (s *Store) checkIdentity(id, name string, dir, file *os.File) error {
	known := s.identities[id]
	if known == nil {
		return nil
	}
	if known.removed || known.name != name {
		return domain.Fail(domain.Conflict, "immutable file ID was removed or has a different name")
	}
	for _, pair := range []struct {
		file *os.File
		info os.FileInfo
	}{{dir, known.directory}, {file, known.file}} {
		if pair.file == nil || pair.info == nil {
			continue
		}
		info, err := pair.file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, pair.info) {
			return domain.Fail(domain.Conflict, "immutable file resource was replaced")
		}
	}
	return nil
}

func (s *Store) remember(id, name string, dir, file *os.File) error {
	if err := s.checkIdentity(id, name, dir, file); err != nil {
		return err
	}
	directory, err := dir.Stat()
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	s.identities[id] = &identity{name: name, directory: directory, file: info}
	return nil
}

func validAbsolutePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return domain.Fail(domain.Invalid, "file store path must be absolute, canonical and free of control characters")
	}
	return nil
}

func openAbsoluteDirectory(path string) (*os.File, error) {
	if err := validAbsolutePath(path); err != nil {
		return nil, err
	}
	path, err := canonicalAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	current, err := os.OpenFile("/", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, filesystemError("open filesystem root", err)
	}
	if path == "/" {
		return current, nil
	}
	currentPath := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		currentPath = filepath.Join(currentPath, part)
		next, err := openDirectoryAt(current, part, currentPath)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func openAbsoluteFile(path string, flags int) (*os.File, error) {
	if err := validAbsolutePath(path); err != nil {
		return nil, err
	}
	path, err := canonicalAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	parent, err := openAbsoluteDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return openFileAt(parent, filepath.Base(path), path, flags, 0)
}

func openDirectoryAt(parent *os.File, name, path string) (*os.File, error) {
	return openFileAt(parent, name, path, os.O_RDONLY|unix.O_DIRECTORY, 0)
}

func openFileAt(parent *os.File, name, path string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, filesystemError("open owned file resource", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func checkDirectory(ctx context.Context, dir *os.File) error {
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0711 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return domain.Fail(domain.Denied, "file store directory must be a real owned 0711 directory")
	}
	if err := secure.CheckOwner(info); err != nil {
		return err
	}
	return checkACL(ctx, dir, true)
}

func checkFile(ctx context.Context, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&^os.FileMode(0640) != 0 ||
		info.Mode().Perm()&0600 != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Size() < 0 || info.Size() > domain.MaxFileSize {
		return domain.Fail(domain.Denied, "file content must be an owned regular non-executable file within 1 GiB")
	}
	if err := secure.CheckOwner(info); err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return domain.Fail(domain.Denied, "file content must have exactly one hard link")
	}
	return checkACL(ctx, file, false)
}

func checkLinkedPath(file *os.File) error {
	info, err := os.Lstat(file.Name())
	if err != nil {
		return filesystemError("inspect pinned file path", err)
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, actual) {
		return domain.Fail(domain.Conflict, "pinned file path was replaced")
	}
	return nil
}

func checkLinkedAt(parent *os.File, name string, file *os.File) error {
	var linked, actual unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &linked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return filesystemError("inspect pinned file resource", err)
	}
	if err := unix.Fstat(int(file.Fd()), &actual); err != nil {
		return err
	}
	if linked.Dev != actual.Dev || linked.Ino != actual.Ino || linked.Mode != actual.Mode {
		return domain.Fail(domain.Conflict, "pinned file resource was replaced")
	}
	return nil
}

func onlyFile(dir *os.File, name string) error {
	names, err := dir.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 1 || len(names) == 1 && names[0] != name {
		return domain.Fail(domain.Conflict, "immutable file directory contains unexpected resources")
	}
	return nil
}

func filesystemError(action string, err error) error {
	var code domain.Code
	switch {
	case errors.Is(err, os.ErrNotExist):
		code = domain.NotFound
	case errors.Is(err, os.ErrExist), errors.Is(err, unix.ENOTEMPTY):
		code = domain.Conflict
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR), errors.Is(err, os.ErrPermission):
		code = domain.Denied
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
	return errors.Join(domain.Fail(code, action+" failed"), err)
}
