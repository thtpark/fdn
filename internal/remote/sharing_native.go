package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type SharingBackend interface {
	Inventory(context.Context, config.Worker, *local.State) ([]domain.Terminal, error)
	Open(context.Context, config.Worker, *local.State, domain.Session, domain.Terminal) (io.ReadWriteCloser, error)
	EndInput(context.Context, config.Worker, *local.State, domain.Session) error
	Close() error
}

type sharingTerminalSpec struct {
	Terminal     domain.Terminal `json:"terminal"`
	WorkspaceRef string          `json:"workspace_ref"`
}

type managedSharingTerminal struct {
	terminal   domain.Terminal
	config     config.Worker
	session    domain.Session
	generation string
	endpoint   io.ReadWriteCloser
}

type nativeSharingBackend struct {
	mu      sync.Mutex
	parent  context.Context
	managed map[string]*managedSharingTerminal
}

func newNativeSharingBackend(ctx context.Context) *nativeSharingBackend {
	return &nativeSharingBackend{parent: ctx, managed: map[string]*managedSharingTerminal{}}
}

func RequestSharingTerminal(ctx context.Context, state *local.State, operation, name, workspace string, protected bool) (domain.Terminal, error) {
	if !domain.ValidOperation(operation) {
		return domain.Terminal{}, domain.Fail(domain.Invalid, "terminal preparation needs a stable operation ID")
	}
	if err := domain.CheckText(name, "terminal name", 160, true); err != nil {
		return domain.Terminal{}, err
	}
	if workspace != "" {
		if _, err := state.Workspace(ctx, workspace); err != nil {
			return domain.Terminal{}, err
		}
	}
	lock, err := secure.Acquire(filepath.Join(state.Root, "sharing-terminals.lock"))
	if err != nil {
		return domain.Terminal{}, err
	}
	defer lock.Close()
	var specs []sharingTerminalSpec
	if _, err := state.Get(ctx, "sharing-terminals", &specs); err != nil {
		return domain.Terminal{}, err
	}
	id := "terminal-" + domain.Hash(operation)[:24]
	for _, spec := range specs {
		if spec.Terminal.ID == id {
			if spec.Terminal.Name != name || spec.WorkspaceRef != workspace || spec.Terminal.Protected != protected {
				return domain.Terminal{}, domain.Fail(domain.Conflict, "terminal operation belongs to another exact preparation")
			}
			return spec.Terminal, nil
		}
	}
	if len(specs) >= domain.MaxSharingTerminals-2 {
		return domain.Terminal{}, domain.Fail(domain.Prerequisite, "managed terminal inventory is full; retire an unused terminal before adding another")
	}
	meta := domain.Terminal{ID: id, Name: name, Generation: domain.NewID("terminal"), Kind: "shell", CanInput: true, Protected: protected}
	specs = append(specs, sharingTerminalSpec{Terminal: meta, WorkspaceRef: workspace})
	return meta, state.Put(ctx, "sharing-terminals", specs)
}

