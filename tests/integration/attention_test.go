//go:build integration

package integration_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type attentionExecutor struct {
	base        executor
	preferred   string
	checks      []string
	invocations []provider.Invocation
}

func (x *attentionExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	x.checks = append(x.checks, name)
	health, err := x.base.Check(ctx, name)
	health.Models = []string{"reply-model"}
	if name != x.preferred {
		health.Authentication = "login-required"
	}
	return health, err
}

func (x *attentionExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	x.invocations = append(x.invocations, invocation)
	return x.base.Invoke(ctx, invocation)
}

func TestMentionReplyUsesRecipientProviderAndModel(t *testing.T) {
	for _, preferred := range []string{"codex", "copilot"} {
		for _, taskProvider := range []string{"codex", "copilot"} {
			for _, onboarded := range []bool{false, true} {
				name := preferred + "/task-" + taskProvider + "/before-onboarding"
				if onboarded {
					name = preferred + "/task-" + taskProvider + "/after-onboarding"
				}
				t.Run(name, func(t *testing.T) {
					h := newHarness(t)
					ctx := context.Background()
					x := &attentionExecutor{preferred: preferred}
					engine := newEngine(t, h, &x.base)
					engine.Executor = x
					engine.Config.Provider, engine.Config.Model = preferred, "reply-model"
					if onboarded {
						skipHealthOnboarding(t, engine)
					}
					task := h.ready(t)
					task = h.change(t, h.founder, task.ID, "assign", func(change *domain.TaskChange) {
						change.AgentID, change.Provider, change.Model = h.otherAgent.ID, taskProvider, "execution-model"
					})
					entry, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{
						Command: command(), Kind: "comment", Body: "Please answer @{" + h.workerAgent.Name + "}."})
					if err != nil || !slices.Equal(entry.Mentions, []string{h.workerAgent.ID}) {
						t.Fatal("mention was not delivered to the intended recipient", entry, err)
					}
					if !onboarded {
						if status, err := engine.Step(ctx, false); err != nil || status.State != "onboarding" {
							t.Fatal("another worker's execution provider blocked recipient onboarding", status, err)
						}
					}
					status, err := engine.Step(ctx, false)
					if err != nil || status.State != "attention" {
						t.Fatal("another worker's provider/model blocked the mention reply", status, err)
					}
					if x.base.count("attention") != 1 || len(status.AuthenticationRequests) != 0 {
						t.Fatal("mention did not produce exactly one reply without unrelated sign-in help", status)
					}
					if status.Health == nil || status.Health.Provider != preferred || status.Health.Model != "reply-model" {
						t.Fatal("mention health did not report the actual reply provider/model", status.Health)
					}
					for _, name := range x.checks {
						if name != preferred {
							t.Fatal("mention checked the task execution provider", x.checks)
						}
					}
					for _, invocation := range x.invocations {
						if invocation.Provider != preferred || invocation.Model != "reply-model" || invocation.Coding || invocation.Workspace != "" {
							t.Fatal("mention did not use the recipient's tool-disabled reply configuration", invocation.Provider, invocation.Model, invocation.Coding)
						}
					}
					entries, err := api.Call[domain.Page[domain.Entry]](ctx, h.founder, "GET", api.Path("tasks", task.ID, "entries"), nil)
					if err != nil || len(entries.Items) != 2 {
						t.Fatal("mention reply was not published", entries, err)
					}
					replies := 0
					for _, reply := range entries.Items {
						if reply.ParentID == entry.ID && reply.AuthorID == h.workerAgent.ID && reply.Body == "exact context reviewed" {
							replies++
						}
					}
					if replies != 1 {
						t.Fatal("mention reply was not attached to its source comment", entries)
					}
					pending, err := api.Call[domain.Page[domain.Attention]](ctx, h.worker, "GET", "/v1/attention", nil)
					if err != nil || len(pending.Items) != 0 {
						t.Fatal("answered mention remained pending", pending, err)
					}
					current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
					if err != nil || current.AssigneeID != h.otherAgent.ID || current.Provider != taskProvider || current.Model != "execution-model" || current.Status != task.Status || current.Readiness != nil || current.ActiveRun != nil {
						t.Fatal("mention response changed execution authority or task configuration", current, err)
					}
					engine = restartAuthenticationEngine(t, engine)
					if status, err := engine.Step(ctx, false); err != nil || status.State != "idle" || x.base.count("attention") != 1 {
						t.Fatal("restart repeated mention inference", status, err)
					}
				})
			}
		}
	}
}

