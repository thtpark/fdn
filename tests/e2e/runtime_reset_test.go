//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

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
