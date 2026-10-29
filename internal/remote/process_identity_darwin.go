package remote

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func inspectProcessWithParent(pid int) (*ownedProcess, int, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EIO) && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if info == nil || info.Proc.P_pid == 0 {
		return nil, 0, nil
	}
	return &ownedProcess{PID: pid, Group: int(info.Eproc.Pgid), UID: info.Eproc.Ucred.Uid,
		Started: fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)}, int(info.Eproc.Ppid), nil
}
