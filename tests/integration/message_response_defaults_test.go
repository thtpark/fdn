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
