package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func StreamArguments(c config.SSH, mode string) ([]string, error) {
	if mode != "fdn-api" && mode != "fdn-join" {
		return nil, domain.Fail(domain.Invalid, "SSH stream mode must be fdn-api or fdn-join")
	}
	if !hostPattern.MatchString(c.Host) || !userPattern.MatchString(c.User) || c.Port < 1 || c.Port > 65535 {
		return nil, domain.Fail(domain.Invalid, "invalid SSH stream host, user, or port")
	}
	for _, path := range []string{c.IdentityFile, c.KnownHostsFile} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n%$") {
			return nil, domain.Fail(domain.Invalid, "SSH identity and host pins must use absolute literal paths")
		}
	}
	private, err := secure.Read(c.IdentityFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(private)) == 0 {
		return nil, domain.Fail(domain.Invalid, "SSH identity file must be nonempty")
	}
	pins, err := secure.ReadPublic(c.KnownHostsFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(pins)) == 0 {
		return nil, domain.Fail(domain.Invalid, "SSH pinned known_hosts must be nonempty")
	}
	knownHosts := c.KnownHostsFile
	if strings.ContainsAny(knownHosts, " \t\"\\") {
		knownHosts = "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(knownHosts) + "\""
	}
	return []string{"-F", "/dev/null", "-T", "-p", strconv.Itoa(c.Port), "-i", c.IdentityFile,
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "AddKeysToAgent=no",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no", "-o", "VerifyHostKeyDNS=no",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "PermitLocalCommand=no",
		"-o", "ProxyCommand=none", "-o", "ProxyJump=none", "-o", "RequestTTY=no", "-o", "EscapeChar=none",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "PreferredAuthentications=publickey",
		"-o", "GSSAPIAuthentication=no", "-o", "HostbasedAuthentication=no", "-o", "SendEnv=-*",
		"-o", "ConnectionAttempts=1", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"--", c.User + "@" + c.Host, mode}, nil
}

type stdioConn struct {
	reader *os.File
	writer *os.File
}

func (c *stdioConn) Read(data []byte) (int, error)  { return c.reader.Read(data) }
func (c *stdioConn) Write(data []byte) (int, error) { return c.writer.Write(data) }
func (c *stdioConn) Close() error                   { return errors.Join(c.reader.Close(), c.writer.Close()) }
func (c *stdioConn) LocalAddr() net.Addr            { return streamAddress("local") }
func (c *stdioConn) RemoteAddr() net.Addr           { return streamAddress("ssh") }
func (c *stdioConn) SetDeadline(deadline time.Time) error {
	return errors.Join(c.reader.SetReadDeadline(deadline), c.writer.SetWriteDeadline(deadline))
}
func (c *stdioConn) SetReadDeadline(deadline time.Time) error {
	return c.reader.SetReadDeadline(deadline)
}
func (c *stdioConn) SetWriteDeadline(deadline time.Time) error {
	return c.writer.SetWriteDeadline(deadline)
}

type streamAddress string

func (a streamAddress) Network() string { return "pipe" }
func (a streamAddress) String() string  { return string(a) }

type streamConn struct {
	net.Conn
	input     *os.File
	output    *os.File
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	err       error
}

func OpenStream(ctx context.Context, c config.SSH, mode string, runner process.Runner) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, err := StreamArguments(c, mode)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		return nil, domain.Fail(domain.Invalid, "SSH stream requires a process runner")
	}
	// File-backed stdio lets exec observe SSH exit without waiting for an
	// unrelated stdin copy blocked on a net.Pipe reader.
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	reader, output, err := os.Pipe()
	if err != nil {
		input.Close()
		writer.Close()
		return nil, err
	}
	client := &stdioConn{reader: reader, writer: writer}
	streamCtx, cancel := context.WithCancel(ctx)
	conn := &streamConn{Conn: client, input: input, output: output, cancel: cancel, done: make(chan struct{})}
	started := make(chan struct{}, 1)
	stderr := process.NewBuffer(8192)
	stop := context.AfterFunc(streamCtx, func() {
		client.Close()
		input.Close()
		output.Close()
	})
	go func() {
		err := runner.Run(streamCtx, process.Spec{Path: "/usr/bin/ssh", Args: args,
			Env:   []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"},
			Stdin: input, Stdout: output, Stderr: stderr,
			Started: func(int) error {
				select {
				case started <- struct{}{}:
				default:
				}
				return streamCtx.Err()
			}})
		var cleanup *process.CleanupError
		switch {
		case errors.As(err, &cleanup):
			conn.err = domain.Fail(domain.Unavailable, "SSH stream owned-process cleanup failed")
		case streamCtx.Err() != nil:
			conn.err = streamCtx.Err()
		case err != nil:
			conn.err = domain.Fail(domain.Unavailable, "pinned SSH stream process failed")
		default:
			conn.err = io.EOF
		}
		if _, err := stderr.Bytes(); err != nil && streamCtx.Err() == nil && cleanup == nil {
			conn.err = domain.Fail(domain.Unavailable, "SSH stream stderr exceeded its private size limit")
		}
		stop()
		input.Close()
		output.Close()
		cancel()
		close(conn.done)
	}()
	startup := time.NewTimer(10 * time.Second)
	defer startup.Stop()
	select {
	case <-started:
		select {
		case <-conn.done:
			conn.Close()
			return nil, streamStartError(conn.err)
		default:
			if err := ctx.Err(); err != nil {
				conn.Close()
				return nil, err
			}
			return conn, nil
		}
	case <-conn.done:
		conn.Close()
		return nil, streamStartError(conn.err)
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	case <-startup.C:
		conn.Close()
		return nil, domain.Fail(domain.Unavailable, "pinned SSH stream process did not start")
	}
}

func streamStartError(err error) error {
	if errors.Is(err, io.EOF) {
		return domain.Fail(domain.Unavailable, "SSH exited before the stream was ready")
	}
	return err
}

func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.Conn.Close()
		c.input.Close()
		c.output.Close()
		<-c.done
		if !errors.Is(c.err, context.Canceled) && !errors.Is(c.err, context.DeadlineExceeded) && !errors.Is(c.err, io.EOF) {
			c.closeErr = c.err
		}
	})
	return c.closeErr
}
