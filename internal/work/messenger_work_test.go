package work_test

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/testidentity"
	"github.com/thtpark/fdn/internal/work"
	"net/http"
)

type roomFakeExecutor struct {
	calls []work.Launch
}

func (f *roomFakeExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	return provider.Health{
		Provider:       name,
		Version:        "1.0.0",
		Path:           "/bin/echo",
		Authentication: "authenticated",
		Models:         []string{"fixture-model"},
	}, nil
}

func (f *roomFakeExecutor) Invoke(_ context.Context, inv provider.Invocation) (provider.Reply, error) {
	_, raw, ok := strings.Cut(inv.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !ok {
		return provider.Reply{}, domain.Fail(domain.Internal, "missing context JSON")
	}
	var launch work.Launch
	if err := config.Decode([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	f.calls = append(f.calls, launch)
	if launch.Phase == "onboarding" {
		return provider.Reply{Outcome: "reviewed", Summary: "Ready", Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}}, nil
	}
	falseVal := false
	return provider.Reply{
		Outcome:           "responded",
		Summary:           "Reply from " + launch.AgentID + " in " + launch.Room.Kind + " room",
		Evidence:          []string{},
		Questions:         []provider.Question{},
		AnswerIDs:         []string{},
		ResponseRequested: &falseVal,
	}, nil
}

func TestWorkerProcessesGroupAndProjectRoomMessagesWithReadBeforeInvoke(t *testing.T) {
	ctx := context.Background()
	rawRoot := t.TempDir()
	root, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	storeDir := filepath.Join(root, "server")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := foundation.OpenWithFiles(ctx, filepath.Join(storeDir, "foundation.sqlite"),
		foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(func(context.Context, string, []uint32) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	founder, err := testidentity.Register(ctx, store, "", "Founder", 5100)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := api.NewAuthenticated(store, logger, func(r *http.Request) (domain.Agent, error) {
		return founder, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	clientFor := func(agentID string) *api.Client {
		pipeCtx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		c := api.NewMessageClient("fixture://foundation", func(context.Context) (net.Conn, error) {
			left, right := net.Pipe()
			wg.Go(func() {
				defer left.Close()
				_ = srv.ServeTrustedMessages(pipeCtx, left, agentID)
			})
			return right, nil
		})
		c.Credential.AgentID = agentID
		t.Cleanup(func() {
			c.Close()
			cancel()
			wg.Wait()
		})
		return c
	}
	founderClient := clientFor(founder.ID)

	nextUID := int64(5101)
	makeWorker := func(name string) (domain.Agent, *work.Engine, *roomFakeExecutor, config.Worker) {
		agent, err := testidentity.Register(ctx, store, founder.ID, name, nextUID)
		if err != nil {
			t.Fatal(err)
		}
		nextUID++
		wDir := filepath.Join(root, name)
		if err := os.MkdirAll(wDir, 0700); err != nil {
			t.Fatal(err)
		}
		cfg := config.DefaultWorker(wDir)
		cfg.Mode = "ai"
		cfg.Provider = "copilot"
		cfg.CAFile = filepath.Join(root, "ca.pem")
		cfg.CredentialFile = filepath.Join(wDir, "cred.json")
		state, err := local.Open(ctx, cfg.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { state.Close() })
		exec := &roomFakeExecutor{}
		eng := work.New(cfg, "", clientFor(agent.ID), state, exec)
		eng.Log = logger
		// Complete initial onboarding step.
		if _, err := eng.Step(ctx, false); err != nil {
			t.Fatal(err)
		}
		return agent, eng, exec, cfg
	}

	agent1, eng1, exec1, _ := makeWorker("Agent1")
	agent2, eng2, exec2, _ := makeWorker("Agent2")

	// Create a project with Agent1 as delegate and Agent2 as assignee, plus project notes.
	project, err := api.Call[domain.Project](ctx, founderClient, "POST", "/v1/projects", domain.ProjectRequest{
		Command:     domain.Command{OperationID: domain.NewID("op")},
		Name:        "Realtime Platform",
		Description: "Project room test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Project](ctx, founderClient, "POST", api.Path("projects", project.ID, "delegates"), domain.GrantRequest{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: project.Revision},
		AgentID: agent1.ID,
	}); err != nil {
		t.Fatal(err)
	}
	noteBody := "# Project Guidance\nAlways cite latency numbers."
	if _, err := api.Call[domain.Note](ctx, founderClient, "POST", "/v1/notes", domain.NoteRequest{
		Command:  domain.Command{OperationID: domain.NewID("op")},
		Scope:    domain.ProjectNotes,
		TargetID: project.ID,
		Content:  &noteBody,
		Reason:   "Seed project guidance",
	}); err != nil {
		t.Fatal(err)
	}
	task, err := api.Call[domain.Task](ctx, founderClient, "POST", "/v1/tasks", domain.TaskRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		ProjectID: project.ID,
		Title:     "Spec item",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Task](ctx, founderClient, "POST", api.Path("tasks", task.ID, "change"), domain.TaskChange{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
		Kind:    "assign",
		AgentID: agent2.ID,
		Reason:  "Assign to Agent2",
	}); err != nil {
		t.Fatal(err)
	}

	roomID := domain.ProjectRoomID(project.ID)
	// 1) Unmentioned message in Project Room -> BOTH Agent1 and Agent2 respond!
	sentAll, err := api.Call[domain.RoomMessage](ctx, founderClient, "POST", api.Path("rooms", roomID, "messages"), domain.RoomMessageRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
		Body:    "Status check for the whole project team!",
		Origin:  "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	if st1, err := eng1.Step(ctx, false); err != nil || st1.State != "message" {
		t.Fatalf("Agent1 did not process unmentioned project room message: state=%+v err=%v", st1, err)
	}
	if st2, err := eng2.Step(ctx, false); err != nil || st2.State != "message" {
		t.Fatalf("Agent2 did not process unmentioned project room message: state=%+v err=%v", st2, err)
	}

	// Verify read receipts from BOTH agents and quoted replies in the room!
	updatedAll, err := api.Call[domain.RoomMessage](ctx, founderClient, "GET", api.Path("room-messages", sentAll.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedAll.ReadBy) != 2 {
		t.Fatalf("expected read receipts from both Agent1 and Agent2 before invoke, got %+v", updatedAll.ReadBy)
	}
	// Verify project shared notes were supplied to the invocation!
	lastCall := exec1.calls[len(exec1.calls)-1]
	if !slices.ContainsFunc(lastCall.SharedNotes, func(n domain.Note) bool {
		return n.Scope == domain.ProjectNotes && n.TargetID == project.ID && strings.Contains(n.Content, "Always cite latency numbers")
	}) {
		t.Fatalf("project room invocation missed project shared notes: %+v", lastCall.SharedNotes)
	}

	// 2) Mentioned message (@Agent1) -> ONLY Agent1 responds; Agent2 stays idle!
	before2 := len(exec2.calls)
	if _, err := api.Call[domain.RoomMessage](ctx, founderClient, "POST", api.Path("rooms", roomID, "messages"), domain.RoomMessageRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Body:      "@Agent1 follow up on this specifically",
		Origin:    "human",
		ReplyToID: sentAll.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if st1, err := eng1.Step(ctx, false); err != nil || st1.State != "message" {
		t.Fatalf("Agent1 did not process @Agent1 project room message: state=%+v err=%v", st1, err)
	}
	if _, err := eng2.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(exec2.calls) != before2 {
		t.Fatalf("Agent2 should not have replied when @Agent1 was specifically mentioned")
	}

	page, err := api.Call[domain.Page[domain.RoomMessage]](ctx, founderClient, "GET", api.Path("rooms", roomID, "messages")+"?limit=20", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 { // sentAll + 2 replies + @Agent1 + 1 reply
		t.Fatalf("expected 5 total messages in project room, got %d", len(page.Items))
	}
}
