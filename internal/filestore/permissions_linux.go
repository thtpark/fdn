package filestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

const accessACL = "system.posix_acl_access"
const defaultACL = "system.posix_acl_default"

type nativePermissions struct{}

func newNativePermissions() Permissions { return nativePermissions{} }

func canonicalAbsolutePath(path string) (string, error) { return path, nil }

func descriptorPath(file *os.File) string {
	return "/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10)
}

func (nativePermissions) applyFile(ctx context.Context, file *os.File, uids []uint32) error {
	readers, err := readerUIDs(uids)
	if err != nil {
		return err
	}
	for _, uid := range readers {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := strconv.FormatUint(uint64(uid), 10)
		account, err := user.LookupId(id)
		if err != nil || account.Uid != id {
			return domain.Fail(domain.Prerequisite, "file reader UID does not resolve to an existing OS account")
		}
	}
	if err := qualifyReaderTraversal(ctx, file, readers); err != nil {
		return err
	}
	data, err := encodeAccessACL(readers)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fsetxattr(int(file.Fd()), accessACL, data, 0); err != nil {
		return fmt.Errorf("apply exact native file ACL (POSIX ACL support required): %w", err)
	}
	if err := checkACL(ctx, file, false); err != nil {
		return err
	}
	actual, err := readACL(file, accessACL)
	if err != nil {
		return err
	}
	var observed []uint32
	if len(actual) != 0 {
		observed, err = decodeAccessACL(actual)
		if err != nil {
			return err
		}
	}
	if len(observed) != len(readers) {
		return domain.Fail(domain.Conflict, "native file ACL did not retain the exact readers")
	}
	for i := range readers {
		if observed[i] != readers[i] {
			return domain.Fail(domain.Conflict, "native file ACL did not retain the exact readers")
		}
	}
	return ctx.Err()
}

func initializeACL(ctx context.Context, file *os.File, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keys := []string{accessACL}
	mode := os.FileMode(0600)
	if directory {
		keys = append(keys, defaultACL)
		mode = 0711
	}
	for _, key := range keys {
		err := unix.Fremovexattr(int(file.Fd()), key)
		if err != nil && !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
			return fmt.Errorf("clear inherited native file ACL: %w", err)
		}
	}
	return file.Chmod(mode)
}

func checkACL(ctx context.Context, file *os.File, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := readACL(file, accessACL)
	if err != nil {
		return err
	}
	if directory {
		defaults, err := readACL(file, defaultACL)
		if err != nil {
			return err
		}
		if len(data) != 0 || len(defaults) != 0 {
			return domain.Fail(domain.Denied, "file store directories must not carry access or inherited ACL grants")
		}
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		if info.Mode().Perm() != 0600 {
			return domain.Fail(domain.Denied, "group-readable file requires a named-users-only POSIX ACL")
		}
		return nil
	}
	readers, err := decodeAccessACL(data)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if len(readers) != 0 {
		mode = 0640 // These are ACL mask bits, not an owning-group read grant.
	}
	if info.Mode().Perm() != mode {
		return domain.Fail(domain.Denied, "file mode and named-users-only POSIX ACL differ")
	}
	return nil
}

func readACL(file *os.File, key string) ([]byte, error) {
	size, err := unix.Fgetxattr(int(file.Fd()), key, nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect native file ACL: %w", err)
	}
	if size <= 0 || size > 65536 {
		return nil, domain.Fail(domain.Denied, "native file ACL exceeds its size bound")
	}
	data := make([]byte, size)
	n, err := unix.Fgetxattr(int(file.Fd()), key, data)
	if err != nil {
		return nil, fmt.Errorf("read native file ACL: %w", err)
	}
	if n != size {
		return nil, domain.Fail(domain.Conflict, "native file ACL changed during inspection")
	}
	return data, nil
}

func checkTraversalACL(ctx context.Context, dir *os.File, readers []uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return domain.Fail(domain.Prerequisite, "file reader ancestor ownership cannot be established")
	}
	data, err := readACL(dir, accessACL)
	if err != nil {
		return err
	}
	if len(data) != 0 {
		for _, uid := range readers {
			if uid != stat.Uid {
				return domain.Fail(domain.Prerequisite, "an ancestor POSIX ACL prevents conservative proof of reader traversal")
			}
		}
	}
	return nil
}
