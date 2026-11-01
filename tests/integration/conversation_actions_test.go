//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type conversationExecutor struct {
	base        executor
	t           *testing.T
	ref         string
	marker      string
	planning    int
	executions  int
	sawThread   bool
	requestNext *bool
}

func (x *conversationExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	return x.base.Check(ctx, name)
}

func (x *conversationExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	_, data, ok := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !ok {
		x.t.Fatal("invocation omitted explicit controller context")
	}
	var launch work.Launch
	if err := json.Unmarshal([]byte(data), &launch); err != nil {
		x.t.Fatal(err)
	}
	if invocation.Started != nil {
		if err := invocation.Started(123456); err != nil {
			return provider.Reply{}, err
		}
	}
	if !invocation.Coding {
		if launch.Phase != "message" && launch.Phase != "attention" {
			x.t.Fatal("unexpected action planning phase", launch.Phase)
		}
		x.planning++
		for _, message := range launch.Thread {
			if strings.Contains(message.Body, x.marker) {
				x.sawThread = true
			}
		}
		return provider.Reply{
			Outcome: "responded", Summary: "Need to inspect the matching skill and authorized workspace before answering.",
			Action: &provider.ConversationAction{Instructions: "Read the matching fixture skill, retrieve relevant context, write the requested result, and check its contents.", WorkspaceRef: x.ref},
		}, nil
	}
	x.executions++
	if launch.Phase != domain.ExecutionRun || launch.ConversationID == "" || launch.Task == nil ||
		launch.Task.ProjectID != domain.AdhocProjectID || launch.Task.AssigneeID != launch.AgentID ||
		!strings.Contains(invocation.Prompt, "Inspect applicable local/project skills") {
		x.t.Fatal("action was not a real admitted Adhoc execution", launch)
	}
	skill, err := os.ReadFile(filepath.Join(invocation.Workspace, ".agents", "skills", "fixture-action", "SKILL.md"))
	if err != nil || !strings.Contains(string(skill), "Write grounded-answer.txt") {
		x.t.Fatal("applicable skill was not available in the actual granted workspace", err)
	}
	if err := os.WriteFile(filepath.Join(invocation.Workspace, "grounded-answer.txt"), []byte("checked result from relevant context"), 0600); err != nil {
		return provider.Reply{}, err
	}
	checked, err := os.ReadFile(filepath.Join(invocation.Workspace, "grounded-answer.txt"))
	if err != nil || string(checked) != "checked result from relevant context" {
		x.t.Fatal("actual action result was not verified", err)
	}
	return provider.Reply{
		Outcome: "completed", Summary: "Wrote and checked the requested result after reading the relevant skill.",
		Evidence: []string{"fixture-action skill read", "grounded-answer.txt contents checked"}, ResponseRequested: x.requestNext,
	}, nil
}

func prepareConversationExecutor(t *testing.T, h *harness) (*work.Engine, *conversationExecutor, string) {
	t.Helper()
	x := &conversationExecutor{t: t, marker: "EARLIER_RELEVANT_CONTEXT"}
	engine := newEngine(t, h, &x.base)
	engine.Executor = x
	skipHealthOnboarding(t, engine)
	workspace := filepath.Join(t.TempDir(), "granted-workspace")
	skills := filepath.Join(workspace, ".agents", "skills", "fixture-action")
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "SKILL.md"), []byte("---\nname: fixture-action\ndescription: Fixture result writing\n---\nWrite grounded-answer.txt and verify its contents.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := engine.State.BindWorkspace(context.Background(), workspace, true, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.ref = binding.Ref
	return engine, x, workspace
}

