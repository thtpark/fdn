package remote

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"golang.org/x/sys/unix"
)

func legacyPeer(conn *net.UnixConn) (*ownedProcess, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var pid int
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		var credentials *unix.Xucred
		credentials, readErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if readErr == nil && credentials.Uid != uint32(os.Geteuid()) {
			readErr = domain.Fail(domain.Denied, "legacy server peer belongs to another OS account")
		}
		if readErr == nil {
			pid, readErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		}
	}); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	p, err := inspectProcess(pid)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.valid() || p.PID == os.Getpid() {
		return nil, domain.Fail(domain.Prerequisite, "legacy server lacks a dedicated kernel process-start/group identity")
	}
	return p, nil
}

func legacyEndpointUnbound(ctx context.Context, path string) (bool, error) {
	output, diagnostic := process.NewBuffer(8<<20), process.NewBuffer(4096)
	err := (process.Native{}).Run(ctx, process.Spec{Path: "/usr/sbin/lsof",
		Args: []string{"-nP", "-a", "-u", strconv.Itoa(os.Geteuid()), "-U", "-F0n"},
		Env:  process.Environment(), Stdout: output, Stderr: diagnostic})
	data, outputErr := output.Bytes()
	errorsData, diagnosticErr := diagnostic.Bytes()
	if outputErr != nil || diagnosticErr != nil {
		return false, errors.Join(outputErr, diagnosticErr)
	}
	if len(errorsData) != 0 {
		return false, domain.Fail(domain.Prerequisite, "kernel Unix endpoint inspection reported incomplete observations")
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(data) != 0 || len(errorsData) != 0 {
			return false, errors.Join(domain.Fail(domain.Prerequisite, "kernel Unix endpoint inspection is unavailable"), err)
		}
	}
	for _, field := range bytes.Split(data, []byte{0}) {
		field = bytes.TrimLeft(field, "\n")
		if len(field) != 0 && field[0] == 'n' && bytes.Contains(field[1:], []byte(path)) {
			return false, nil
		}
	}
	return true, nil
}
