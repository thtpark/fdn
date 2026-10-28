package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type nativeSession struct {
	Version       int           `json:"version"`
	SessionID     string        `json:"session_id"`
	TargetID      string        `json:"target_id"`
	Generation    string        `json:"generation"`
	State         string        `json:"state"`
	Client        *ownedProcess `json:"client,omitempty"`
	Server        *ownedProcess `json:"server,omitempty"`
	ServerStopped bool          `json:"server_stopped,omitempty"`
}

type startupFailure struct {
	error
	clean bool
}

func (e *startupFailure) CleanupProven() bool { return e.clean }

func sessionNativePaths(c config.Worker, id, generation string) (string, string, error) {
	if !filepath.IsAbs(c.StateDir) || !domain.ValidOperation(id) || !domain.ValidOperation(generation) {
		return "", "", domain.Fail(domain.Invalid, "native session recovery requires its exact state, session and fence generation")
	}
	directory := filepath.Join(c.StateDir, "sessions")
	if err := secure.Dir(directory); err != nil {
		return "", "", err
	}
	name := domain.Hash(id + ":" + generation)[:32]
	return filepath.Join(directory, name+".json"), filepath.Join(directory, name+".sock"), nil
}

func writeNativeSession(path string, record nativeSession) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return secure.Write(path, data)
}

func readNativeSession(c config.Worker, session domain.Session, generation string) (nativeSession, string, string, error) {
	var record nativeSession
	path, socket, err := sessionNativePaths(c, session.ID, generation)
	if err != nil {
		return record, path, socket, err
	}
	if err := config.Read(path, &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = domain.Fail(domain.Prerequisite, "retained session has no durable native ownership record; preserve it for an authorized operator")
		}
		return record, path, socket, err
	}
	if record.Version != 1 || record.SessionID != session.ID || record.Generation != generation || record.TargetID != session.TargetID ||
		(record.State != "launching" && record.State != "running" && record.State != "closing" && record.State != "cleaned") ||
		record.Client != nil && !record.Client.valid() || record.Server != nil && !record.Server.valid() {
		return record, path, socket, domain.Fail(domain.Prerequisite, "native session ownership evidence does not match the retained fence")
	}
	return record, path, socket, nil
}

func (n Native) tmux(ctx context.Context, socket string, arguments ...string) ([]byte, error) {
	runner := n.Runner
	if runner == nil {
		runner = process.Native{}
	}
	output := process.NewBuffer(4096)
	err := runner.Run(ctx, process.Spec{Path: "tmux", Args: append([]string{"-S", socket}, arguments...),
		Env: process.Environment(), Stdout: output, Stderr: process.NewBuffer(4096)})
	data, outputErr := output.Bytes()
	return data, errors.Join(err, outputErr)
}

func (n Native) serverIdentity(ctx context.Context, socket string) (*ownedProcess, error) {
	info, err := os.Lstat(socket)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, domain.Fail(domain.Denied, "native session endpoint is not its protected Unix socket")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, err
	}
	data, err := n.tmux(ctx, socket, "display-message", "-p", "#{pid}")
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return nil, domain.Fail(domain.Prerequisite, "native session server identity is unavailable")
	}
	server, err := inspectProcess(pid)
	if err != nil {
		return nil, err
	}
	if server == nil || !server.valid() {
		return nil, domain.Fail(domain.Denied, "native session server does not have the expected OS ownership")
	}
	return server, nil
}

func (n Native) cleanNativeSession(ctx context.Context, path, socket string, record *nativeSession) error {
	if record.State == "cleaned" {
		return writeNativeSession(path, *record)
	}
	if record.Client == nil {
		return domain.Fail(domain.Prerequisite, "interrupted native launch has no durable process identity; no process was guessed or adopted")
	}
	record.State = "closing"
	if err := writeNativeSession(path, *record); err != nil {
		return err
	}
	if !record.ServerStopped {
		if record.Server != nil {
			if _, err := record.Server.stopped(); err != nil {
				return err
			}
		}
		server, observeErr := n.serverIdentity(ctx, socket)
		if observeErr != nil && (domain.AsFault(observeErr).Code == domain.Denied || domain.AsFault(observeErr).Code == domain.Invalid) {
			return observeErr
		}
		if server != nil {
			if record.Server != nil && *record.Server != *server {
				return domain.Fail(domain.Conflict, "native session server ownership changed")
			}
			record.Server = server
			if err := writeNativeSession(path, *record); err != nil {
				return err
			}
		}
		_, stopErr := n.tmux(ctx, socket, "kill-server")
		if stopErr != nil {
			if record.Server == nil {
				return errors.Join(domain.Fail(domain.Prerequisite, "native session server shutdown is not proven"), observeErr, stopErr)
			}
			stopped, err := record.Server.stopped()
			if err != nil || !stopped {
				return errors.Join(domain.Fail(domain.Prerequisite, "native session server cleanup remains pending"), observeErr, stopErr, err)
			}
		} else if observeErr != nil {
			return observeErr
		}
		record.ServerStopped = true
		if err := writeNativeSession(path, *record); err != nil {
			return err
		}
	}
	if err := record.Client.stop(ctx); err != nil {
		return &process.CleanupError{Err: err}
	}
	record.State = "cleaned"
	return writeNativeSession(path, *record)
}

func (n Native) RecoverOwned(ctx context.Context, session domain.Session, c config.Worker, generation string) error {
	if session.Mode != "tmux" {
		return domain.Fail(domain.Prerequisite, "this native endpoint has no restart-safe automatic cleanup capability")
	}
	record, path, socket, err := readNativeSession(c, session, generation)
	if err != nil {
		return err
	}
	terminal := session.State == "closed" || session.State == "expired" || session.State == "denied" || session.State == "revoked" || session.State == "failed"
	if !terminal && record.State != "closing" && record.State != "cleaned" {
		return domain.Fail(domain.Conflict, "an active interactive session is not an automatic recovery target")
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return n.cleanNativeSession(cleanupCtx, path, socket, &record)
}
