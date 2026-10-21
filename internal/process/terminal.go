package process

import (
	"errors"
	"os"
	osSignal "os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var terminalControl sync.Mutex

func prepareTerminal(file *os.File) (func() error, error) {
	terminalControl.Lock()
	group, err := unix.IoctlGetInt(int(file.Fd()), unix.TIOCGPGRP)
	if err != nil {
		terminalControl.Unlock()
		return nil, err
	}
	state, err := term.GetState(int(file.Fd()))
	if err != nil {
		terminalControl.Unlock()
		return nil, err
	}
	ignored := osSignal.Ignored(syscall.SIGTTOU)
	// The supervising process must reclaim the terminal after the child exits.
	osSignal.Ignore(syscall.SIGTTOU)
	return func() error {
		defer terminalControl.Unlock()
		err := errors.Join(unix.IoctlSetPointerInt(int(file.Fd()), unix.TIOCSPGRP, group), term.Restore(int(file.Fd()), state))
		if !ignored {
			osSignal.Reset(syscall.SIGTTOU)
		}
		return err
	}, nil
}
