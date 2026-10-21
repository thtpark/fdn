//go:build linux

package identityservice

import (
	"net"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

func kernelPeer(connection net.Conn) (Peer, error) {
	local, ok := connection.(*net.UnixConn)
	if !ok {
		return Peer{}, domain.Fail(domain.Denied, "identity IPC requires a Unix connection")
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var peer Peer
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		credentialErr = err
		if err == nil {
			peer = Peer{UID: int(credentials.Uid), GID: int(credentials.Gid)}
		}
	}); err != nil {
		return Peer{}, err
	}
	return peer, credentialErr
}
