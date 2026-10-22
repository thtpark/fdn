package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/display"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type Native struct{}

func (Native) Open(ctx context.Context, s domain.Session, c config.Worker) (io.ReadWriteCloser, error) {
	if s.Mode == "vnc" {
		return display.Open(ctx, c, s)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Mode != "tmux" {
		return nil, domain.Fail(domain.Invalid, "unsupported session endpoint")
	}
	dir := filepath.Join(c.StateDir, "sessions")
	if err := secure.Dir(dir); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, s.ID+".sock")
	args := []string{"-S", socket, "-f", "/dev/null", "new-session", "-s", "foundation"}
	if s.Purpose == "authentication" {
		path, native, err := provider.New(c).LoginCommand(ctx, s.Provider)
		if err != nil {
			return nil, err
		}
		args = append(append(args, path), native...)
	}
	cmd := exec.Command("tmux", args...)
	cmd.Env = process.Environment("TERM=xterm-256color")
	cmd.Dir = c.StateDir
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &nativeTerminal{File: terminal, cmd: cmd, done: done, socket: socket}, nil
}

type nativeTerminal struct {
	*os.File
	cmd    *exec.Cmd
	done   <-chan error
	socket string
	once   sync.Once
	err    error
}

func (t *nativeTerminal) Close() error {
	t.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output := process.NewBuffer(4096)
		killServerErr := (process.Native{}).Run(ctx, process.Spec{Path: "tmux", Args: []string{"-S", t.socket, "kill-server"}, Env: process.Environment(), Stdout: output, Stderr: output})
		if killServerErr != nil {
			if _, statErr := os.Lstat(t.socket); !os.IsNotExist(statErr) {
				t.err = errors.Join(killServerErr, statErr)
			}
		}
		signalErr := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		if signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
			t.err = errors.Join(t.err, signalErr)
		}
		closeErr := t.File.Close()
		<-t.done
		t.err = errors.Join(t.err, closeErr)
	})
	return t.err
}
