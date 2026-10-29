//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
)

func TestNativeFounderResetClearsLegacySession(t *testing.T) {
	l := newLab(t, "codex")
	root, err := os.MkdirTemp("/tmp", "fdn-reset-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	l.worker.config.StateDir, l.worker.config.Mode = filepath.Join(root, "s"), "manual"
	if err := config.Write(l.worker.configFile, l.worker.config); err != nil {
		t.Fatal(err)
	}
	must[domain.Agent](t, "agent-cli", []string{"--config", l.founder.configFile, "agent", "mode", l.worker.agent.ID, "--input", "-"},
		domain.ModeRequest{Command: domain.Command{ExpectedRevision: l.worker.agent.Revision}, Mode: "manual", Reason: "Keep isolated native legacy reset paused"})
	grant := must[domain.AssistanceGrant](t, "agent-cli", []string{"--config", l.founder.configFile, "assistance", "grant", "--input", "-"}, domain.AssistanceGrantRequest{
		RequesterID: l.founder.agent.ID, TargetID: l.worker.agent.ID, Mode: "tmux", Purpose: "terminal", Reason: "Isolated closed legacy-session fixture"})
	session := must[domain.Session](t, "agent-cli", []string{"--config", l.founder.configFile, "session", "request", "--input", "-"}, domain.SessionRequest{
		GrantID: grant.ID, TargetID: l.worker.agent.ID, Mode: "tmux", Purpose: "terminal", Reason: "Closed legacy endpoint only", ExpiresAt: time.Now().Unix() + 120})
	session = must[domain.Session](t, "agent-cli", []string{"--config", l.founder.configFile, "session", "change", session.ID, "--input", "-"},
		domain.SessionChange{Command: domain.Command{ExpectedRevision: session.Revision}, Action: "close"})
	if session.State != "closed" {
		t.Fatal(session)
	}
	state, err := local.Open(context.Background(), l.worker.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	fence := remote.Fence{Active: true, ID: session.ID, Generation: "legacy_native"}
	if err := state.Put(context.Background(), "session-fence", fence); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(state.Root, "sessions")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, session.ID+".sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	before, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := remote.ReadSessionFence(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	daemon, _ := start(t, "agent-service", "--config", l.worker.configFile, "serve")
	defer daemon.stop(t)
	request := domain.AgentResetRequest{Reason: "Founder requests complete native cleanup of the exact old-format session"}
	args := []string{"--config", l.founder.configFile, "--operation-id", "native-reset-legacy", "agent", "reset", l.worker.agent.ID, "--input", "-"}
	queued := must[domain.AgentOperation](t, "agent-cli", args, request)
	var result domain.AgentOperation
	waitFor(t, 12*time.Second, func() bool {
		result = must[domain.AgentOperation](t, "agent-cli", []string{"--config", l.founder.configFile, "agent", "operation", queued.ID}, nil)
		return domain.AgentOperationTerminal(result.State)
	}, "compiled reset to actually retire the legacy fence and orphan socket")
	if result.State != "reset" || result.Result == nil || result.Result.Code != "runtime-reset" || result.Result.ModeAtCheck != "manual" {
		t.Fatal("compiled reset still stranded a recoverable legacy session", result)
	}
	after, err := remote.ReadSessionFence(context.Background(), state)
	if err != nil || after.Fence.Active {
		t.Fatal("successful reset did not retire the actual legacy fence", after, err)
	}
	retired := filepath.Join(directory, "retired", domain.Hash(session.ID + ":" + snapshot.Fingerprint)[:32]+".sock")
	preserved, err := os.Lstat(retired)
	if err != nil || !os.SameFile(before, preserved) {
		t.Fatal("compiled cleanup discarded legacy evidence", err)
	}
	replacement := remote.Fence{Active: true, ID: session.ID, Generation: "replacement_fence"}
	if err := state.Put(context.Background(), "session-fence", replacement); err != nil {
		t.Fatal(err)
	}
	replayed := must[domain.AgentOperation](t, "agent-cli", args, request)
	if replayed.ID != result.ID || replayed.State != "reset" {
		t.Fatal("exact replay created another reset", replayed)
	}
	current, err := remote.ReadSessionFence(context.Background(), state)
	if err != nil || current.Fence != replacement {
		t.Fatal("replay erased a newer legacy fence", current, err)
	}
	journals, err := state.Journals(context.Background(), false)
	if err != nil || len(journals) != 0 {
		t.Fatal("reset started provider inference", journals, err)
	}
}
func TestNativeFounderOnlyRuntimeResetIncludingSelf(t *testing.T) {
	l := newLab(t, "codex")
	paused := must[domain.Agent](t, "agent-cli", []string{"--config", l.founder.configFile, "agent", "mode", l.worker.agent.ID, "--input", "-"},
		domain.ModeRequest{Command: domain.Command{ExpectedRevision: l.worker.agent.Revision}, Mode: "manual", Reason: "Keep isolated native reset fixture paused"})
	if paused.Mode != "manual" {
		t.Fatal(paused)
	}
	for _, target := range []*worker{l.worker, l.founder} {
		t.Run(target.agent.Name, func(t *testing.T) {
			daemon, _ := start(t, "agent-service", "--config", target.configFile, "serve")
			defer daemon.stop(t)
			waitFor(t, 10*time.Second, func() bool {
				mode := must[map[string]string](t, "agent-cli", []string{"--config", target.configFile, "mode", "show"}, nil)
				return mode["mode"] == "manual"
			}, "native worker to observe its authoritative manual mode")
			configBefore, err := os.ReadFile(target.configFile)
			if err != nil {
				t.Fatal(err)
			}
			identityBefore := must[domain.AgentIdentity](t, "agent-cli", []string{"--config", target.configFile, "agent", "identity"}, nil)
			reason := domain.AgentResetRequest{Reason: "Founder resets the responsive runtime without erasing any durable state"}
			operationID := "native-reset-" + target.agent.ID
			args := []string{"--config", l.founder.configFile, "--operation-id", operationID, "agent", "reset", target.agent.ID, "--input", "-"}
			queued := must[domain.AgentOperation](t, "agent-cli", args, reason)
			if queued.State != "queued" || queued.Result != nil || queued.Kind != "runtime-reset" {
				t.Fatal("compiled reset submission claimed execution", queued)
			}
			var result domain.AgentOperation
			waitFor(t, 12*time.Second, func() bool {
				result = must[domain.AgentOperation](t, "agent-cli", []string{"--config", l.founder.configFile, "agent", "operation", queued.ID}, nil)
				return domain.AgentOperationTerminal(result.State)
			}, "native Founder-authorized runtime reset")
			if result.State != "reset" || result.Result == nil || result.Result.ModeAtCheck != "manual" || result.Result.AutomationMayResume {
				t.Fatal("native reset changed pause or failed", result)
			}
			replayed := must[domain.AgentOperation](t, "agent-cli", args, reason)
			if replayed.ID != result.ID {
				t.Fatal("compiled CLI replay created another reset", replayed, result)
			}
			envelope, code, _ := run(t, "agent-cli", []string{"--config", l.worker.configFile, "agent", "reset", target.agent.ID, "--input", "-"}, reason)
			if code != 4 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Denied {
				t.Fatal("compiled non-Founder CLI could reset an agent", envelope, code)
			}
			configAfter, err := os.ReadFile(target.configFile)
			if err != nil || !bytes.Equal(configBefore, configAfter) {
				t.Fatal("reset changed the worker configuration", err)
			}
			identityAfter := must[domain.AgentIdentity](t, "agent-cli", []string{"--config", target.configFile, "agent", "identity"}, nil)
			if !reflect.DeepEqual(identityBefore, identityAfter) {
				t.Fatal("native reset changed the enrolled identity", identityBefore, identityAfter)
			}
			state, err := local.Open(context.Background(), target.config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			journals, readErr := state.Journals(context.Background(), false)
			closeErr := state.Close()
			if readErr != nil || closeErr != nil || len(journals) != 0 {
				t.Fatal("reset started provider inference", journals, readErr, closeErr)
			}
		})
	}
}
