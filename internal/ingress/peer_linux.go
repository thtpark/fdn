package ingress

import (
	"errors"

	"golang.org/x/sys/unix"
)

func kernelPeerUID(fd int) (uint32, error) {
	credential, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	if credential.Pid <= 0 || credential.Uid == ^uint32(0) {
		return 0, errors.New("Linux Unix socket has no connected peer credentials")
	}
	return credential.Uid, nil
}
