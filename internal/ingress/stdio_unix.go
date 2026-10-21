//go:build linux || darwin

package ingress

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Inherited os.Stdin/os.Stdout may be blocking descriptors unregistered with
// Go's poller. Closing such a descriptor does NOT interrupt another goroutine's
// read. A nonblocking duplicate must be registered before starting either copy.
func interruptibleProxyFile(file *os.File) (*os.File, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return nil, errors.New("gateway stdio must be non-PTY SSH pipes or sockets")
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	pollable := os.NewFile(uintptr(fd), "fdn-gateway-stdio")
	if pollable == nil {
		_ = unix.Close(fd)
		return nil, os.ErrInvalid
	}
	if err := pollable.SetDeadline(time.Time{}); err != nil {
		_ = pollable.Close()
		return nil, errors.New("gateway stdio cannot be interrupted safely")
	}
	return pollable, nil
}
