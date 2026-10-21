package identityservice

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Peer struct {
	UID int
	GID int
}

// Boundary is injectable by embedded/rootless tests, not configurable at runtime.
// The native command always uses NativeBoundary with kernel peer credentials.
type Boundary interface {
	Listen(config.IdentityService) (net.Listener, error)
	Peer(net.Conn) (Peer, error)
}

type NativeBoundary struct{}

// CheckNativeAccount forbids the provider account and root from owning a native
// identity service. Privileged installation is a separate administrator action.
func CheckNativeAccount(c config.IdentityService) error {
	if err := validateSettings(c); err != nil {
		return err
	}
	if os.Geteuid() == 0 || c.WorkerUID == 0 || os.Geteuid() == c.WorkerUID {
		return domain.Fail(domain.Denied, "identity service must run as a dedicated non-root account distinct from the tool-runner UID")
	}
	return nil
}

func (NativeBoundary) Listen(c config.IdentityService) (net.Listener, error) {
	if err := CheckNativeAccount(c); err != nil {
		return nil, err
	}
	groups, err := os.Getgroups()
	if err != nil {
		return nil, domain.Fail(domain.Denied, "cannot verify identity account IPC group membership")
	}
	member := os.Getegid() == c.WorkerGID
	for _, group := range groups {
		member = member || group == c.WorkerGID
	}
	if !member {
		return nil, domain.Fail(domain.Denied, "identity account must already belong to the configured worker group to publish IPC")
	}
	if err := secure.Dir(c.StateDir); err != nil {
		return nil, err
	}
	dir := filepath.Dir(c.Socket)
	if err := checkAncestors(dir); err != nil {
		return nil, err
	}
	created := false
	if err := os.Mkdir(dir, 0710); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if created {
		if err := os.Chown(dir, -1, c.WorkerGID); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0710); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0710 || int(stat.Gid) != c.WorkerGID {
		return nil, domain.Fail(domain.Denied, "identity socket directory must be identity-owned 0710 with the configured worker group")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, err
	}
	if stale, err := os.Lstat(c.Socket); err == nil {
		if stale.Mode()&os.ModeSocket == 0 || secure.CheckOwner(stale) != nil {
			return nil, domain.Fail(domain.Denied, "refusing to replace an unknown identity socket path")
		}
		if connection, err := net.DialTimeout("unix", c.Socket, 200*time.Millisecond); err == nil {
			connection.Close()
			return nil, domain.Fail(domain.Conflict, "identity socket is already in use")
		}
		if err := os.Remove(c.Socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	address := &net.UnixAddr{Name: c.Socket, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chown(c.Socket, -1, c.WorkerGID); err != nil {
		listener.Close()
		os.Remove(c.Socket)
		return nil, err
	}
	if err := os.Chmod(c.Socket, 0660); err != nil {
		listener.Close()
		os.Remove(c.Socket)
		return nil, err
	}
	info, err = os.Lstat(c.Socket)
	if err != nil {
		listener.Close()
		return nil, err
	}
	return &ownedListener{UnixListener: listener, path: c.Socket, info: info}, nil
}

func (NativeBoundary) Peer(connection net.Conn) (Peer, error) {
	return kernelPeer(connection)
}

type ownedListener struct {
	*net.UnixListener
	path string
	info os.FileInfo
	once sync.Once
	err  error
}

func (l *ownedListener) Close() error {
	l.once.Do(func() {
		l.err = l.UnixListener.Close()
		if current, err := os.Lstat(l.path); err == nil && os.SameFile(current, l.info) {
			l.err = errors.Join(l.err, os.Remove(l.path))
		}
	})
	return l.err
}

type peerConnection struct {
	net.Conn
	peer Peer
}

type authorizedListener struct {
	net.Listener
	boundary Boundary
	uid, gid int
}

func (l authorizedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, err := l.boundary.Peer(connection)
		if err != nil || peer.UID != l.uid || peer.GID != l.gid {
			connection.Close()
			continue
		}
		return &peerConnection{Conn: connection, peer: peer}, nil
	}
}
