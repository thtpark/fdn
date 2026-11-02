package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestHelpSessionOpenRequiresExactLiveMessageAndRetainedGrant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	help, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, domain.AssistanceHelpRequest{
		Command: op(), Mode: "tmux", Purpose: "authentication", Provider: "codex",
		Reason: "Human-only sign-in fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := help.Messages[0]
	if message.Help == nil || message.Help.Provider != "codex" || message.ServerObservedAt != f.now.Unix() {
		t.Fatal("message omitted typed assistance or fresh server time", message.Help, message.ServerObservedAt)
	}
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{
		Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "tmux",
		Purpose: "authentication", Provider: "codex", Reason: "Explicit reviewed fixture scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SessionRequest{Command: op(), TargetID: f.worker.ID, GrantID: grant.ID,
		Mode: "tmux", Purpose: "authentication", Provider: "codex", HelpMessageID: message.ID,
		Reason: "Explicit live help action", ExpiresAt: f.now.Unix() + 300}
	if _, err := f.s.RequestSession(ctx, f.founder.ID, request); err != nil {
		t.Fatal("live exact help could not open its retained grant", err)
	}
	for _, change := range []func(*domain.SessionRequest){
		func(r *domain.SessionRequest) { r.Provider = "copilot" },
		func(r *domain.SessionRequest) { r.HelpMessageID = "message_missing" },
	} {
		changed := request
		changed.Command = op()
		change(&changed)
		if _, err := f.s.RequestSession(ctx, f.founder.ID, changed); err == nil {
			t.Fatal("mismatched help scope was accepted")
		}
	}
	f.now = time.Unix(help.ExpiresAt, 0)
	request.Command = op()
	request.ExpiresAt = f.now.Unix() + 300
	_, err = f.s.RequestSession(ctx, f.founder.ID, request)
	assertCode(t, err, domain.Denied)
}
