package remote

import (
	"bufio"
	"context"
	"net"
	"os"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

func legacyPeer(conn *net.UnixConn) (*ownedProcess, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Ucred
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, readErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	if credentials.Uid != uint32(os.Geteuid()) || int(credentials.Pid) == os.Getpid() {
		return nil, domain.Fail(domain.Denied, "legacy server peer is not its dedicated installing-user process")
	}
	p, err := inspectProcess(int(credentials.Pid))
	if err != nil {
		return nil, err
	}
	if p == nil || !p.valid() {
		return nil, domain.Fail(domain.Prerequisite, "legacy server lacks a kernel process-start/group identity")
	}
	return p, nil
}

func legacyEndpointUnbound(ctx context.Context, path string) (bool, error) {
	file, err := os.Open("/proc/net/unix")
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if strings.Contains(scanner.Text(), path) {
			return false, nil
		}
	}
	return scanner.Err() == nil, scanner.Err()
}