func sharingSpecs(ctx context.Context, state *local.State) ([]sharingTerminalSpec, error) {
	lock, err := secure.Acquire(filepath.Join(state.Root, "sharing-terminals.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var specs []sharingTerminalSpec
	if _, err := state.Get(ctx, "sharing-terminals", &specs); err != nil {
		return nil, err
	}
	var generation string
	found, err := state.Get(ctx, "sharing-default-generation", &generation)
	if err != nil {
		return nil, err
	}
	if !found {
		generation = domain.NewID("terminal")
		if err := state.Put(ctx, "sharing-default-generation", generation); err != nil {
			return nil, err
		}
	}
	specs = append([]sharingTerminalSpec{{Terminal: domain.Terminal{ID: "shell", Name: "Help terminal",
		Generation: generation, Kind: "shell", CanInput: true}}}, specs...)
	var execution domain.Terminal
	found, err = state.Get(ctx, "sharing-execution", &execution)
	if err != nil {
		return nil, err
	}
	if found {
		specs = append(specs, sharingTerminalSpec{Terminal: execution})
	}
	terminals := make([]domain.Terminal, len(specs))
	for i := range specs {
		terminals[i] = specs[i].Terminal
	}
	return specs, domain.ValidateTerminals(terminals)
}

func (backend *nativeSharingBackend) Inventory(ctx context.Context, _ config.Worker, state *local.State) ([]domain.Terminal, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, errors.Join(domain.Fail(domain.Prerequisite, "tmux is required for terminal sharing"), err)
	}
	specs, err := sharingSpecs(ctx, state)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Terminal, len(specs))
	for i := range specs {
		result[i] = specs[i].Terminal
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	var cleanupErr error
	for key, resource := range backend.managed {
		if resource.terminal.Kind == "execution" && !slices.Contains(result, resource.terminal) {
			err := resource.close(ctx)
			cleanupErr = errors.Join(cleanupErr, err)
			if err == nil {
				delete(backend.managed, key)
			}
		}
	}
	return result, cleanupErr
}

func (backend *nativeSharingBackend) Open(ctx context.Context, c config.Worker, state *local.State, session domain.Session, terminal domain.Terminal) (io.ReadWriteCloser, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	specs, err := sharingSpecs(ctx, state)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(specs, func(spec sharingTerminalSpec) bool { return spec.Terminal == terminal })
	if index < 0 {
		return nil, domain.Fail(domain.Conflict, "native terminal generation changed before attachment")
	}
	key := terminal.ID + ":" + terminal.Generation
	resource := backend.managed[key]
	if resource == nil {
		owned := c
		owned.StateDir = filepath.Join(c.StateDir, "sharing")
		if err := secure.Dir(owned.StateDir); err != nil {
			return nil, err
		}
		nativeSession := domain.Session{ID: "resource_" + domain.Hash(key)[:32], TargetID: session.TargetID,
			Mode: "tmux", Purpose: "terminal", State: "active", OpenRequested: true}
		native := Native{}
		native.Command = []string{"/bin/sh", "-i"}
		if terminal.Kind == "execution" {
			directory := filepath.Join(c.StateDir, "invocations", terminal.Generation)
			for _, name := range []string{"stdout.log", "stderr.log"} {
				file, err := waitExecutionLog(ctx, filepath.Join(directory, name))
				if err != nil {
					return nil, err
				}
				info, statErr := file.Stat()
				closeErr := file.Close()
				if statErr != nil || closeErr != nil {
					return nil, errors.Join(statErr, closeErr)
				}
				if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
					return nil, domain.Fail(domain.Denied, "execution output must remain a private regular file")
				}
				if err := secure.CheckOwner(info); err != nil {
					return nil, err
				}
			}
			native.Command = []string{"tail", "-q", "-n", "+1", "-F", filepath.Join(directory, "stdout.log"), filepath.Join(directory, "stderr.log")}
		} else if specs[index].WorkspaceRef != "" {
			binding, err := state.Workspace(ctx, specs[index].WorkspaceRef)
			if err != nil {
				return nil, err
			}
			native.Directory = binding.Path
		}
		nativeGeneration := ""
		pointer := "sharing-native:" + domain.Hash(key)
		if _, err := state.Get(ctx, pointer, &nativeGeneration); err != nil {
			return nil, err
		}
		if nativeGeneration == "" {
			nativeGeneration = domain.NewID("native")
			if err := state.Put(ctx, pointer, nativeGeneration); err != nil {
				return nil, err
			}
		}
		path, socket, err := sessionNativePaths(owned, nativeSession.ID, nativeGeneration)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(path); err == nil {
			record, _, _, err := readNativeSession(owned, nativeSession, nativeGeneration)
			if err != nil {
				return nil, err
			}
			if record.State == "cleaned" {
				nativeGeneration = domain.NewID("native")
				if err := state.Put(ctx, pointer, nativeGeneration); err != nil {
					return nil, err
				}
				endpoint, err := native.OpenOwned(backend.parent, nativeSession, owned, nativeGeneration)
				if err != nil {
					if endpoint != nil {
						err = errors.Join(err, endpoint.Close())
					}
					return nil, err
				}
				resource = &managedSharingTerminal{terminal: terminal, config: owned, session: nativeSession, generation: nativeGeneration, endpoint: endpoint}
				go func() { _, _ = io.Copy(io.Discard, endpoint) }()
			} else {
				server, err := native.serverIdentity(ctx, socket)
				if err != nil || server == nil || record.Server == nil || *server != *record.Server {
					return nil, errors.Join(err, domain.Fail(domain.Prerequisite, "retained sharing terminal has no matching live native ownership"))
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		} else {
			endpoint, err := native.OpenOwned(backend.parent, nativeSession, owned, nativeGeneration)
			if err != nil {
				if endpoint != nil {
					err = errors.Join(err, endpoint.Close())
				}
				return nil, err
			}
			resource = &managedSharingTerminal{terminal: terminal, config: owned, session: nativeSession, generation: nativeGeneration, endpoint: endpoint}
			go func() { _, _ = io.Copy(io.Discard, endpoint) }()
		}
		if resource == nil {
			resource = &managedSharingTerminal{terminal: terminal, config: owned, session: nativeSession, generation: nativeGeneration}
		}
		backend.managed[key] = resource
	}
	record, _, socket, err := readNativeSession(resource.config, resource.session, resource.generation)
	if err != nil {
		return nil, err
	}
	server, err := (Native{}).serverIdentity(ctx, socket)
	if err != nil || server == nil || record.Server == nil || *server != *record.Server {
		return nil, errors.Join(err, domain.Fail(domain.Conflict, "native terminal ownership changed before attachment"))
	}
	return attachManagedTerminal(ctx, socket)
}

func waitExecutionLog(ctx context.Context, path string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err == nil || !os.IsNotExist(err) {
			return file, err
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(err, domain.Fail(domain.Prerequisite, "execution output is not available for this native generation"))
		case <-ticker.C:
		}
	}
}

func attachManagedTerminal(ctx context.Context, socket string) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	socketArgument, directory := tmuxSocketLocation(socket)
	cmd := exec.Command("tmux", "-S", socketArgument, "attach-session", "-t", "foundation", "-f", "ignore-size")
	cmd.Dir = directory
	cmd.Env = process.Environment("TERM=xterm-256color")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, err
	}
	identity, err := inspectProcess(cmd.Process.Pid)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	if err != nil || identity == nil || !identity.valid() {
		closeErr := file.Close()
		stopErr := cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			stopErr = errors.Join(stopErr, domain.Fail(domain.Prerequisite, "sharing attachment did not join after failed native ownership capture"))
		}
		return nil, &process.CleanupError{Err: errors.Join(err, closeErr, stopErr, domain.Fail(domain.Prerequisite, "sharing attachment lacks exact native process identity"))}
	}
	return &sharingAttachment{File: file, identity: *identity, done: done}, nil
}

