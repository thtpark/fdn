package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]{0,252}$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,63}$`)

func Arguments(c config.SSH) ([]string, error) {
	if !hostPattern.MatchString(c.Host) || !userPattern.MatchString(c.User) || c.Port < 1 || c.Port > 65535 || c.LocalPort < 1 || c.LocalPort > 65535 || c.RemotePort < 1 || c.RemotePort > 65535 {
		return nil, domain.Fail(domain.Invalid, "invalid SSH host, user, or port")
	}
	if !filepath.IsAbs(c.KnownHostsFile) || !filepath.IsAbs(c.IdentityFile) {
		return nil, domain.Fail(domain.Invalid, "SSH identity and pinned known_hosts must be absolute")
	}
	if _, err := secure.Read(c.IdentityFile, domain.MaxBody); err != nil {
		return nil, err
	}
	data, err := secure.ReadPublic(c.KnownHostsFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, domain.Fail(domain.Invalid, "pinned known_hosts file must be nonempty and bounded")
	}
	return []string{"-F", "/dev/null", "-N", "-T", "-p", strconv.Itoa(c.Port), "-i", c.IdentityFile,
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + c.KnownHostsFile,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no", "-o", "IdentityAgent=none",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", c.LocalPort, c.RemotePort), "--", c.User + "@" + c.Host}, nil
}

type Tunnel struct {
	cancel context.CancelFunc
	done   <-chan error
}

func Start(ctx context.Context, c config.SSH, runner process.Runner) (*Tunnel, error) {
	args, err := Arguments(c)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(c.LocalPort))
	probe, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		probe.Close()
		return nil, domain.Fail(domain.Conflict, "tunnel port already has a listener; never borrow an unknown connection")
	}
	tunnelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	output := process.NewBuffer(8192)
	go func() {
		err := runner.Run(tunnelCtx, process.Spec{Path: "ssh", Args: args, Env: process.Environment(), Stdout: output, Stderr: output})
		done <- err
		close(done)
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			cancel()
			if err == nil {
				err = domain.Fail(domain.Unavailable, "SSH exited before the tunnel was ready")
			}
			return nil, err
		case <-ctx.Done():
			cancel()
			<-done
			return nil, ctx.Err()
		case <-deadline.C:
			cancel()
			<-done
			return nil, domain.Fail(domain.Unavailable, "pinned SSH tunnel did not become responsive")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return &Tunnel{cancel: cancel, done: done}, nil
			}
		}
	}
}

func (t *Tunnel) Close() error {
	t.cancel()
	err := <-t.done
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (t *Tunnel) Done() <-chan error { return t.done }

func Maintain(ctx context.Context, c config.SSH, runner process.Runner, problem func(error)) error {
	delay := time.Second
	for {
		tunnel, err := Start(ctx, c, runner)
		if err == nil {
			delay = time.Second
			select {
			case <-ctx.Done():
				return tunnel.Close()
			case err = <-tunnel.Done():
				if err == nil {
					err = domain.Fail(domain.Unavailable, "SSH tunnel exited; reconnecting with the same pinned identity")
				}
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if problem != nil {
			problem(err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
