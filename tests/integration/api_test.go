//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/testidentity"
)

type harness struct {
	store                                 *foundation.Store
	service                               *api.Server
	server                                *httptest.Server
	root                                  string
	founder, worker, other                *api.Client
	founderAgent, workerAgent, otherAgent domain.Agent
}

func command() domain.Command { return domain.Command{OperationID: domain.NewID("op")} }

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "foundation")
	store, err := foundation.Open(ctx, filepath.Join(root, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(root); err != nil {
		t.Fatal(err)
	}
	// This adapter preserves business-policy coverage, not the production identity boundary.
	service, err := api.NewAuthenticated(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func(r *http.Request) (domain.Agent, error) {
		return testidentity.Authenticate(store, r)
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(service.Handler())
	server.TLS, err = pki.Server(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	h := &harness{store: store, service: service, server: server, root: root}
	t.Cleanup(func() {
		for _, client := range []*api.Client{h.founder, h.worker, h.other} {
			if client != nil {
				client.Close()
			}
		}
		service.Close()
		server.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	h.founder, h.founderAgent = h.enroll(t, "Founder", "", 4100)
	for _, name := range []string{"worker", "other"} {
		uid := int64(4101)
		if name == "other" {
			uid++
		}
		client, agent := h.enroll(t, name, h.founderAgent.ID, uid)
		if name == "worker" {
			h.worker, h.workerAgent = client, agent
		} else {
			h.other, h.otherAgent = client, agent
		}
	}
	return h
}

func (h *harness) enroll(t *testing.T, name, founder string, uid int64) (*api.Client, domain.Agent) {
	t.Helper()
	c, err := api.NewClient(h.server.URL, filepath.Join(h.root, "ca.pem"), "", config.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := testidentity.Register(context.Background(), h.store, founder, name, uid)
	if err != nil {
		t.Fatal(err)
	}
	c.Credential = config.Credential{AgentID: agent.ID}
	c.HTTP.Transport = testidentity.Transport(c.HTTP.Transport, agent.ID)
	return c, agent
}

func (h *harness) change(t *testing.T, c *api.Client, id, kind string, set func(*domain.TaskChange)) domain.Task {
	t.Helper()
	ctx := context.Background()
	task, err := api.Call[domain.Task](ctx, c, "GET", api.Path("tasks", id), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.TaskChange{Command: command(), Kind: kind, Reason: "integration decision"}
	req.ExpectedRevision = task.Revision
	if set != nil {
		set(&req)
	}
	result, err := api.Call[domain.Task](ctx, c, "POST", api.Path("tasks", id, "change"), req)
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return result
}

func (h *harness) ready(t *testing.T) domain.Task {
	t.Helper()
	ctx := context.Background()
	project, err := api.Call[domain.Project](ctx, h.founder, "POST", "/v1/projects", domain.ProjectRequest{Command: command(), Name: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := api.Call[domain.Task](ctx, h.worker, "POST", "/v1/tasks", domain.TaskRequest{Command: command(), ProjectID: project.ID, Title: "integration", Description: "Write the specified file", AcceptanceCriteria: "File content verified"})
	if err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.founder, task.ID, "assign", func(r *domain.TaskChange) { r.AgentID = h.workerAgent.ID; r.Provider = "codex" })
	task = h.change(t, h.worker, task.ID, "workspace", func(r *domain.TaskChange) { r.WorkspaceRef = "workspace_integration" })
	task = h.change(t, h.worker, task.ID, "status", func(r *domain.TaskChange) { r.Status = domain.Todo })
	task = h.change(t, h.worker, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	return h.change(t, h.founder, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
}

func TestTLSAPIEnforcesRawWritesAndExactReplay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.ready(t)
	for _, input := range []any{
		map[string]any{"operation_id": "forged-author", "expected_revision": task.Revision, "kind": "edit", "reason": "forge", "assignee_id": h.workerAgent.ID},
		domain.TaskChange{Command: domain.Command{OperationID: "forged-approval", ExpectedRevision: task.Revision}, Kind: "approve", Reason: "forge", SpecGeneration: task.SpecGeneration},
	} {
		_, err := api.Call[domain.Task](ctx, h.worker, "POST", api.Path("tasks", task.ID, "change"), input)
		if err == nil {
			t.Fatal("untrusted raw write bypassed authority")
		}
	}
	path := api.Path("tasks", task.ID, "claim")
	token := domain.NewToken()
	req := domain.ClaimRequest{Command: command(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	req.ExpectedRevision = task.Revision
	run, err := api.Call[domain.Run](ctx, h.worker, "POST", path, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := api.Call[domain.Run](ctx, h.worker, "POST", path, req)
	if err != nil || replay.ID != run.ID {
		t.Fatal("claim replay", err)
	}
	report := domain.ReportRequest{Command: command(), RunID: run.ID, Token: token, SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "finished", Evidence: []string{"local test"}}
	out, err := api.Call[domain.ReportOutcome](ctx, h.worker, "POST", api.Path("tasks", task.ID, "report"), report)
	if err != nil || out.Task.Status != domain.Completed || !out.Accepted {
		t.Fatal(out, err)
	}
	if _, err := api.Call[any](ctx, h.worker, "POST", "/v1/_bulk_docs", map[string]any{}); err == nil {
		t.Fatal("unexpected write endpoint")
	}
	if _, err := api.Call[any](ctx, h.worker, "GET", "/v1/tasks?limit=not-a-number", nil); err == nil {
		t.Fatal("invalid query accepted")
	}
}

func TestConcurrentHTTPClaimsKeepOneMachineSlot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	one, two := h.ready(t), h.ready(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := range 16 {
		task := one
		if i%2 == 1 {
			task = two
		}
		wg.Go(func() {
			req := domain.ClaimRequest{Command: command(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}
			req.ExpectedRevision = task.Revision
			_, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), req)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else if domain.AsFault(err).Code != domain.Conflict {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatal("active tasks for one machine", accepted)
	}
}

func TestPrivateAPIAndQueryBoundPagination(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, second := h.ready(t), h.ready(t)
	page, err := api.Call[domain.Page[domain.Task]](ctx, h.worker, "GET", "/v1/tasks?limit=1", nil)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	_, err = api.Call[domain.Page[domain.Task]](ctx, h.other, "GET", "/v1/tasks?limit=1&cursor="+page.NextCursor, nil)
	if err == nil {
		t.Fatal("borrowed actor cursor")
	}
	_, err = api.Call[domain.Page[domain.Task]](ctx, h.worker, "GET", "/v1/tasks?limit=1&project="+first.ProjectID+"&cursor="+page.NextCursor, nil)
	if err == nil {
		t.Fatal("changed query cursor")
	}
	message, err := api.Call[domain.Message](ctx, h.worker, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.founderAgent.ID, Body: "private body", Kind: "direct", Origin: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Message](ctx, h.other, "GET", api.Path("messages", message.ID), nil); err == nil {
		t.Fatal("private message exposed")
	}
	events, err := api.Call[domain.Page[domain.Event]](ctx, h.other, "GET", "/v1/events?task="+second.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := json.Marshal(events)
	if strings.Contains(string(bytes), "private body") || strings.Contains(string(bytes), message.ID) {
		t.Fatal("private event leaked")
	}
}

func TestConsentRelayTransfersBytesAndClosesOnWithdrawal(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task := h.ready(t)
	if _, err := api.Call[[]domain.Screen](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "screens"), domain.ScreensRequest{Command: command(), Screens: []domain.Screen{{ID: "relay-screen", Name: "RFB fixture screen", Scope: "screen", Width: 1280, Height: 800, Primary: true, Applications: []string{}}}}); err != nil {
		t.Fatal(err)
	}
	grant, err := api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", "/v1/assistance", domain.AssistanceGrantRequest{Command: command(), RequesterID: h.founderAgent.ID, TargetID: h.workerAgent.ID, ProjectID: task.ProjectID, Mode: "vnc", Purpose: "terminal", ScreenID: "relay-screen", Reason: "Founder authorizes the fixture screen only"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", domain.SessionRequest{Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, ProjectID: task.ProjectID, TaskID: task.ID, Mode: "vnc", Purpose: "terminal", ScreenID: grant.ScreenID, Reason: "view authorized test endpoint", ExpiresAt: time.Now().Unix() + 90})
	if err != nil {
		t.Fatal(err)
	}
	url := "wss" + strings.TrimPrefix(h.server.URL, "https") + api.Path("sessions", session.ID, "relay", "target")
	headers := http.Header{testidentity.ActorHeader: []string{h.workerAgent.ID}}
	if conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: h.worker.HTTP, HTTPHeader: headers}); err == nil {
		conn.CloseNow()
		t.Fatal("consent alone opened a route")
	}
	change := domain.SessionChange{Command: command(), Action: "open"}
	change.ExpectedRevision = session.Revision
	session, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), change)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: h.worker.HTTP, HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer target.CloseNow()
	requesterURL := "wss" + strings.TrimPrefix(h.server.URL, "https") + api.Path("sessions", session.ID, "relay", "requester")
	requester, _, err := websocket.Dial(ctx, requesterURL, &websocket.DialOptions{HTTPClient: h.founder.HTTP, HTTPHeader: http.Header{testidentity.ActorHeader: []string{h.founderAgent.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	defer requester.CloseNow()
	if err := target.Write(ctx, websocket.MessageBinary, []byte("rfb-fixture")); err != nil {
		t.Fatal(err)
	}
	_, bytes, err := requester.Read(ctx)
	if err != nil || string(bytes) != "rfb-fixture" {
		t.Fatal(string(bytes), err)
	}
	if err := requester.Write(ctx, websocket.MessageBinary, []byte("reply")); err != nil {
		t.Fatal(err)
	}
	_, bytes, err = target.Read(ctx)
	if err != nil || string(bytes) != "reply" {
		t.Fatal(string(bytes), err)
	}
	revoke := domain.AssistanceRevoke{Command: command(), Reason: "Founder withdraws the persistent grant"}
	revoke.ExpectedRevision = grant.Revision
	if _, err := api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", api.Path("assistance", grant.ID, "revoke"), revoke); err != nil {
		t.Fatal(err)
	}
	_, _, err = requester.Read(ctx)
	if err == nil || ctx.Err() != nil {
		t.Fatal("withdrawal did not promptly close relay", err)
	}
}
