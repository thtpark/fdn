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
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

func requestedHelpProject(t *testing.T, h *harness) domain.Project {
	t.Helper()
	project, err := api.Call[domain.Project](context.Background(), h.founder, "POST", "/v1/projects", domain.ProjectRequest{Command: command(), Name: "requested-help"})
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID}
	grant.ExpectedRevision = project.Revision
	project, err = api.Call[domain.Project](context.Background(), h.founder, "POST", api.Path("projects", project.ID, "delegates"), grant)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func requestedHelpTask(t *testing.T, h *harness, project, name string) domain.Task {
	t.Helper()
	task, err := api.Call[domain.Task](context.Background(), h.founder, "POST", "/v1/tasks", domain.TaskRequest{Command: command(), ProjectID: project, Title: name,
		Description: "Review the requested implementation and ask useful questions", AcceptanceCriteria: "Obtain exact current approval before coding"})
	if err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.founder, task.ID, "assign", func(change *domain.TaskChange) { change.AgentID = h.workerAgent.ID })
	return h.change(t, h.founder, task.ID, "status", func(change *domain.TaskChange) { change.Status = domain.Todo })
}

func helpRequestForTask(t *testing.T, status work.Status, id string) work.AuthenticationRequest {
	t.Helper()
	for _, request := range status.AuthenticationRequests {
		if request.TaskID == id {
			return request
		}
	}
	t.Fatal("requested task has no scoped sign-in help request", status)
	return work.AuthenticationRequest{}
}

func TestRequestedTaskEscalatesBeforeOnboardingDespiteExpiredNotice(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	old := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	engine.Now = func() time.Time { return time.Unix(old.ExpiresAt, 0) }
	requireAuthenticationBlock(t, engine, domain.Prerequisite)
	engine.Now = time.Now
	project := requestedHelpProject(t, h)
	task := requestedHelpTask(t, h, project.ID, "new requested work")
	status, err := engine.Step(context.Background(), false)
	if err == nil || status.State != "blocked" || status.InitialReviewDone || x.invocations != 0 {
		t.Fatal("blocked onboarding ignored requested work or started inference", status, err)
	}
	request := helpRequestForTask(t, status, task.ID)
	if request.State != "requested" || request.ProjectID != project.ID || request.ActionKey == "" || request.ID == old.ID || len(request.Deliveries) != 2 ||
		len(authenticationInbox(t, h.founder)) != 2 || len(authenticationInbox(t, h.other)) != 1 {
		t.Fatal("missing Founder/delegate task help escalation", request)
	}
	engine = restartAuthenticationEngine(t, engine)
	for range 3 {
		next, _ := engine.Step(context.Background(), false)
		if helpRequestForTask(t, next, task.ID).ID != request.ID || x.invocations != 0 {
			t.Fatal("poll/restart duplicated a requested-action notice", next)
		}
	}
	second := requestedHelpTask(t, h, project.ID, "a second requested action in the same project")
	next, _ := engine.Step(context.Background(), false)
	fresh := helpRequestForTask(t, next, second.ID)
	if fresh.ID == request.ID || fresh.ActionKey == request.ActionKey || len(authenticationInbox(t, h.other)) != 2 {
		t.Fatal("new work was swallowed by an old provider/project episode", next)
	}
	for range 3 {
		if _, err := engine.Step(context.Background(), false); err == nil {
			t.Fatal("sign-in blocker unexpectedly cleared")
		}
	}
	if len(authenticationInbox(t, h.founder)) != 3 || len(authenticationInbox(t, h.other)) != 2 {
		t.Fatal("multiple blocked tasks alternately recreated each other's requests")
	}
}

func TestRequestedMessageAndExplicitWorkRearmSignInWithoutIdleSpam(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	first := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	engine.Now = func() time.Time { return time.Unix(first.ExpiresAt, 0) }
	requireAuthenticationBlock(t, engine, domain.Prerequisite)
	engine.Now = time.Now
	message, err := api.Call[domain.Message](context.Background(), h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID,
		Body: "Please review the pending request", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	status, err := engine.Step(context.Background(), false)
	if err == nil || len(status.AuthenticationRequests) != 2 || x.invocations != 0 {
		t.Fatal("a direct requested response did not rearm blocked onboarding", status, err)
	}
	found := false
	for _, request := range status.AuthenticationRequests {
		found = found || strings.Contains(request.ActionKey, message.ID)
	}
	if !found {
		t.Fatal("message request lost its durable action identity", status)
	}
	ctx := work.WithRequestedAction(context.Background(), "explicit-help-attempt")
	for range 2 {
		if _, err := engine.Step(ctx, true); err == nil {
			t.Fatal("explicit request bypassed authentication")
		}
	}
	if len(authenticationInbox(t, h.founder)) != 3 || x.invocations != 0 {
		t.Fatal("explicit request replay duplicated help or started inference")
	}
}

func TestRequestedPermissionBlockEscalatesWithoutGrantingWorkspaceAccess(t *testing.T) {
	h := newHarness(t)
	base := &executor{}
	engine := newEngine(t, h, base)
	task := readyForEngine(t, h, engine)
	project, err := api.Call[domain.Project](context.Background(), h.founder, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID}
	grant.ExpectedRevision = project.Revision
	if _, err := api.Call[domain.Project](context.Background(), h.founder, "POST", api.Path("projects", project.ID, "delegates"), grant); err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.founder, task.ID, "approve", func(change *domain.TaskChange) { change.SpecGeneration = task.SpecGeneration })
	inventory, err := engine.State.RefreshInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.State.Put(context.Background(), "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := engine.State.EnableWorkspace(context.Background(), task.WorkspaceRef, false); err != nil {
		t.Fatal(err)
	}
	var first string
	for range 3 {
		status, err := engine.Step(context.Background(), false)
		if err != nil || status.State != "blocked" || len(status.PermissionRequests) != 1 {
			t.Fatal("permission blocker did not expose a durable help request", status, err)
		}
		request := status.PermissionRequests[0]
		if request.TaskID != task.ID || request.Requirement != "workspace" || request.State != "requested" || len(request.Deliveries) != 2 {
			t.Fatal("workspace permission help lost its current task/authorities", request)
		}
		if first != "" && request.ID != first {
			t.Fatal("permission polling minted a new help request", request)
		}
		first = request.ID
	}
	if _, err := engine.State.Workspace(context.Background(), task.WorkspaceRef); err == nil || len(base.phases) != 0 {
		t.Fatal("asking for permission enabled tools or started inference", err, base.phases)
	}
	for _, client := range []*api.Client{h.founder, h.other} {
		page, err := api.Call[domain.Page[domain.Message]](context.Background(), client, "GET", "/v1/messages?limit=100", nil)
		if err != nil || len(page.Items) != 1 || page.Items[0].Kind != "permission" || page.Items[0].ResponseRequest {
			t.Fatal("permission handoff was not private/protected and exactly once", page, err)
		}
	}
}

