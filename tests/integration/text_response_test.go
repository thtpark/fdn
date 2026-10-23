//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type textPhaseExecutor struct {
	mu         sync.Mutex
	badScope   bool
	badMessage string
	phases     map[string]int
}

func (x *textPhaseExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	return provider.Health{Provider: name, Version: "1.2.3", Authentication: "authenticated"}, nil
}
func (x *textPhaseExecutor) Invoke(_ context.Context, inv provider.Invocation) (provider.Reply, error) {
	_, data, _ := strings.Cut(inv.Prompt, "\nFDN_CONTEXT_JSON\n")
	var launch work.Launch
	if err := json.Unmarshal([]byte(data), &launch); err != nil {
		return provider.Reply{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.phases == nil {
		x.phases = map[string]int{}
	}
	x.phases[launch.Phase]++
	if inv.Coding {
		return provider.Reply{}, domain.Fail(domain.Provider, "test forbids task execution")
	}
	outcome := "clear"
	switch launch.Phase {
	case "scope":
		if !strings.Contains(inv.Prompt, "MUST be exactly one of: clear, blocked, paused") {
			return provider.Reply{}, domain.Fail(domain.Provider, "phase prompt missing")
		}
		if x.badScope {
			outcome = "reviewed"
		}
	case "message":
		outcome = "responded"
		if launch.Message != nil && launch.Message.ID == x.badMessage {
			outcome = "reviewed"
		}
	case "onboarding":
		outcome = "reviewed"
	case "research":
		outcome = "researched"
	}
	return provider.Reply{Outcome: outcome, Summary: "Fixture review or requested response", Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}}, nil
}
func (x *textPhaseExecutor) count(phase string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.phases[phase]
}

func TestRejectedScopeReplyDoesNotPoisonInboxOrRepeatInference(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "new-provider-reply"
		if cached {
			name = "cached-deployment-reply"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			project := requestedHelpProject(t, h)
			task := requestedHelpTask(t, h, project.ID, "Scope contract fixture")
			x := &textPhaseExecutor{badScope: true}
			e := newEngine(t, h, &executor{})
			e.Executor = x
			skipHealthOnboarding(t, e)
			message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Origin: "human", Kind: "direct", Body: "A separate requested reply must not be swallowed", ResponseRequest: true})
			if err != nil {
				t.Fatal(err)
			}
			if cached {
				launch := work.Launch{Phase: "scope", AgentID: h.workerAgent.ID, Task: &task, Project: &project}
				contextData, _ := json.Marshal(launch)
				replyData, _ := json.Marshal(provider.Reply{Outcome: "reviewed", Summary: "Private legacy response must remain unchanged"})
				if err := e.State.SaveJournal(ctx, local.Journal{ID: "invocation_cached_scope_contract", Kind: "scope", State: "reply-prepared", Provider: "codex", Context: contextData, Reply: replyData, PromptHash: domain.Hash("old prompt")}); err != nil {
					t.Fatal(err)
				}
			}
			status, err := e.Step(ctx, false)
			if err == nil || domain.AsFault(err).Code != domain.Provider || len(status.TextResponseBlocks) != 1 {
				t.Fatal("invalid reviewed outcome not quarantined", status, err)
			}
			id := status.TextResponseBlocks[0].JournalID
			bad, err := e.State.Journal(ctx, id)
			if err != nil || bad.State != "reply-rejected" {
				t.Fatal("rejected reply owns global pending journal", bad, err)
			}
			var original provider.Reply
			if err := json.Unmarshal(bad.Reply, &original); err != nil || original.Outcome != "reviewed" {
				t.Fatal("bad outcome was coerced or erased", original, err)
			}
			e = restartAuthenticationEngine(t, e)
			status, err = e.Step(work.WithRequestedAction(ctx, "new-operation-is-not-retry-authority"), true)
			if err != nil || status.State != "message" || x.count("message") != 1 {
				t.Fatal("known bad scope still swallowed unrelated reply", status, err)
			}
			source, err := api.Call[domain.Message](ctx, h.worker, "GET", api.Path("messages", message.ID), nil)
			if err != nil || source.State != "handled" {
				t.Fatal("unrelated response not delivered", source, err)
			}
			for range 2 {
				if _, err := e.Step(ctx, false); err != nil {
					t.Fatal(err)
				}
			}
			wantScope := 1
			if cached {
				wantScope = 0
			}
			if x.count("scope") != wantScope || x.count("message") != 1 {
				t.Fatal("poll/restart/new operation repeated rejected inference", x.phases)
			}
			current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
			if err != nil || current.Readiness != nil || current.ActiveRun != nil {
				t.Fatal("rejection invented approval or execution", current, err)
			}
			if err := e.Recover(ctx, id, false); err == nil {
				t.Fatal("retry skipped native stopped-process acknowledgement")
			}
			if err := e.Recover(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			x.mu.Lock()
			x.badScope = false
			x.mu.Unlock()
			status, err = e.Step(ctx, false)
			if err != nil || x.count("scope") != wantScope+1 {
				t.Fatal("exact recovery did not permit one corrected review", status, err)
			}
			retained, err := e.State.Journal(ctx, id)
			if err != nil || retained.State != "abandoned" || string(retained.Reply) != string(bad.Reply) {
				t.Fatal("explicit retry erased old response", retained, err)
			}
		})
	}
}

func TestRejectedMessageDoesNotStarveAnotherRequestedReply(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &textPhaseExecutor{}
	e := newEngine(t, h, &executor{})
	e.Executor = x
	skipHealthOnboarding(t, e)
	messages := []domain.Message{}
	for range 2 {
		m, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Origin: "human", Kind: "direct", Body: "fixture requested response", ResponseRequest: true})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if messages[0].ID > messages[1].ID {
		messages[0], messages[1] = messages[1], messages[0]
	}
	x.badMessage = messages[0].ID
	if _, err := e.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Provider {
		t.Fatal("first wrong message outcome accepted", err)
	}
	if status, err := e.Step(ctx, false); err != nil || status.State != "message" || x.count("message") != 2 {
		t.Fatal("rejected message starved another reply", status, err, x.phases)
	}
	for index, message := range messages {
		m, err := api.Call[domain.Message](ctx, h.worker, "GET", api.Path("messages", message.ID), nil)
		want := "pending"
		if index == 1 {
			want = "handled"
		}
		if err != nil || m.State != want {
			t.Fatal("rejection changed another message disposition", m, err)
		}
	}
}
