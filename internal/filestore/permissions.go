package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

// Permissions replaces all named readers of the supplied file. The owner keeps
// read/write access; named readers receive read only, never group/world access.
type Permissions interface {
	Apply(context.Context, string, []uint32) error
}

// PermissionFunc adapts isolated business fixtures to Permissions.
type PermissionFunc func(context.Context, string, []uint32) error

func (f PermissionFunc) Apply(ctx context.Context, path string, uids []uint32) error {
	if f == nil {
		return domain.Fail(domain.Prerequisite, "file permissions adapter is nil")
	}
	return f(ctx, path, uids)
}

// NativePermissions returns the platform ACL implementation. Unknown UIDs,
// unsupported ACLs and failed native operations are errors, never broad grants.
func NativePermissions() Permissions { return newNativePermissions() }

const maxReaders = (65536-4)/8 - 4

func readerUIDs(uids []uint32) ([]uint32, error) {
	if len(uids) > maxReaders {
		return nil, domain.Fail(domain.Invalid, "file reader list exceeds the native ACL size bound")
	}
	readers := slices.Clone(uids)
	slices.Sort(readers)
	readers = slices.Compact(readers)
	if slices.Contains(readers, ^uint32(0)) {
		return nil, domain.Fail(domain.Invalid, "file reader UID is not a valid OS account UID")
	}
	return readers, nil
}

func (p nativePermissions) Apply(ctx context.Context, path string, uids []uint32) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := openAbsoluteFile(path, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := checkFile(ctx, file); err != nil {
		return err
	}
	if err := p.applyFile(ctx, file, uids); err != nil {
		return err
	}
	return errors.Join(file.Sync(), checkLinkedPath(file))
}

func qualifyReaderTraversal(ctx context.Context, file *os.File, readers []uint32) error {
	if len(readers) == 0 {
		return nil
	}
	path, err := canonicalAbsolutePath(filepath.Dir(file.Name()))
	if err != nil {
		return err
	}
	current, err := os.OpenFile("/", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return filesystemError("open file reader ancestor", err)
	}
	defer func() { current.Close() }()
	parts := []string{""}
	if path != "/" {
		parts = append(parts, strings.Split(strings.TrimPrefix(path, "/"), "/")...)
	}
	currentPath := "/"
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if part != "" {
			currentPath = filepath.Join(currentPath, part)
			next, err := openDirectoryAt(current, part, currentPath)
			if err != nil {
				return err
			}
			current.Close()
			current = next
		}
		info, err := current.Stat()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return domain.Fail(domain.Prerequisite, "file reader ancestor ownership cannot be established")
		}
		for _, uid := range readers {
			if uid == stat.Uid {
				if info.Mode().Perm()&0100 != 0 {
					continue
				}
			} else if info.Mode().Perm()&0011 == 0011 {
				// Requiring both classes avoids assuming a reader's groups.
				continue
			}
			return domain.Fail(domain.Prerequisite, "file reader cannot provably traverse every ancestor; private directories are not broadened")
		}
		if err := checkTraversalACL(ctx, current, readers); err != nil {
			return err
		}
	}
	return nil
}