func TestLaterThreadActionRetrievesContextExecutesRecordedAdhocAndReportsActualResult(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	engine, x, workspace := prepareConversationExecutor(t, h)
	root, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Kind: "direct", Origin: "human",
		Body: x.marker + " PRIVATE_REQUEST_CONTEXT", ResponseRequest: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := api.Call[domain.Message](ctx, h.worker, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.founderAgent.ID, Kind: "direct", Origin: "ai",
		Body: "Earlier response", ReplyToID: root.ID, ResponseRequest: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	followup, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Kind: "direct", Origin: "human",
		Body: "Act on the earlier context using the matching skill.", ReplyToID: prior.ID, ResponseRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "grounded-answer.txt")); !os.IsNotExist(err) || x.executions != 0 {
		t.Fatal("a text planning acknowledgement was mistaken for execution", err, x.executions)
	}
	engine = restartAuthenticationEngine(t, engine)
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal("durable action did not resume and execute after restart", err)
	}
	if x.planning != 1 || x.executions != 1 || !x.sawThread {
		t.Fatal("later-thread context or exact-once invocation was lost", x.planning, x.executions, x.sawThread)
	}
	tasks, err := api.Call[domain.Page[domain.Task]](ctx, h.worker, "GET", "/v1/tasks?project="+domain.AdhocProjectID, nil)
	if err != nil || len(tasks.Items) != 1 {
		t.Fatal("action did not create exactly one special-project task", tasks, err)
	}
	task := tasks.Items[0]
	if task.ProjectID != domain.AdhocProjectID || task.AssigneeID != h.workerAgent.ID || task.Status != domain.Completed ||
		task.Result == nil || strings.Contains(task.Description+task.Result.Summary, "PRIVATE_REQUEST_CONTEXT") {
		t.Fatal("task record was not completed, self-assigned or privacy-preserving", task)
	}
	thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", followup.ID, "thread"), nil)
	if err != nil || len(thread) != 4 {
		t.Fatal("actual action result was not delivered to the existing thread", thread, err)
	}
	var result domain.Message
	for _, message := range thread {
		if message.ReplyToID == followup.ID {
			result = message
		}
	}
	if result.Origin != "ai" || !result.ResponseRequest || !strings.Contains(result.Body, "contents checked") || !strings.Contains(result.Body, task.ID) {
		t.Fatal("reply omitted actual checks, task reference or conversational continuation", result)
	}
	if _, err := engine.Step(ctx, false); err != nil || x.executions != 1 || x.planning != 1 {
		t.Fatal("finished action was executed or planned again", err, x.executions, x.planning)
	}
}

func TestAssignedUnmentionedCommentActionUsesSkillsAndRepliesToThatComment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	engine, x, _ := prepareConversationExecutor(t, h)
	project, err := api.Call[domain.Project](ctx, h.founder, "POST", "/v1/projects", domain.ProjectRequest{Command: command(), Name: "Comment action fixture"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := api.Call[domain.Task](ctx, h.worker, "POST", "/v1/tasks", domain.TaskRequest{
		Command: command(), ProjectID: project.ID, Title: "Source discussion", Description: "Source context.", AcceptanceCriteria: "Keep source context.",
	})
	if err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.founder, task.ID, "assign", func(change *domain.TaskChange) { change.AgentID = h.workerAgent.ID })
	comment, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"),
		domain.EntryRequest{Command: command(), Kind: "comment", Body: "Please inspect the matching skill and carry out the local action."})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := engine.Step(ctx, false); err != nil {
			t.Fatal("assigned comment action did not execute", err)
		}
	}
	entries, err := api.Call[domain.Page[domain.Entry]](ctx, h.founder, "GET", api.Path("tasks", task.ID, "entries"), nil)
	if err != nil {
		t.Fatal(err)
	}
	replies := 0
	for _, entry := range entries.Items {
		if entry.ParentID == comment.ID && entry.AuthorID == h.workerAgent.ID && strings.Contains(entry.Body, "contents checked") {
			replies++
		}
	}
	if replies != 1 || x.executions != 1 {
		t.Fatal("assigned comment action was only acknowledged, duplicated or attached to the wrong parent", replies, x.executions)
	}
	pending, err := api.Call[domain.Page[domain.Attention]](ctx, h.worker, "GET", "/v1/attention", nil)
	if err != nil || len(pending.Items) != 0 {
		t.Fatal("completed assigned comment retained pending attention", pending, err)
	}
}

