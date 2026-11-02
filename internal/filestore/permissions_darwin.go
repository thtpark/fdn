package filestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

const nativeTimeout = 10 * time.Second

type nativePermissions struct{ runner process.Runner }

func newNativePermissions() Permissions { return nativePermissions{runner: process.Native{}} }

func canonicalAbsolutePath(path string) (string, error) {
	if path != "/var" && !strings.HasPrefix(path, "/var/") {
		return path, nil
	}
	info, err := os.Lstat("/var")
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	target, err := os.Readlink("/var")
	if err != nil || !ok || stat.Uid != 0 || target != "private/var" {
		return "", domain.Fail(domain.Denied, "macOS var alias is not the expected root-owned system alias")
	}
	for _, ancestor := range []string{"/", "/private", "/private/var"} {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return "", err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return "", domain.Fail(domain.Denied, "macOS var alias has an unsafe system ancestor")
		}
	}
	// Only the fixed OS alias is normalized. Task-controlled components still
	// go through descriptor-relative O_NOFOLLOW opens, never EvalSymlinks.
	return "/private/var" + strings.TrimPrefix(path, "/var"), nil
}

func descriptorPath(file *os.File) string {
	return "/dev/fd/" + strconv.FormatUint(uint64(file.Fd()), 10)
}

func (p nativePermissions) applyFile(ctx context.Context, file *os.File, uids []uint32) error {
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	readers, err := readerUIDs(uids)
	if err != nil {
		return err
	}
	if len(readers) > 128 {
		return domain.Fail(domain.Invalid, "file readers exceed the macOS 128-entry ACL limit")
	}
	names := make([]string, 0, len(readers))
	for _, uid := range readers {
		id := strconv.FormatUint(uint64(uid), 10)
		output, err := p.run(ctx, nil, "/usr/bin/id", "-nu", id)
		name := strings.TrimSpace(string(output))
		if err != nil || !safeAccountName(name) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return domain.Fail(domain.Prerequisite, "file reader UID does not resolve to an existing OS account")
		}
		output, err = p.run(ctx, nil, "/usr/bin/id", "-u", name)
		if err != nil || strings.TrimSpace(string(output)) != id {
			if err := ctx.Err(); err != nil {
				return err
			}
			return domain.Fail(domain.Prerequisite, "file reader OS account does not resolve to the exact UID")
		}
		names = append(names, name)
	}
	if err := qualifyReaderTraversal(ctx, file, readers); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("retain owner-only file mode: %w", err)
	}
	// stdin carries the pinned file descriptor into each bounded native process.
	// A parent's /dev/fd/N is not inherited by exec and must not be used here.
	if _, err := p.run(ctx, file, "/bin/chmod", "-N", "/dev/fd/0"); err != nil {
		return fmt.Errorf("clear native file ACL (macOS ACL support required): %w", err)
	}
	for _, name := range names {
		if _, err := p.run(ctx, file, "/bin/chmod", "+a", "user:"+name+" allow read", "/dev/fd/0"); err != nil {
			return fmt.Errorf("apply exact native file reader ACL: %w", err)
		}
	}
	actual, err := p.inspect(ctx, file)
	if err != nil {
		return err
	}
	slices.Sort(names)
	slices.Sort(actual)
	if !slices.Equal(names, actual) {
		return domain.Fail(domain.Conflict, "native file ACL did not retain the exact readers")
	}
	return ctx.Err()
}

func initializeACL(ctx context.Context, file *os.File, directory bool) error {
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	p := nativePermissions{runner: process.Native{}}
	if _, err := p.run(ctx, file, "/bin/chmod", "-N", "/dev/fd/0"); err != nil {
		return fmt.Errorf("clear inherited macOS ACL: %w", err)
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0711
	}
	return file.Chmod(mode)
}

func checkACL(ctx context.Context, file *os.File, directory bool) error {
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	readers, err := (nativePermissions{runner: process.Native{}}).inspect(ctx, file)
	if err != nil {
		return err
	}
	if directory && len(readers) != 0 {
		return domain.Fail(domain.Denied, "file store directories must not carry native ACL grants")
	}
	if !directory {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0600 {
			return domain.Fail(domain.Denied, "macOS file mode must remain owner-only beneath named reader ACLs")
		}
	}
	return nil
}

