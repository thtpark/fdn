package display

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

var applications = struct {
	sync.Mutex
	owned map[string]*applicationProcess
}{owned: map[string]*applicationProcess{}}

type applicationProcess struct {
	cmd         *exec.Cmd
	done        chan struct{}
	cleanupDone chan struct{}
	waitErr     error
	key         string
	owner       string
	fingerprint string
	once        sync.Once
	err         error
}

func launch(ctx context.Context, c config.Worker, item available, app config.ScreenApplication, owner string) (*applicationProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, startupError(err, true)
	}
	if !filepath.IsAbs(c.StateDir) {
		return nil, startupError(domain.Fail(domain.Invalid, "application ownership needs the worker's absolute state directory"), true)
	}
	path, err := executable(app.Executable)
	if err != nil {
		return nil, startupError(err, true)
	}
	env, home, err := environment(item.profile)
	if err != nil {
		return nil, startupError(err, true)
	}
	encoded, err := json.Marshal(struct {
		App     config.ScreenApplication
		Display string
		Auth    string
		Home    string
		Rect    rectangle
	}{app, item.profile.Display, item.profile.XAuthority, home, item.rect})
	if err != nil {
		return nil, startupError(err, true)
	}
	key, fingerprint := filepath.Join(filepath.Clean(c.StateDir), item.profile.ID), domain.Hash(string(encoded))
	applications.Lock()
	defer applications.Unlock()
	if previous := applications.owned[key]; previous != nil {
		select {
		case <-previous.done:
			if err := previous.Close(); err != nil {
				return nil, startupError(err, false)
			}
			delete(applications.owned, key)
		default:
			if previous.owner == owner && previous.fingerprint == fingerprint {
				return previous, nil
			}
			return nil, startupError(domain.Fail(domain.Conflict, "another owned application already occupies this screen; it was not adopted or replaced"), true)
		}
	}
	if err := placementReady(ctx, item); err != nil {
		return nil, startupError(err, true)
	}
	if err := ctx.Err(); err != nil {
		return nil, startupError(err, true)
	}
	cmd := exec.Command(path, app.Arguments...)
	cmd.Env, cmd.Dir = env, home
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, startupError(domain.Fail(domain.Prerequisite, "configured application could not start as the runner UID"), true)
	}
	owned := &applicationProcess{cmd: cmd, done: make(chan struct{}), cleanupDone: make(chan struct{}), key: key, owner: owner, fingerprint: fingerprint}
	applications.owned[key] = owned
	go func() { owned.waitErr = cmd.Wait(); close(owned.done) }()
	if err := place(ctx, item, cmd.Process.Pid); err != nil {
		cleanupErr := owned.Close()
		if cleanupErr == nil {
			delete(applications.owned, key)
		}
		return nil, startupError(errors.Join(err, cleanupErr), cleanupErr == nil)
	}
	select {
	case <-owned.done:
		cleanupErr := owned.Close()
		if cleanupErr == nil {
			delete(applications.owned, key)
		}
		return nil, startupError(errors.Join(domain.Fail(domain.Prerequisite, "application exited before screen preparation; configure a foreground non-reusing application"), cleanupErr), cleanupErr == nil)
	default:
	}
	go func() {
		defer close(owned.cleanupDone)
		select {
		case <-ctx.Done():
		case <-owned.done:
		}
		if owned.Close() == nil {
			applications.Lock()
			if applications.owned[key] == owned {
				delete(applications.owned, key)
			}
			applications.Unlock()
		}
	}()
	return owned, nil
}

func (p *applicationProcess) Close() error {
	p.once.Do(func() {
		var problem error
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			problem = errors.Join(problem, err)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-p.done:
		case <-timer.C:
		}
		timer.Stop()
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			problem = errors.Join(problem, err)
		}
		timer.Reset(2 * time.Second)
		select {
		case <-p.done:
		case <-timer.C:
			problem = errors.Join(problem, domain.Fail(domain.Prerequisite, "owned application process did not stop"))
		}
		timer.Stop()
		deadline := time.Now().Add(500 * time.Millisecond)
		for {
			err := syscall.Kill(-p.cmd.Process.Pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if err != nil || time.Now().After(deadline) {
				problem = errors.Join(problem, domain.Fail(domain.Prerequisite, "owned application process-group cleanup is not proved"))
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if problem != nil {
			p.err = &process.CleanupError{Err: problem}
		}
	})
	return p.err
}