func TestRequestedTaskHelpLostAcknowledgementReplaysSameScopedNotice(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	project := requestedHelpProject(t, h)
	task := requestedHelpTask(t, h, project.ID, "delivery uncertainty")
	original := h.worker.HTTP.Transport
	transport := &authenticationTransport{base: original, mode: "lost-ack"}
	h.worker.HTTP.Transport = transport
	defer func() { h.worker.HTTP.Transport = original }()
	first, err := engine.Step(context.Background(), false)
	if err == nil {
		t.Fatal("lost delivery acknowledgement was hidden")
	}
	request := helpRequestForTask(t, first, task.ID)
	engine = restartAuthenticationEngine(t, engine)
	second, _ := engine.Step(context.Background(), false)
	if next := helpRequestForTask(t, second, task.ID); next.ID != request.ID || next.ExpiresAt != request.ExpiresAt || next.State != "requested" ||
		len(authenticationInbox(t, h.founder)) != 1 || len(authenticationInbox(t, h.other)) != 1 || x.invocations != 0 {
		t.Fatal("requested-action retry did not reuse exact delivery and deadline", first, second)
	}
}

type signOutDuringWork struct {
	base           *executor
	authentication string
	phase          string
	failed         bool
}

func (x *signOutDuringWork) Check(_ context.Context, name string) (provider.Health, error) {
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/synthetic/provider", Authentication: x.authentication, Models: []string{}}, nil
}

func (x *signOutDuringWork) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	_, raw, _ := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	if launch.Phase == x.phase && !x.failed {
		x.failed, x.authentication = true, "credentials-rejected"
		if invocation.Started != nil {
			if err := invocation.Started(123456); err != nil {
				return provider.Reply{}, err
			}
		}
		return provider.Reply{}, domain.Fail(domain.Provider, "private provider failure must not be copied into a message")
	}
	return x.base.Invoke(ctx, invocation)
}

func TestProviderSignOutDuringWorkEscalatesWithoutBypassingRecovery(t *testing.T) {
	for _, phase := range []string{"preflight", "execution"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			base := &executor{}
			engine := newEngine(t, h, base)
			task := readyForEngine(t, h, engine)
			project, err := api.Call[domain.Project](context.Background(), h.founder, "GET", api.Path("projects", task.ProjectID), nil)
			if err != nil {
				t.Fatal(err)
			}
			grant := domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID}
			grant.ExpectedRevision = project.Revision
			if _, err := api.Call[domain.Project](context.Background(), h.founder, "POST", api.Path("projects", project.ID, "delegates"), grant); err != nil {
				t.Fatal(err)
			}
			if phase == "execution" {
				task = h.change(t, h.worker, task.ID, "clarity", func(change *domain.TaskChange) { change.SpecGeneration = task.SpecGeneration })
			}
			task = h.change(t, h.founder, task.ID, "approve", func(change *domain.TaskChange) { change.SpecGeneration = task.SpecGeneration })
			x := &signOutDuringWork{base: base, authentication: "authenticated", phase: phase}
			engine.Executor = x
			for range 4 {
				if _, err := engine.Step(context.Background(), false); err != nil {
					break
				}
			}
			status, err := engine.Status(context.Background())
			if err != nil || !x.failed || len(authenticationInbox(t, h.founder)) != 1 || len(authenticationInbox(t, h.other)) != 1 {
				t.Fatal("unexpected sign-out was not escalated to both authorities", status, err)
			}
			request := helpRequestForTask(t, status, task.ID)
			if request.State != "requested" || request.Provider != "codex" {
				t.Fatal("sign-out escalation lost the requested provider/task", request)
			}
			for _, message := range authenticationInbox(t, h.founder) {
				if strings.Contains(message.Body, "private provider failure") {
					t.Fatal("raw provider failure entered a shared notice")
				}
			}
			if phase == "preflight" {
				if _, err := engine.Step(context.Background(), false); err == nil || !strings.Contains(err.Error(), "fenced") {
					t.Fatal("sign-in notice bypassed uncertain invocation recovery", err)
				}
			}
		})
	}
}
