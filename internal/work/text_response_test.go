package work

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

func TestPhaseContractRequiresPreparationOutcomesNotReviewed(t *testing.T) {
	for _, phase := range []string{"scope", "acceptance", "preflight"} {
		launch := Launch{Phase: phase}
		for _, outcome := range []string{"clear", "blocked", "paused"} {
			if err := validateTextPhase(launch, provider.Reply{Outcome: outcome}); err != nil {
				t.Fatal(phase, outcome, err)
			}
		}
		for _, outcome := range []string{"reviewed", "completed", "responded", "failed"} {
			if err := validateTextPhase(launch, provider.Reply{Outcome: outcome}); err == nil || domain.AsFault(err).Code != domain.Provider {
				t.Fatal("wrong phase response accepted", phase, outcome, err)
			}
		}
		data, err := json.Marshal(launch)
		if err != nil {
			t.Fatal(err)
		}
		prompt, err := invocationPrompt(data)
		if err != nil || !strings.Contains(prompt, "Current phase: "+phase) || !strings.Contains(prompt, "MUST be exactly one of: clear, blocked, paused") || !strings.Contains(prompt, "Do NOT return reviewed or completed") {
			t.Fatal("phase instructions were ambiguous", phase, err)
		}
	}
	for _, phase := range []string{"onboarding", "message", "attention", "research"} {
		valid := map[string]string{"onboarding": "reviewed", "message": "responded", "attention": "responded", "research": "researched"}[phase]
		if err := validateTextPhase(Launch{Phase: phase}, provider.Reply{Outcome: valid}); err != nil {
			t.Fatal(phase, err)
		}
	}
}

func TestRejectedTextResponseRetainsEvidenceFencesOnlyActionAndAllowsExactRetry(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	launch := Launch{Phase: "scope", Task: &f.task, Project: &f.project, ActionKey: "explicit:original"}
	contextData, _ := json.Marshal(launch)
	reply := provider.Reply{Outcome: "reviewed", Summary: "PRIVATE_REJECTED_PROVIDER_SUMMARY"}
	replyData, _ := json.Marshal(reply)
	journal := local.Journal{ID: "invocation_phase_rejected", Kind: "scope", State: "reply-prepared", Provider: "codex", Context: contextData, Reply: replyData, CreatedAt: f.now, PromptHash: domain.Hash("original prompt")}
	if err := f.state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.applyText(ctx, &journal, launch, reply); err == nil || domain.AsFault(err).Code != domain.Provider {
		t.Fatal("bad phase response was accepted", err)
	}
	stored, err := f.state.Journal(ctx, journal.ID)
	if err != nil || stored.State != "reply-rejected" || string(stored.Reply) != string(replyData) || stored.PromptHash != journal.PromptHash {
		t.Fatal("rejection lost or coerced original evidence", stored, err)
	}
	pending, err := f.state.Journals(ctx, true)
	if err != nil || len(pending) != 0 {
		t.Fatal("known rejected text poisoned the global slot", pending, err)
	}
	blocks, err := f.engine.textResponseBlocks(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].JournalID != journal.ID {
		t.Fatal("rejected action is not observable", blocks, err)
	}
	for range 3 {
		launch.ActionKey = "explicit:fresh-operation-is-not-retry-authority"
		_, err := f.engine.text(WithRequestedAction(ctx, "new-manual-operation"), f.c, launch, true)
		if err == nil || domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal("fresh operation bypassed semantic action fence", err)
		}
	}
	if len(f.messages) != 2 {
		t.Fatal("unchanged polling spammed Founder/delegate help", len(f.messages))
	}
	for _, message := range f.messages {
		if message.Kind != "permission" || message.ResponseRequest || !strings.Contains(message.Body, "work recover --journal "+journal.ID) || strings.Contains(message.Body, reply.Summary) {
			t.Fatal("unsafe or unusable handoff", message)
		}
	}
	f.reopen(t)
	if _, err := f.engine.text(ctx, f.c, launch, false); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("restart discarded rejected action fence", err)
	}
	if err := f.engine.Recover(ctx, journal.ID, false); err == nil {
		t.Fatal("native stopped-process confirmation bypassed")
	}
	if err := f.engine.Recover(ctx, journal.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.textResponseBlocked(ctx, f.c, launch); err != nil {
		t.Fatal("exact recovery did not authorize a fresh review", err)
	}
	stored, err = f.state.Journal(ctx, journal.ID)
	if err != nil || stored.State != "abandoned" || string(stored.Reply) != string(replyData) {
		t.Fatal("retry erased rejected reply", stored, err)
	}
	if err := f.engine.Recover(ctx, journal.ID, true); err == nil {
		t.Fatal("old acknowledgement could reset another retry")
	}
}

func TestTextResponseSemanticFenceChangesOnlyWithRealInputContext(t *testing.T) {
	launch := Launch{Phase: "scope", Task: &domain.Task{ID: "task_exact", SpecGeneration: 1, DiscussionGeneration: 2}, Project: &domain.Project{Epoch: 1}}
	first := textActionHash(launch)
	launch.ActionKey = "explicit:new"
	if textActionHash(launch) != first {
		t.Fatal("operation ID reset semantic response fence")
	}
	copyTask := *launch.Task
	copyTask.SpecGeneration++
	launch.Task = &copyTask
	if textActionHash(launch) == first {
		t.Fatal("new specification was hidden by old response fence")
	}
	launch.Phase = "acceptance"
	if textActionHash(launch) == first {
		t.Fatal("different preparation phase reused rejection")
	}
}

func TestRejectedApplyingReplyStaysFencedRatherThanDiscardingPossibleEffects(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	launch := Launch{Phase: "scope", Task: &f.task, Project: &f.project}
	contextData, _ := json.Marshal(launch)
	reply := provider.Reply{Outcome: "reviewed", Summary: "fixture rejected review"}
	replyData, _ := json.Marshal(reply)
	journal := local.Journal{ID: "invocation_applying", Kind: "scope", State: "applying", Context: contextData, Reply: replyData, CreatedAt: f.now}
	if err := f.state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.applyText(ctx, &journal, launch, reply); err == nil {
		t.Fatal("invalid applying reply accepted")
	}
	pending, err := f.state.Journals(ctx, true)
	if err != nil || len(pending) != 1 || pending[0].State != "applying" {
		t.Fatal("possible applied effects were discarded", pending, err)
	}
}
