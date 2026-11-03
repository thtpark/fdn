//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/provider"
)

type stoppedPlanningFileExecutor struct {
	*fileRequestExecutor
	stoppedSource string
	stoppedCalls  int
}

func (executor *stoppedPlanningFileExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	var launch struct {
		Message *domain.Message `json:"message"`
	}
	_, data, found := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !found || json.Unmarshal([]byte(data), &launch) != nil {
		executor.t.Fatal("file catch-up lost its exact source context")
	}
	if !invocation.Coding && launch.Message != nil && launch.Message.ID == executor.stoppedSource {
		executor.stoppedCalls++
		return provider.Reply{}, domain.Fail(domain.Unavailable, "synthetic settled authority transport interruption")
	}
	return executor.fileRequestExecutor.Invoke(ctx, invocation)
}

func TestPausedFileMessageCatchesUpDespiteUnrelatedSettledPlanningFailure(t *testing.T) {
	h := newHarnessWithFiles(t, foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
		func(context.Context, string, []uint32) error { return nil })})
	ctx := context.Background()
	base := &fileRequestExecutor{t: t, preferred: "codex", content: []byte("4\n5\n6\n")}
	engine := newEngine(t, h, &base.base)
	engine.Config.Provider, engine.Config.Model = "codex", "fixture-model"
	x := &stoppedPlanningFileExecutor{fileRequestExecutor: base}
	engine.Executor, base.engine = x, engine
	skipHealthOnboarding(t, engine)
	older, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", map[string]any{
		"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "origin": "human", "kind": "direct", "body": "An unrelated older question."})
	if err != nil {
		t.Fatal(err)
	}
	x.stoppedSource = older.ID
	if _, err := engine.Step(ctx, false); err != nil || x.stoppedCalls != 1 {
		t.Fatal("settled text-only failure became a global work fence", x.stoppedCalls, err)
	}
	mode := func(value string) {
		t.Helper()
		current, err := api.Call[domain.Agent](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", current.ID, "mode"), domain.ModeRequest{
			Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: current.Revision},
			Mode:    value, Reason: "Explicit Founder pause/resume around a queued private file request"})
		if err != nil {
			t.Fatal(err)
		}
	}
	mode("manual")
	file, err := h.founder.UploadFile(ctx, domain.FileUploadRequest{Command: command(), Name: "numbers.txt",
		MediaType: "text/plain", Size: int64(len(base.content)), SHA256: domain.Hash(string(base.content)),
		TransferTo: h.workerAgent.ID}, bytes.NewReader(base.content))
	if err != nil {
		t.Fatal(err)
	}
	base.file = file
	source, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", map[string]any{
		"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID,
		"origin": "human", "kind": "direct",
		"body":           "Analyze the numbers in private file " + file.ID + " and save their total.",
		"attachment_ids": []string{file.ID}})
	if err != nil || !source.ResponseRequest {
		t.Fatal("queued file instructions lost their requested reply", source, err)
	}
	if status, err := engine.Step(ctx, false); err != nil || status.State != "manual" ||
		x.stoppedCalls != 1 || base.planning != 0 || base.executions != 0 {
		t.Fatal("paused file/message started inference or effects", status, err)
	}
	mode("ai")
	if status, err := engine.Step(ctx, false); err != nil || status.State != "file-received" {
		t.Fatal("resume did not receive the original queued transfer", status, err)
	}
	if status, err := engine.Step(ctx, false); err != nil || status.State != "message" || base.planning != 1 {
		t.Fatal("older failed text stranded the new file instructions", status, err)
	}
	engine = restartAuthenticationEngine(t, engine)
	base.engine = engine
	if status, err := engine.Step(ctx, false); err != nil || base.executions != 1 {
		t.Fatal("recorded file analysis did not survive restart", status, err)
	}
	thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
	answered := false
	for _, response := range thread {
		answered = answered || response.ReplyToID == source.ID && strings.Contains(response.Body, "Private file total: 15")
	}
	if err != nil || len(thread) != 2 || !answered {
		t.Fatal("catch-up did not return actual file analysis", len(thread), err)
	}
	pending, err := engine.State.Journals(ctx, true)
	if err != nil || len(pending) != 0 || base.executions != 1 || x.stoppedCalls > 3 {
		t.Fatal("catch-up repeated execution or left a global text-only fence", base.executions, x.stoppedCalls, err)
	}
}
