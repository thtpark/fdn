package remote

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type legacySocket struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Modified int64  `json:"modified"`
}

type legacyReset struct {
	Version     int            `json:"version"`
	SessionID   string         `json:"session_id"`
	TargetID    string         `json:"target_id"`
	Generation  string         `json:"generation,omitempty"`
	Fingerprint string         `json:"fingerprint"`
	State       string         `json:"state"`
	Socket      *legacySocket  `json:"socket,omitempty"`
	Server      *ownedProcess  `json:"server,omitempty"`
	Groups      []ownedProcess `json:"groups"`
	Clients     []ownedProcess `json:"clients"`
}

func writeLegacyReset(path string, record legacyReset, create bool) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if create {
		return secure.Create(path, data)
	}
	return secure.Write(path, data)
}

// ResetSession is explicit Founder-reset cleanup, never ordinary restart recovery.
// The caller must join its runtime and hold work.lock for this exact fence.
func (n Native) ResetSession(ctx context.Context, session domain.Session, c config.Worker, snapshot SessionFenceSnapshot) error {
	fence := snapshot.Fence
	if !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir ||
		!fence.Active || fence.ID != session.ID || !domain.ValidOperation(session.ID) || !domain.ValidOperation(session.TargetID) ||
		!domain.ValidHash(snapshot.Fingerprint) || !domain.TerminalSessionState(session.State) || session.Mode != "tmux" {
		return domain.Fail(domain.Prerequisite, "reset cleanup requires the exact target's terminal tmux session and fence")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := secure.Dir(c.StateDir); err != nil {
		return err
	}
	directory := filepath.Join(c.StateDir, "sessions")
	if err := secure.Dir(directory); err != nil {
		return err
	}
	if fence.Generation != "" {
		path, socket, err := sessionNativePaths(c, session.ID, fence.Generation)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			return n.RecoverOwned(ctx, session, c, fence.Generation)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(domain.Fail(domain.Prerequisite, "a current-format endpoint is missing its ownership record; it is not legacy cleanup"), err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) > 1024 {
		return domain.Fail(domain.Prerequisite, "native session evidence exceeds the bounded reset inspection")
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if len(name) != 32 || entry.Name() != name+".json" {
			continue
		}
		if _, err := hex.DecodeString(name); err != nil {
			continue
		}
		var record nativeSession
		if err := config.Read(filepath.Join(directory, entry.Name()), &record); err != nil {
			return err
		}
		if record.SessionID == session.ID {
			return domain.Fail(domain.Conflict, "another native generation owns this session; legacy cleanup cannot adopt it")
		}
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return n.resetLegacy(cleanupCtx, session, directory, snapshot)
}

func legacySocketIdentity(path string) (*legacySocket, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, domain.Fail(domain.Denied, "legacy reset endpoint must be its private Unix socket, never a symlink or another file")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, domain.Fail(domain.Prerequisite, "legacy socket kernel identity is unavailable")
	}
	return &legacySocket{Device: uint64(stat.Dev), Inode: stat.Ino, Modified: info.ModTime().UnixNano()}, nil
}

func legacyConnection(ctx context.Context, path string) (*ownedProcess, error) {
	if socket, err := legacySocketIdentity(path); err != nil || socket == nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, domain.Fail(domain.Denied, "legacy cleanup requires a native Unix endpoint")
	}
	return legacyPeer(unixConn)
}

func (n Native) legacyGroups(ctx context.Context, socket string, server ownedProcess) ([]ownedProcess, []ownedProcess, error) {
	data, err := n.tmux(ctx, socket, "list-sessions", "-F", "#{session_name}:#{pid}")
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(string(data)) != "foundation:"+strconv.Itoa(server.PID) {
		return nil, nil, domain.Fail(domain.Denied, "legacy endpoint is not the exact dedicated Foundation tmux server")
	}
	groups, clients := []ownedProcess{}, []ownedProcess{}
	seen := map[int]bool{server.PID: true}
	for index, command := range [][]string{
		{"list-clients", "-F", "#{client_pid}"},
		{"list-panes", "-a", "-F", "#{pane_pid}"},
	} {
		data, err := n.tmux(ctx, socket, command...)
		if err != nil {
			return nil, nil, err
		}
		for _, line := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(line)
			if err != nil || pid <= 1 || pid == os.Getpid() || len(groups)+len(clients) >= 64 {
				return nil, nil, domain.Fail(domain.Prerequisite, "legacy endpoint process inventory is unsupported")
			}
			if seen[pid] {
				continue
			}
			p, parent, err := inspectProcessWithParent(pid)
			if err != nil {
				return nil, nil, err
			}
			if p == nil || !p.valid() {
				return nil, nil, domain.Fail(domain.Prerequisite, "legacy client or pane lacks a current owned process-group identity")
			}
			seen[pid] = true
			if index == 0 {
				clients = append(clients, *p)
			} else {
				if parent != server.PID {
					return nil, nil, domain.Fail(domain.Denied, "legacy pane is not a kernel-confirmed child of this exact server")
				}
				groups = append(groups, *p)
			}
		}
	}
	return groups, clients, nil
}

