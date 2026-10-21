package ingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

var (
	ErrSelector    = errors.New("SSH original command must be exactly fdn-api or fdn-join")
	ErrProxyLimit  = errors.New("ingress gateway stream exceeded its byte limit")
	ErrIdleTimeout = errors.New("ingress gateway idle deadline exceeded")
)

// ProxyOptions is trusted, root-owned gateway configuration, not SSH input.
// ServerUID must identify the service account owning both local endpoints.
// Zero limits select finite defaults: 5s dial, 1m idle, 1h lifetime, and 64 MiB
// in each direction. No caller-supplied dialer, network, command or port is used.
type ProxyOptions struct {
	APISocket   string        `json:"api_socket"`
	JoinSocket  string        `json:"join_socket"`
	ServerUID   uint32        `json:"server_uid"`
	DialTimeout time.Duration `json:"dial_timeout,omitempty"`
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`
	MaxDuration time.Duration `json:"max_duration,omitempty"`
	MaxBytes    int64         `json:"max_bytes,omitempty"`
}

// Proxy owns input and output and closes them before returning. Native SSH pipe
// or socket files are duplicated into pollable descriptors. Other Close methods
// must interrupt pending I/O (as io.Pipe and net.Conn do). Both copy workers are
// joined, including on cancellation and failure.
// Original commands are compared byte-for-byte; no parsing or shell is used.
func Proxy(ctx context.Context, originalCommand string, input io.ReadCloser, output io.WriteCloser, options ProxyOptions) error {
	if input == nil || output == nil {
		return errors.New("ingress gateway requires closable SSH input and output")
	}
	defer input.Close()
	defer output.Close()
	var path string
	switch originalCommand {
	case "fdn-api":
		path = options.APISocket
	case "fdn-join":
		path = options.JoinSocket
	default:
		return ErrSelector
	}
	if err := options.validate(); err != nil {
		return err
	}
	before, err := checkedSocket(path, options.ServerUID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.MaxDuration)
	defer cancel()
	dialer := net.Dialer{Timeout: options.DialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	uid, err := connectionUID(conn)
	if err != nil || uid != options.ServerUID {
		return errors.Join(ErrNoPeerUID, err)
	}
	after, err := checkedSocket(path, options.ServerUID)
	if err != nil || !os.SameFile(before, after) {
		return errors.New("ingress endpoint changed while connecting")
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ErrNoPeerUID
	}
	if file, ok := input.(*os.File); ok {
		input, err = interruptibleProxyFile(file)
		if err != nil {
			return err
		}
		defer input.Close()
	}
	if file, ok := output.(*os.File); ok {
		output, err = interruptibleProxyFile(file)
		if err != nil {
			return err
		}
		defer output.Close()
	}
	type result struct {
		upstream bool
		err      error
	}
	done := make(chan result, 2)
	activity := make(chan struct{}, 1)
	go func() {
		err := copyBounded(unixConn, input, options.MaxBytes, activity)
		if err == nil {
			closeErr := unixConn.CloseWrite()
			if !errors.Is(closeErr, syscall.ENOTCONN) && !errors.Is(closeErr, syscall.EPIPE) {
				err = closeErr
			}
		}
		done <- result{upstream: true, err: err}
	}()
	go func() {
		done <- result{err: copyBounded(output, unixConn, options.MaxBytes, activity)}
	}()
	idle := time.NewTimer(options.IdleTimeout)
	defer idle.Stop()
	remaining := 2
	finish := func(cause error) error {
		_ = conn.Close()
		_ = input.Close()
		_ = output.Close()
		for remaining > 0 {
			result := <-done
			if errors.Is(result.err, ErrProxyLimit) {
				cause = errors.Join(cause, result.err)
			}
			remaining--
		}
		return cause
	}
	for {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case <-idle.C:
			return finish(ErrIdleTimeout)
		case <-activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(options.IdleTimeout)
		case result := <-done:
			remaining--
			if result.err != nil || !result.upstream {
				return finish(result.err)
			}
		}
	}
}

func (o *ProxyOptions) validate() error {
	if err := socketPath(o.APISocket); err != nil {
		return err
	}
	if err := socketPath(o.JoinSocket); err != nil {
		return err
	}
	if o.APISocket == o.JoinSocket {
		return errors.New("API and enrollment ingress must use distinct sockets")
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = time.Minute
	}
	if o.MaxDuration == 0 {
		o.MaxDuration = time.Hour
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 64 << 20
	}
	if o.DialTimeout < 0 || o.DialTimeout > 30*time.Second || o.IdleTimeout < 0 || o.IdleTimeout > 10*time.Minute ||
		o.MaxDuration < 0 || o.MaxDuration > 24*time.Hour || o.MaxBytes < 0 || o.MaxBytes > 1<<30 {
		return errors.New("invalid ingress gateway resource limits")
	}
	return nil
}

func copyBounded(dst io.Writer, src io.Reader, limit int64, activity chan<- struct{}) error {
	buffer := make([]byte, 32<<10)
	var copied int64
	for {
		n, readErr := src.Read(buffer[:min(int64(len(buffer)), limit-copied+1)])
		if n > 0 {
			if int64(n) > limit-copied {
				return ErrProxyLimit
			}
			written, err := dst.Write(buffer[:n])
			copied += int64(written)
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
			select {
			case activity <- struct{}{}:
			default:
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("copy ingress stream: %w", readErr)
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}
