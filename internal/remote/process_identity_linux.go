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

func inspectProcess(pid int) (*ownedProcess, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, domain.Fail(domain.Prerequisite, "kernel process ownership is unavailable")
	}
	data, err := os.ReadFile(filepath.Join(path, "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return nil, domain.Fail(domain.Prerequisite, "kernel process identity is malformed")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return nil, domain.Fail(domain.Prerequisite, "kernel process identity is incomplete")
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, err
	}
	return &ownedProcess{PID: pid, Group: group, UID: owner.Uid, Started: fields[19]}, nil
}
