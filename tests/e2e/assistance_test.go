//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/remote"
)

type unattendedEndpoint struct {
	starts          atomic.Int64
	cleanupFailures int64
}

type unattendedCleanup struct {
	io.ReadWriteCloser
	failures int64
	closes   atomic.Int64
	once     sync.Once
}

func (e *unattendedCleanup) Close() error {
	var problem error
	e.once.Do(func() { problem = e.ReadWriteCloser.Close() })
	if e.closes.Add(1) <= e.failures {
		return errors.Join(problem, &process.CleanupError{Err: errors.New("temporary fixture cleanup failure")})
	}
	return problem
}

func (e *unattendedEndpoint) Open(_ context.Context, _ domain.Session, _ config.Worker) (io.ReadWriteCloser, error) {
	e.starts.Add(1)
	left, right := net.Pipe()
	go func() {
		defer right.Close()
		fmt.Fprintln(right, "UNATTENDED_ASSISTANCE_READY")
		reader := bufio.NewReader(right)
		for {
			input, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(input, "finish-fixture") {
				return
			}
		}
	}()
	return &unattendedCleanup{ReadWriteCloser: left, failures: e.cleanupFailures}, nil
}

func TestNativeAssistanceReopensRetainedGrantWithoutTargetHumanOrReapproval(t *testing.T) {
	runNativeAssistanceReconnect(t, 0)
}

func TestNativeAssistanceAutomaticallyRecoversOwnedCleanupBeforeReconnect(t *testing.T) {
	runNativeAssistanceReconnect(t, 2)
}

func runNativeAssistanceReconnect(t *testing.T, cleanupFailures int64) {
	l := newLab(t, "codex")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grantData := l.cli(t, l.founder, []string{"assistance", "grant", "--input", "-"}, domain.AssistanceGrantRequest{
		RequesterID: l.founder.agent.ID, TargetID: l.worker.agent.ID, Mode: "tmux", Purpose: "authentication", Reason: "Founder authorizes once"})
	var grant domain.AssistanceGrant
	if err := json.Unmarshal(grantData, &grant); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, l.worker.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	endpoint := &unattendedEndpoint{cleanupFailures: cleanupFailures}
	manager := &remote.Manager{Client: l.worker.client, State: state, Config: l.worker.config, Opener: endpoint, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for attempt := range 2 {
		command := exec.Command(filepath.Join(binaries, "agent-cli"), "--config", l.founder.configFile, "assistance", "open", grant.ID, "--provider", "codex", "--ttl", "90")
		command.Env = isolatedEnvironment([]string{"--config", l.founder.configFile})
		terminal, err := pty.Start(command)
		if err != nil {
			t.Fatal(err)
		}
		finished := make(chan error, 1)
		go func() { finished <- command.Wait() }()
		output := make(chan string, 1)
		go func() {
			var captured bytes.Buffer
			scanner := bufio.NewScanner(terminal)
			for scanner.Scan() {
				line := scanner.Text()
				captured.WriteString(line + "\n")
				if strings.Contains(line, "UNATTENDED_ASSISTANCE_READY") {
					io.WriteString(terminal, "finish-fixture\n")
				}
			}
			output <- captured.String()
		}()
		select {
		case err := <-finished:
			terminal.Close()
			text := <-output
			if err != nil || !strings.Contains(text, "UNATTENDED_ASSISTANCE_READY") {
				t.Fatal("protected requester could not open unattended assistance", attempt, err, text)
			}
		case <-time.After(8 * time.Second):
			command.Process.Kill()
			<-finished
			terminal.Close()
			t.Fatal("requester could not complete an unattended session")
		}
		waitFor(t, 3*time.Second, func() bool {
			var fence remote.Fence
			_, err := state.Get(ctx, "session-fence", &fence)
			return err == nil && !fence.Active
		}, "target endpoint cleanup before reconnect")
	}
	retained := must[domain.AssistanceGrant](t, "agent-cli", []string{"--config", l.founder.configFile, "assistance", "show", grant.ID}, nil)
	if !retained.Active || retained.Revision != 1 || endpoint.starts.Load() != 2 {
		t.Fatal("reconnect required a new grant/consent or skipped target preparation", retained, endpoint.starts.Load())
	}
	l.cli(t, l.founder, []string{"assistance", "revoke", grant.ID, "--input", "-"}, domain.AssistanceRevoke{Reason: "Founder explicitly revokes"})
}