func (p nativePermissions) inspect(ctx context.Context, file *os.File) ([]string, error) {
	if err := checkLinkedPath(file); err != nil {
		return nil, err
	}
	// fdescfs reports synthetic modes and omits ACLs. Inspect the canonical name
	// read-only, checking it still identifies the pinned inode on both sides.
	output, err := p.run(ctx, nil, "/bin/ls", "-Llde", file.Name())
	if err != nil {
		return nil, fmt.Errorf("inspect native macOS file ACL: %w", err)
	}
	if err := checkLinkedPath(file); err != nil {
		return nil, err
	}
	return parseDarwinACL(output)
}

func parseDarwinACL(output []byte) ([]string, error) {
	lines, err := darwinListing(output)
	if err != nil {
		return nil, err
	}
	var readers []string
	for index, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != strconv.Itoa(index)+":" ||
			!strings.HasPrefix(fields[1], "user:") || fields[2] != "allow" || fields[3] != "read" {
			return nil, domain.Fail(domain.Denied, "file ACL must contain only named user read grants")
		}
		name := strings.TrimPrefix(fields[1], "user:")
		if !safeAccountName(name) || slices.Contains(readers, name) {
			return nil, domain.Fail(domain.Denied, "file ACL contains an unresolved or duplicate OS account")
		}
		readers = append(readers, name)
	}
	return readers, nil
}

func darwinListing(output []byte) ([]string, error) {
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	header := strings.Fields(lines[0])
	if len(header) == 0 || len(header[0]) < 10 || len(header[0]) > 11 ||
		(header[0][0] != '-' && header[0][0] != 'd') || len(lines) > 129 {
		return nil, domain.Fail(domain.Denied, "native macOS ACL listing is malformed")
	}
	hasACL, hasXattrs := strings.HasSuffix(header[0], "+"), strings.HasSuffix(header[0], "@")
	if hasACL && len(lines) == 1 || len(lines) > 1 && !hasACL && !hasXattrs {
		return nil, domain.Fail(domain.Denied, "native macOS ACL listing is incomplete")
	}
	return lines, nil
}

func safeAccountName(name string) bool {
	return name != "" && len(name) <= 255 && !strings.HasPrefix(name, "-") &&
		!strings.ContainsAny(name, ":/,\\") &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func checkTraversalACL(ctx context.Context, dir *os.File, _ []uint32) error {
	ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
	defer cancel()
	p := nativePermissions{runner: process.Native{}}
	if err := checkLinkedPath(dir); err != nil {
		return err
	}
	output, err := p.run(ctx, nil, "/bin/ls", "-Llde", dir.Name())
	if err != nil {
		return fmt.Errorf("inspect macOS reader ancestor ACL: %w", err)
	}
	if err := checkLinkedPath(dir); err != nil {
		return err
	}
	lines, err := darwinListing(output)
	if err != nil {
		return err
	}
	for index, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != strconv.Itoa(index)+":" ||
			(fields[2] != "allow" && fields[2] != "deny") {
			return domain.Fail(domain.Prerequisite, "an ancestor ACL prevents conservative proof of reader traversal")
		}
		if fields[2] == "deny" {
			for _, permission := range strings.Split(strings.Join(fields[3:], ","), ",") {
				if permission == "search" || permission == "execute" || permission == "all" {
					return domain.Fail(domain.Prerequisite, "an ancestor ACL may deny file reader traversal")
				}
			}
		}
	}
	return nil
}

func (p nativePermissions) run(ctx context.Context, file *os.File, path string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output, diagnostic := process.NewBuffer(65536), process.NewBuffer(4096)
	spec := process.Spec{
		Path: path, Args: args, Stdin: file, Stdout: output, Stderr: diagnostic,
		Env: process.Environment("LC_ALL=C"),
	}
	// Avoid an interface containing a nil *os.File for non-file commands.
	if file == nil {
		spec.Stdin = nil
	}
	runErr := p.runner.Run(ctx, spec)
	data, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	return data, errors.Join(runErr, outputErr, diagnosticErr)
}
