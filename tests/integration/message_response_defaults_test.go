//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func TestDefaultRequestedMessageUsesAIOnlyInAIMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        string
		optOut      bool
		wantReplies int
	}{
		{"ai-default", "ai", false, 1},
		{"ai-explicit-opt-out", "ai", true, 0},
		{"manual-default", "manual", false, 0},
		{"manual-explicit-opt-out", "manual", true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			x := &executor{}
			engine := newEngine(t, h, x)
			skipHealthOnboarding(t, engine)
			if test.mode == "manual" {
				if _, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "mode"),
					domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("mode"), ExpectedRevision: h.workerAgent.Revision},
						Mode: "manual", Reason: "Founder pauses this isolated message fixture"}); err != nil {
					t.Fatal(err)
				}

			}
			input := map[string]any{
				"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID,
				"origin": "human", "kind": "direct", "body": "A message expects a response unless explicitly disabled.",
			}
			if test.optOut {
				input["response_requested"] = false
			}
			sent, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", input)
			if err != nil || sent.ResponseRequest == test.optOut {
				t.Fatal("omitted default or explicit opt-out was lost at the API boundary", sent, err)
			}
			for range 3 {
				status, err := engine.Step(ctx, false)
				if err != nil || status.Mode != test.mode {
					t.Fatal("automatic message step failed or changed mode", status, err)
				}
			}
			if x.count("message") != test.wantReplies {
				t.Fatal("AI response count differs from mode/opt-out contract", x.count("message"))
			}
			thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", sent.ID, "thread"), nil)
			if err != nil || len(thread) != 1+test.wantReplies {
				t.Fatal("message thread did not contain exactly the expected AI reply", thread, err)
			}
			for _, message := range thread {
				if message.ID == sent.ID {
					wantState := "pending"
					if test.wantReplies != 0 {
						wantState = "handled"
					}
					if message.State != wantState || message.ReadAt != 0 {
						t.Fatal("automatic response changed explicit reading or message disposition", message)
					}
					continue
				}
				if message.Origin != "ai" || message.Body != "exact context reviewed" || message.ReplyToID != sent.ID ||
					message.RecipientID != h.founderAgent.ID || !message.ResponseRequest {
					t.Fatal("response was not an AI-generated threaded reply that permits continuation", message)
				}
			}
		})
	}
}

func TestConsecutiveThreadRepliesKeepRecipientAndRequireIndependentResponses(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &executor{}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	root, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Origin: "human", Kind: "direct",
		Body: "Start a private conversation without requesting a response.", ResponseRequest: false})
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]bool{}
	parent := root.ID
	for _, body := range []string{"First reply needs an answer.", "Second consecutive reply needs its own answer.", "No answer needed."} {
		input := map[string]any{"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID,
			"origin": "human", "kind": "direct", "body": body, "reply_to_id": parent}
		if len(sources) == 2 {
			input["response_requested"] = false
		}
		reply, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", input)
		if err != nil || reply.RecipientID != h.workerAgent.ID || reply.SenderID != h.founderAgent.ID ||
			reply.ThreadID != root.ID || reply.ReplyToID != parent || reply.ResponseRequest != (len(sources) < 2) {
			t.Fatal("a consecutive reply reversed its parties, thread or response requirement", reply, err)
		}
		if reply.ResponseRequest {
			sources[reply.ID] = false
		}
		parent = reply.ID
	}
	for range 4 {
		if _, err := engine.Step(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", root.ID, "thread"), nil)
	if err != nil || len(thread) != 6 || x.count("message") != 2 {
		t.Fatal("consecutive replies were collapsed, repeated or ignored the explicit opt-out", thread, err)
	}
	for _, message := range thread {
		if message.SenderID == h.workerAgent.ID {
			if answered, expected := sources[message.ReplyToID]; !expected || answered ||
				message.RecipientID != h.founderAgent.ID || message.ThreadID != root.ID {
				t.Fatal("a response targeted itself or the wrong conversational turn", message)
			}
			sources[message.ReplyToID] = true
		}
	}
	for id, answered := range sources {
		if !answered {
			t.Fatal("a requested consecutive reply received no response", id)
		}
	}
}