type lostConversationReceiptAck struct {
	base http.RoundTripper
	lost atomic.Bool
}

func (transport *lostConversationReceiptAck) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil && request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/receipt") && transport.lost.CompareAndSwap(false, true) {
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		return nil, errors.Join(errors.New("fixture lost conversation receipt acknowledgement"), readErr, closeErr)
	}
	return response, err
}

func TestAdhocActionLostReceiptAndRestartReplaysWithoutRepeatingWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	engine, x, _ := prepareConversationExecutor(t, h)
	noResponse := false
	x.requestNext = &noResponse
	source, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Kind: "direct", Origin: "human",
		Body: "Carry out the authorized fixture action.", ResponseRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	transport := &lostConversationReceiptAck{base: h.worker.HTTP.Transport}
	h.worker.HTTP.Transport = transport
	defer func() { h.worker.HTTP.Transport = transport.base }()
	if _, err := engine.Step(ctx, false); err == nil || !transport.lost.Load() {
		t.Fatal("fixture did not lose the actual committed receipt acknowledgement", err)
	}
	engine = restartAuthenticationEngine(t, engine)
	if _, err := engine.Step(ctx, false); err != nil || x.executions != 1 || x.planning != 1 {
		t.Fatal("receipt replay repeated inference/action or failed after restart", err, x.executions, x.planning)
	}
	thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
	if err != nil || len(thread) != 2 {
		t.Fatal("lost acknowledgement produced a duplicate result", thread, err)
	}
	for _, message := range thread {
		if message.ReplyToID == source.ID && message.ResponseRequest {
			t.Fatal("generated action reply ignored an explicit AI opt-out", message)
		}
	}
}

func TestQueuedAdhocActionWaitsForExplicitFounderResumption(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	engine, x, _ := prepareConversationExecutor(t, h)
	if _, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Kind: "direct", Origin: "human",
		Body: "Carry out the authorized fixture action.", ResponseRequest: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	setMode := func(mode string) {
		t.Helper()
		me, err := api.Call[domain.Agent](ctx, h.worker, "GET", "/v1/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", me.ID, "mode"), domain.ModeRequest{
			Command: domain.Command{OperationID: domain.NewID("mode"), ExpectedRevision: me.Revision},
			Mode:    mode, Reason: "Founder explicitly controls this isolated action fixture",
		}); err != nil {
			t.Fatal(err)
		}
	}
	setMode("manual")
	if status, err := engine.Step(ctx, false); err != nil || status.State != "manual" || x.executions != 0 {
		t.Fatal("queued action bypassed Founder pause", status, err, x.executions)
	}
	setMode("ai")
	if _, err := engine.Step(ctx, false); err != nil || x.executions != 1 {
		t.Fatal("authorized queued action did not continue after Founder resumption", err, x.executions)
	}
}

func TestDisposedConversationActionDoesNotExecuteItsStalePlan(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	engine, x, workspace := prepareConversationExecutor(t, h)
	source, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Kind: "direct", Origin: "human",
		Body: "Carry out the authorized fixture action.", ResponseRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Message](ctx, h.worker, "POST", api.Path("messages", source.ID, "receipt"),
		domain.MessageReceipt{Command: domain.Command{OperationID: domain.NewID("handled"), ExpectedRevision: source.Revision}, State: "handled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(ctx, false); err != nil || x.executions != 0 {
		t.Fatal("a disposed source caused its old action plan to execute", err, x.executions)
	}
	if _, err := os.Stat(filepath.Join(workspace, "grounded-answer.txt")); !os.IsNotExist(err) {
		t.Fatal("stale plan produced an external effect", err)
	}
	pending, err := engine.State.PendingConversationActions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("stale plan remained silently eligible for retry", pending, err)
	}
}