func (n Native) resetLegacy(ctx context.Context, session domain.Session, directory string, snapshot SessionFenceSnapshot) error {
	name := domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]
	path := filepath.Join(directory, name+".legacy-reset.json")
	socket := filepath.Join(directory, session.ID+".sock")
	retiredDir := filepath.Join(directory, "retired")
	if err := secure.Dir(retiredDir); err != nil {
		return err
	}
	retired := filepath.Join(retiredDir, name+".sock")
	expected := legacyReset{Version: 1, SessionID: session.ID, TargetID: session.TargetID, Generation: snapshot.Fence.Generation,
		Fingerprint: snapshot.Fingerprint, State: "prepared", Groups: []ownedProcess{}, Clients: []ownedProcess{}}
	record := expected
	if err := config.Read(path, &record); errors.Is(err, os.ErrNotExist) {
		var err error
		record.Socket, err = legacySocketIdentity(socket)
		if err != nil {
			return err
		}
		record.Server, err = legacyConnection(ctx, socket)
		if err != nil {
			return err
		}
		if record.Server != nil {
			record.Groups, record.Clients, err = n.legacyGroups(ctx, socket, *record.Server)
			if err != nil {
				return err
			}
		} else if free, err := legacyEndpointUnbound(ctx, socket); err != nil || !free {
			return errors.Join(domain.Fail(domain.Prerequisite, "legacy endpoint still has kernel-owned resources"), err)
		}
		if err := writeLegacyReset(path, record, true); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if record.Version != expected.Version || record.SessionID != expected.SessionID || record.TargetID != expected.TargetID ||
		record.Generation != expected.Generation || record.Fingerprint != expected.Fingerprint ||
		(record.State != "prepared" && record.State != "stopped" && record.State != "cleaned") || len(record.Groups)+len(record.Clients) > 64 ||
		record.Server == nil && (len(record.Groups) != 0 || len(record.Clients) != 0) || record.Server != nil && record.Socket == nil {
		return domain.Fail(domain.Conflict, "legacy reset proof belongs to another fence or has an unsupported state")
	}
	source, err := legacySocketIdentity(socket)
	if err != nil {
		return err
	}
	preserved, err := legacySocketIdentity(retired)
	if err != nil {
		return err
	}
	if source != nil && (record.Socket == nil || *source != *record.Socket) ||
		preserved != nil && (record.Socket == nil || *preserved != *record.Socket) || source != nil && preserved != nil ||
		record.Socket != nil && source == nil && preserved == nil || record.State == "cleaned" && source != nil {
		return domain.Fail(domain.Conflict, "legacy session socket changed; replacement resources were not adopted")
	}
	current, err := legacyConnection(ctx, socket)
	if err != nil {
		return fmt.Errorf("recheck legacy endpoint peer: %w", err)
	}
	if current != nil && (record.Server == nil || *record.Server != *current || record.State != "prepared") {
		return domain.Fail(domain.Conflict, "legacy session server changed before cleanup")
	}
	for _, group := range record.Groups {
		if !group.valid() || group.PID == os.Getpid() {
			return domain.Fail(domain.Denied, "legacy reset cannot signal an unowned process group")
		}
		if record.State == "prepared" {
			if err := group.stop(ctx); err != nil {
				return fmt.Errorf("join legacy native group: %w", err)
			}
		} else if stopped, err := group.stopped(); err != nil || !stopped {
			return errors.Join(domain.Fail(domain.Prerequisite, "retained legacy group cleanup is no longer proven"), err)
		}
	}
	if record.Server != nil {
		if !record.Server.valid() || record.Server.PID == os.Getpid() {
			return domain.Fail(domain.Denied, "legacy reset cannot signal an unowned server")
		}
		if record.State == "prepared" {
			_, stopErr := n.tmux(ctx, socket, "kill-server")
			if err := record.Server.stop(ctx); err != nil {
				return errors.Join(stopErr, err)
			}
		} else if stopped, err := record.Server.stopped(); err != nil || !stopped {
			return errors.Join(domain.Fail(domain.Prerequisite, "retained legacy server cleanup is no longer proven"), err)
		}
	}
	for _, client := range record.Clients {
		if !client.valid() || client.PID == os.Getpid() {
			return domain.Fail(domain.Denied, "legacy client cleanup lacks exact process ownership")
		}
		// Old attach clients have no durable parent proof. Closing their exact
		// server must make them exit; never signal a PID reported by tmux alone.
		if err := client.waitStopped(ctx); err != nil {
			return err
		}
	}
	current, err = legacyConnection(ctx, socket)
	if err != nil || current != nil {
		return errors.Join(domain.Fail(domain.Prerequisite, "legacy session endpoint has not stopped"), err)
	}
	if free, err := legacyEndpointUnbound(ctx, socket); err != nil || !free {
		return errors.Join(domain.Fail(domain.Prerequisite, "legacy endpoint cleanup lacks kernel confirmation"), err)
	}
	if record.State == "cleaned" {
		return nil
	}
	record.State = "stopped"
	if err := writeLegacyReset(path, record, false); err != nil {
		return err
	}
	if source != nil {
		final, err := legacySocketIdentity(socket)
		if err != nil || final == nil || *final != *source {
			return errors.Join(domain.Fail(domain.Conflict, "legacy socket changed during native cleanup"), err)
		}
		if err := os.Rename(socket, retired); err != nil {
			return err
		}
		if err := errors.Join(secure.SyncDir(directory), secure.SyncDir(retiredDir)); err != nil {
			return err
		}
	}
	if final, err := legacySocketIdentity(socket); err != nil || final != nil {
		return errors.Join(domain.Fail(domain.Conflict, "legacy socket was replaced during retirement"), err)
	}
	record.State = "cleaned"
	return writeLegacyReset(path, record, false)
}
