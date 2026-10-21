package ingress

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Listen creates a Unix stream socket without replacing any existing entry.
// Its immediate parent may be created; all higher ancestors must already exist,
// be real directories owned by root or the listener owner, and not be writable
// by another account. Symlinks, including symlink ancestors, are rejected.
//
// groupID == -1 requires a private 0700 directory and a 0600 socket. An explicit
// group requires a protected 0710/0750 directory of that group, traversable
// ancestors, and a 0660 socket. The owner alone may modify the directory.
func Listen(path string, groupID int) (net.Listener, error) {
	if err := socketPath(path); err != nil {
		return nil, err
	}
	if groupID < -1 || int64(groupID) > 1<<31-1 {
		return nil, errors.New("invalid ingress group ID")
	}
	owner := uint32(os.Geteuid())
	directory := filepath.Dir(path)
	if err := protectedParents(filepath.Dir(directory), owner, groupID); err != nil {
		return nil, err
	}
	mode := os.FileMode(0700)
	if groupID >= 0 {
		mode = 0710
	}
	if err := os.Mkdir(directory, mode); err == nil {
		if groupID >= 0 {
			if err := os.Chown(directory, -1, groupID); err != nil {
				return nil, fmt.Errorf("set ingress directory group: %w", err)
			}
		}
		if err := os.Chmod(directory, mode); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := protectedSocketDirectory(directory, owner, groupID); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("ingress socket path already exists; refusing replacement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	owned := &protectedListener{UnixListener: listener, path: path, info: info}
	fail := func(err error) (net.Listener, error) {
		return nil, errors.Join(err, owned.Close())
	}
	if groupID >= 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fail(errors.New("unsupported ingress socket ownership metadata"))
		}
		if stat.Gid != uint32(groupID) {
			if err := os.Lchown(path, -1, groupID); err != nil {
				return fail(err)
			}
		}
		mode = 0660
	} else {
		mode = 0600
	}
	if err := os.Chmod(path, mode); err != nil {
		return fail(err)
	}
	if err := protectedSocketDirectory(directory, owner, groupID); err != nil {
		return fail(err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return fail(errors.New("ingress socket changed during creation"))
	}
	if _, err := checkedSocket(path, owner); err != nil {
		return fail(err)
	}
	return owned, nil
}

type protectedListener struct {
	*net.UnixListener
	path string
	info os.FileInfo
	once sync.Once
	err  error
}

func (l *protectedListener) Close() error {
	l.once.Do(func() {
		l.err = l.UnixListener.Close()
		info, err := os.Lstat(l.path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			l.err = errors.Join(l.err, err)
			return
		}
		if os.SameFile(l.info, info) && info.Mode()&os.ModeSocket != 0 {
			l.err = errors.Join(l.err, os.Remove(l.path))
		}
	})
	return l.err
}

func socketPath(path string) error {
	// Darwin's sockaddr_un is the smaller of the supported native layouts.
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 103 ||
		path == "/" || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("ingress socket must be a bounded, absolute, canonical filesystem path")
	}
	return nil
}

func protectedParents(path string, owner uint32, groupID int) error {
	current := "/"
	for _, component := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...) {
		if component != "" {
			current = filepath.Join(current, component)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode().Perm()&0022 != 0 || (stat.Uid != 0 && stat.Uid != owner) {
			return fmt.Errorf("unprotected ingress ancestor: %s", current)
		}
		if groupID >= 0 && info.Mode().Perm()&0001 == 0 &&
			!(stat.Gid == uint32(groupID) && info.Mode().Perm()&0010 != 0) {
			return fmt.Errorf("ingress ancestor is not traversable by its group: %s", current)
		}
	}
	return nil
}

func protectedSocketDirectory(path string, owner uint32, groupID int) error {
	if err := protectedParents(filepath.Dir(path), owner, groupID); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != owner {
		return errors.New("ingress directory must be real and owned by the listener account")
	}
	permissions := info.Mode().Perm()
	if groupID == -1 {
		if permissions != 0700 {
			return errors.New("private ingress directory must have mode 0700")
		}
	} else if stat.Gid != uint32(groupID) || (permissions != 0710 && permissions != 0750) {
		return errors.New("shared ingress directory must have its explicit group and mode 0710 or 0750")
	}
	return nil
}

func checkedSocket(path string, owner uint32) (os.FileInfo, error) {
	if err := socketPath(path); err != nil {
		return nil, err
	}
	if err := protectedParents(filepath.Dir(path), owner, -1); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != owner ||
		(info.Mode().Perm() != 0600 && info.Mode().Perm() != 0660) {
		return nil, errors.New("ingress endpoint must be an owner-bound private or ingress-group Unix socket")
	}
	return info, nil
}
