package remote

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func inspectProcess(pid int) (*ownedProcess, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EIO) && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info == nil || info.Proc.P_pid == 0 {
		return nil, nil
	}
	return &ownedProcess{PID: pid, Group: int(info.Eproc.Pgid), UID: info.Eproc.Ucred.Uid,
		Started: fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)}, nil
}