func TestMentionAuthenticationBeforeOnboardingUsesRecipientProvider(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	project := requestedHelpProject(t, h)
	task := requestedHelpTask(t, h, project.ID, "mention authentication")
	task = h.change(t, h.founder, task.ID, "assign", func(change *domain.TaskChange) {
		change.AgentID, change.Provider = h.otherAgent.ID, "copilot"
	})
	entry, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"), domain.EntryRequest{
		Command: command(), Kind: "comment", Body: "Please answer @{" + h.workerAgent.Name + "}."})
	if err != nil {
		t.Fatal(err)
	}

	var requestID string
	for range 2 {
		status, err := engine.Step(ctx, false)
		if err == nil || status.State != "blocked" || x.invocations != 0 || len(status.AuthenticationRequests) != 1 {
			t.Fatal("mention did not retain one recipient-provider sign-in request before onboarding", status, err)
		}
		request := status.AuthenticationRequests[0]
		if request.Provider != engine.Config.Provider || request.TaskID != task.ID || request.ProjectID != project.ID ||
			!strings.Contains(request.ActionKey, entry.ID) || request.State != "requested" || len(request.Deliveries) != 2 {
			t.Fatal("mention help lost recipient provider, source context or current authorities", request)
		}
		if requestID != "" && request.ID != requestID {
			t.Fatal("restart duplicated mention sign-in help", request)
		}
		requestID = request.ID
		engine = restartAuthenticationEngine(t, engine)
	}
	for _, name := range x.checks {
		if name != engine.Config.Provider {
			t.Fatal("mention requested sign-in to the execution provider", x.checks)
		}
	}
	if len(authenticationInbox(t, h.founder)) != 1 || len(authenticationInbox(t, h.other)) != 1 {
		t.Fatal("mention help was not delivered exactly once to Founder and the project delegate")
	}
}

func TestNestedCommentRepliesRequireEveryMentionedAgentToRespond(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.ready(t)
	task = h.change(t, h.founder, task.ID, "assign", func(change *domain.TaskChange) {
		change.AgentID, change.Provider, change.Model = h.founderAgent.ID, "codex", "execution-model"
	})
	root, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"),
		domain.EntryRequest{Command: command(), Kind: "comment", Body: "A retained comment thread."})
	if err != nil {
		t.Fatal(err)
	}
	executors := []*attentionExecutor{{preferred: "codex"}, {preferred: "copilot"}}
	engines := make([]*work.Engine, len(executors))
	for index, x := range executors {
		engine := newEngine(t, h, &x.base)
		if index == 1 {
			engine.Client = h.other
		}
		engine.Executor = x
		engine.Config.Provider, engine.Config.Model = x.preferred, "reply-model"
		skipHealthOnboarding(t, engine)
		engines[index] = engine
	}
	parent := root.ID
	for round := range 2 {
		source, err := api.Call[domain.Entry](ctx, h.founder, "POST", api.Path("tasks", task.ID, "entries"),
			domain.EntryRequest{Command: command(), Kind: "comment", ParentID: parent,
				Body: "Both @{" + h.workerAgent.Name + "} and @{" + h.otherAgent.Name + "} must respond. @{" + h.workerAgent.ID + "} @{" + h.founderAgent.Name + "}"})
		if err != nil || len(source.Mentions) != 2 || !slices.Contains(source.Mentions, h.workerAgent.ID) ||
			!slices.Contains(source.Mentions, h.otherAgent.ID) {
			t.Fatal("nested reply did not target both distinct agents exactly once", source, err)
		}
		for index, engine := range engines {
			status, err := engine.Step(ctx, false)
			if err != nil || status.State != "attention" || len(status.AuthenticationRequests) != 0 ||
				executors[index].base.count("attention") != round+1 {
				t.Fatal("a mentioned recipient did not respond using its ready provider", status, err)
			}
			pending, err := api.Call[domain.Page[domain.Attention]](ctx, engine.Client, "GET", "/v1/attention", nil)
			if err != nil || len(pending.Items) != 0 {
				t.Fatal("a reply did not acknowledge its recipient's exact attention", pending, err)
			}
			engines[index] = restartAuthenticationEngine(t, engine)
		}
		entries, err := api.Call[domain.Page[domain.Entry]](ctx, h.founder, "GET", api.Path("tasks", task.ID, "entries"), nil)
		if err != nil {
			t.Fatal(err)
		}
		authors := []string{}
		for _, entry := range entries.Items {
			if entry.ParentID == source.ID {
				authors = append(authors, entry.AuthorID)
			}
		}
		slices.Sort(authors)
		if !slices.Equal(authors, source.Mentions) {
			t.Fatal("nested comment did not receive exactly one response from every mentioned agent", authors, source.Mentions)
		}
		parent = source.ID
	}
	for index, engine := range engines {
		if status, err := engine.Step(ctx, false); err != nil || status.State != "idle" ||
			executors[index].base.count("attention") != 2 {
			t.Fatal("polling or restart duplicated a nested comment reply", status, err)
		}
	}
}
