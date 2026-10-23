//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/work"
)

func skipHealthOnboarding(t *testing.T, engine *work.Engine) {
	t.Helper()
	inventory, err := engine.State.RefreshInventory(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.State.Put(context.Background(), "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerHeartbeatPublishesLiveReplyWithoutWaitingForInference(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	release := make(chan struct{})
	x := &executor{block: release, entered: make(chan struct{})}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID,
		Body: "PRIVATE_REQUEST_BODY_MUST_NOT_ENTER_HEALTH", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- engine.Serve(serveCtx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("worker did not stop after health test")
		}
	})
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("requested reply never reached the fixture provider")
	}
	deadline := time.Now().Add(10 * time.Second)
	var diagnosis domain.AgentDiagnostics
	for {
		diagnosis, err = api.Call[domain.AgentDiagnostics](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "diagnostics"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if diagnosis.Health != nil && diagnosis.Health.Phase == "invoking" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("independent heartbeat never published the in-flight reply", diagnosis)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if diagnosis.Stale || diagnosis.Health.ActionID != message.ID || diagnosis.Health.DeadlineAt <= diagnosis.Health.ObservedAt || diagnosis.Health.StepFinishedAt != 0 {
		t.Fatal("fresh live reply was hidden or marked completed", diagnosis)
	}
	data, _ := json.Marshal(diagnosis)
	if strings.Contains(string(data), message.Body) || strings.Contains(string(data), "synthetic/provider") {
		t.Fatal("private request or provider path leaked into central diagnostics", string(data))
	}
	progress, err := api.Call[domain.MessageDiagnostics](ctx, h.founder, "GET", api.Path("messages", message.ID, "diagnostics"), nil)
	if err != nil || progress.State != "generating" {
		t.Fatal("sender cannot explain the pending reply", progress, err)
	}
	close(release)
	for {
		status, err := engine.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Health != nil && status.Health.LastActionID == message.ID && status.Health.Phase == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successful response never recorded business progress", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	progress, err = api.Call[domain.MessageDiagnostics](ctx, h.founder, "GET", api.Path("messages", message.ID, "diagnostics"), nil)
	if err != nil || progress.State != "handled" || x.count("message") != 1 {
		t.Fatal("reply receipt or exactly-once invocation changed", progress, err, x.count("message"))
	}
}

func TestSignedInWorkerExplainsSessionFenceWithoutRetryingOrClearingIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &executor{}
	engine := newEngine(t, h, x)
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID,
		Body: "Reply after sign-in", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: "session_retained_private"}); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Step(ctx, false)
	if err == nil || status.Health == nil || status.Health.Authentication != "authenticated" || status.Health.Blocker != "session-fence" {
		t.Fatal("successful sign-in hid the actual reply blocker", status, err)
	}
	if _, err := api.Call[domain.Agent](ctx, h.worker, "POST", "/v1/heartbeat", domain.HeartbeatRequest{Health: status.Health}); err != nil {
		t.Fatal(err)
	}
	diagnosis, err := api.Call[domain.AgentDiagnostics](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "diagnostics"), nil)
	if err != nil || diagnosis.Health == nil || diagnosis.Health.Blocker != "session-fence" {
		t.Fatal("Founder could not inspect the retained fence", diagnosis, err)
	}
	progress, err := api.Call[domain.MessageDiagnostics](ctx, h.founder, "GET", api.Path("messages", message.ID, "diagnostics"), nil)
	if err != nil || progress.State != "blocked" || x.count("message") != 0 {
		t.Fatal("diagnosis bypassed the session fence or hid blocked message", progress, err)
	}
	var fence remote.Fence
	if _, err := engine.State.Get(ctx, "session-fence", &fence); err != nil || !fence.Active {
		t.Fatal("read-only diagnostic cleared cleanup evidence", fence, err)
	}
	ordinary, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID,
		Body: "Information only", Origin: "human", Kind: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	progress, err = api.Call[domain.MessageDiagnostics](ctx, h.founder, "GET", api.Path("messages", ordinary.ID, "diagnostics"), nil)
	if err != nil || progress.State != "not-requested" {
		t.Fatal("non-response message was treated as a stalled reply", progress, err)
	}
}
