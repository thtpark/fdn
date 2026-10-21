package ingress

import (
	"errors"

	"golang.org/x/sys/unix"
)

func kernelPeerUID(fd int) (uint32, error) {
	credential, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	if credential.Version != 0 {
		return 0, errors.New("unsupported Darwin peer credential version")
	}
	return credential.Uid, nil
}
