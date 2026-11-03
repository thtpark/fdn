package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
)

func inspectProcessWithParent(pid int) (*ownedProcess, int, error) {
	return inspectProcProcess(pid, os.Stat, os.ReadFile)
}

func inspectProcProcess(pid int, stat func(string) (os.FileInfo, error), readFile func(string) ([]byte, error)) (*ownedProcess, int, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := stat(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, 0, domain.Fail(domain.Prerequisite, "kernel process ownership is unavailable")
	}
	data, err := readFile(filepath.Join(path, "stat"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return nil, 0, domain.Fail(domain.Prerequisite, "kernel process identity is malformed")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return nil, 0, domain.Fail(domain.Prerequisite, "kernel process identity is incomplete")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, 0, err
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil, 0, err
	}
	return &ownedProcess{PID: pid, Group: group, UID: owner.Uid, Started: fields[19]}, parent, nil
}
