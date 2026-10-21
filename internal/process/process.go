package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type Spec struct {
	Path     string
	Args     []string
	Dir      string
	Env      []string
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Started  func(int) error
	Terminal *os.File
}

type Runner interface {
	Run(context.Context, Spec) error
}

type Native struct{}

type CleanupError struct{ Err error }

func (e *CleanupError) Error() string { return "owned process-group cleanup failed: " + e.Err.Error() }
func (e *CleanupError) Unwrap() error { return e.Err }

func finished(cause, cleanup error) error {
	if cleanup != nil {
		return &CleanupError{Err: errors.Join(cause, cleanup)}
	}
	return cause
}

func (Native) Run(ctx context.Context, spec Spec) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.Dir, spec.Stdin, spec.Stdout, spec.Stderr
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.Terminal != nil {
		restore, prepareErr := prepareTerminal(spec.Terminal)
		if prepareErr != nil {
			return prepareErr
		}
		defer func() { err = finished(err, restore()) }()
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = int(spec.Terminal.Fd())
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	if spec.Started != nil {
		if err := spec.Started(cmd.Process.Pid); err != nil {
			killErr := signal(cmd.Process.Pid, syscall.SIGKILL)
			waitErr := cmd.Wait()
			return finished(errors.Join(err, waitErr), killErr)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return finished(err, signal(cmd.Process.Pid, syscall.SIGKILL))
	case <-ctx.Done():
		termErr := signal(cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return finished(errors.Join(ctx.Err(), termErr), signal(cmd.Process.Pid, syscall.SIGKILL))
		case <-timer.C:
			killErr := signal(cmd.Process.Pid, syscall.SIGKILL)
			<-done
			return finished(errors.Join(ctx.Err(), termErr), killErr)
		}
	}
}

func signal(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

type BoundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func NewBuffer(limit int) *BoundedBuffer { return &BoundedBuffer{limit: limit} }

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := len(p)
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:max(remaining, 0)]
	}
	_, err := b.buffer.Write(p)
	return length, err
}

func (b *BoundedBuffer) Bytes() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.overflow {
		return nil, domain.Fail(domain.Provider, "process output exceeded its private size limit")
	}
	return append([]byte{}, b.buffer.Bytes()...), nil
}

func Environment(extra ...string) []string {
	allowed := map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "PATH": true, "SHELL": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "TERM": true,
		"CODEX_HOME": true, "COPILOT_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true}
	env := []string{}
	for _, item := range os.Environ() {
		for i, c := range item {
			if c == '=' {
				if allowed[item[:i]] {
					env = append(env, item)
				}
				break
			}
		}
	}
	return append(env, extra...)
}