type sharingAttachment struct {
	*os.File
	identity ownedProcess
	done     <-chan struct{}
	once     sync.Once
	err      error
}

func (attachment *sharingAttachment) Close() error {
	attachment.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeErr := attachment.File.Close()
		if errors.Is(closeErr, os.ErrClosed) {
			closeErr = nil
		}
		attachment.err = errors.Join(closeErr, attachment.identity.stop(ctx))
		select {
		case <-attachment.done:
		case <-ctx.Done():
			attachment.err = errors.Join(attachment.err, ctx.Err())
		}
	})
	return attachment.err
}

func (backend *nativeSharingBackend) EndInput(ctx context.Context, c config.Worker, state *local.State, session domain.Session) error {
	if session.Mode != "tmux" {
		return nil
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	var lease sharingInputRecord
	found, err := state.Get(ctx, sharingInputKey(session.ID), &lease)
	if err != nil {
		return err
	}
	if !found || lease.Session.ID != session.ID || lease.Terminal.ID != session.TerminalID {
		return domain.Fail(domain.Prerequisite, "input cleanup needs the exact selected terminal and durable native generation")
	}
	key := session.TerminalID + ":" + lease.Terminal.Generation
	if resource := backend.managed[key]; resource != nil {
		if err := resource.close(ctx); err != nil {
			return err
		}
		delete(backend.managed, key)
	} else {
		c.StateDir = filepath.Join(c.StateDir, "sharing")
		var nativeGeneration string
		found, err := state.Get(ctx, "sharing-native:"+domain.Hash(key), &nativeGeneration)
		if err != nil {
			return err
		}
		if !found || !domain.ValidOperation(nativeGeneration) {
			return domain.Fail(domain.Prerequisite, "sharing input has no exact retained native resource")
		}
		nativeSession := domain.Session{ID: "resource_" + domain.Hash(key)[:32], TargetID: session.TargetID, Mode: "tmux"}
		if err := (Native{}).RecoverOwned(ctx, nativeSession, c, nativeGeneration); err != nil {
			return err
		}
	}
	if session.TerminalID == "shell" {
		var generation string
		if _, err := state.Get(ctx, "sharing-default-generation", &generation); err != nil {
			return err
		}
		if generation == lease.Terminal.Generation {
			return state.Put(ctx, "sharing-default-generation", domain.NewID("terminal"))
		}
		return nil
	}
	lock, err := secure.Acquire(filepath.Join(state.Root, "sharing-terminals.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	var specs []sharingTerminalSpec
	if _, err := state.Get(ctx, "sharing-terminals", &specs); err != nil {
		return err
	}
	for i := range specs {
		if specs[i].Terminal.ID == session.TerminalID && specs[i].Terminal.Generation == lease.Terminal.Generation {
			specs[i].Terminal.Generation = domain.NewID("terminal")
		}

	}
	return state.Put(ctx, "sharing-terminals", specs)
}

func (resource *managedSharingTerminal) close(ctx context.Context) error {
	if resource.endpoint != nil {
		return resource.endpoint.Close()
	}
	return (Native{}).RecoverOwned(ctx, resource.session, resource.config, resource.generation)
}

func (backend *nativeSharingBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result error
	for key, resource := range backend.managed {
		err := resource.close(ctx)
		result = errors.Join(result, err)
		if err == nil {
			delete(backend.managed, key)
		}
	}
	return result
}
