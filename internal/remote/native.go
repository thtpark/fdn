package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/display"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type Native struct {
	Runner process.Runner
}

func (n Native) Open(ctx context.Context, s domain.Session, c config.Worker) (io.ReadWriteCloser, error) {
	return n.OpenOwned(ctx, s, c, domain.NewID("native"))
}

func (n Native) OpenOwned(ctx context.Context, s domain.Session, c config.Worker, generation string) (io.ReadWriteCloser, error) {
	if s.Mode == "vnc" {
		return display.Open(ctx, c, s)
	}
	if err := ctx.Err(); err != nil {
		return nil, &startupFailure{error: err, clean: true}
	}
	if s.Mode != "tmux" {
		return nil, &startupFailure{error: domain.Fail(domain.Invalid, "unsupported session endpoint"), clean: true}
	}
	path, socket, err := sessionNativePaths(c, s.ID, generation)
	if err != nil {
		return nil, &startupFailure{error: err, clean: true}
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		return nil, domain.Fail(domain.Conflict, "native session namespace already exists or cannot be inspected")
	}
	args := []string{"-S", socket, "-f", "/dev/null", "new-session", "-s", "foundation"}
	if s.Purpose == "authentication" {
		path, native, err := provider.New(c).LoginCommand(ctx, s.Provider)
		if err != nil {
			return nil, &startupFailure{error: err, clean: true}
		}
		args = append(append(args, path), native...)
	}
	record := nativeSession{Version: 1, SessionID: s.ID, TargetID: s.TargetID, Generation: generation, State: "launching"}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, &startupFailure{error: err, clean: true}
	}
	if err := secure.Create(path, data); err != nil {
		return nil, &startupFailure{error: err, clean: !errors.Is(err, os.ErrExist)}
	}
	cmd := exec.Command("tmux", args...)
	cmd.Env = process.Environment("TERM=xterm-256color")
	cmd.Dir = c.StateDir
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		if cmd.Process == nil {
			record.State = "cleaned"
			saveErr := writeNativeSession(path, record)
			return nil, &startupFailure{error: errors.Join(err, saveErr), clean: saveErr == nil}
		}
		return nil, err
	}
	// Capture the kernel identity before Wait can reap a fast-exiting client.
	client, inspectErr := inspectProcess(cmd.Process.Pid)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	endpoint := &nativeTerminal{File: terminal, cmd: cmd, done: done, socket: socket, path: path, record: record, native: n}
	if inspectErr != nil || client == nil || !client.valid() {
		return endpoint, errors.Join(inspectErr, domain.Fail(domain.Prerequisite, "native session client ownership was not durably captured"))
	}
	endpoint.record.Client = client
	endpoint.record.State = "running"
	if err := writeNativeSession(path, endpoint.record); err != nil {
		return endpoint, err
	}
	startup := time.NewTimer(300 * time.Millisecond)
	defer startup.Stop()
	for {
		server, err := n.serverIdentity(ctx, socket)
		if err != nil {
			return endpoint, err
		}
		if server != nil {
			endpoint.record.Server = server
			if err := writeNativeSession(path, endpoint.record); err != nil {
				return endpoint, err
			}
			break
		}
		select {
		case <-ctx.Done():
			return endpoint, ctx.Err()
		case <-done:
			return endpoint, nil
		case <-startup.C:
			return endpoint, nil
		case <-time.After(10 * time.Millisecond):
		}
	}
	return endpoint, nil
}

type nativeTerminal struct {
	*os.File
	cmd    *exec.Cmd
	done   <-chan struct{}
	socket string
	path   string
	record nativeSession
	native Native
	mu     sync.Mutex
	closed bool
}

func (t *nativeTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleanupErr := t.native.cleanNativeSession(ctx, t.path, t.socket, &t.record)
	closeErr := t.File.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	if cleanupErr != nil || closeErr != nil {
		return errors.Join(cleanupErr, closeErr)
	}
	select {
	case <-t.done:
		t.closed = true
		return nil
	case <-ctx.Done():
		return &process.CleanupError{Err: domain.Fail(domain.Prerequisite, "owned terminal client has not completed shutdown")}
	}
}
