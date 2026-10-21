//go:build darwin

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
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		credentialErr = err
		if err == nil {
			if credentials.Ngroups < 1 {
				credentialErr = domain.Fail(domain.Denied, "Unix peer did not supply group credentials")
				return
			}
			peer = Peer{UID: int(credentials.Uid), GID: int(credentials.Groups[0])}
		}
	}); err != nil {
		return Peer{}, err
	}
	return peer, credentialErr
}
