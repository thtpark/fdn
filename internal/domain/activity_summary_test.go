package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestActivitySummariesAreConciseAndContainNoPrivateContext(t *testing.T) {
	for _, item := range []struct{ phase, activity, blocker, text, state string }{
		{"invoking", "scope", "", "Reviewing task scope", "working"},
		{"invoking", "acceptance", "", "Reviewing acceptance criteria", "working"},
		{"invoking", "execution", "", "Working on an approved task", "working"},
		{"invoking", "verification", "", "Verifying completed work", "working"},
		{"invoking", "message", "", "Replying to a message", "working"},
		{"blocked", "clarification", "work-prerequisite", "Waiting for clarification", "blocked"},
		{"blocked", "scope", "approval-required", "Waiting for task approval", "blocked"},
		{"blocked", "scope", "authentication-required", "Waiting for provider sign-in", "blocked"},
		{"blocked", "message", "session-fence", "Waiting for session recovery", "blocked"},
		{"blocked", "scope", "invocation-uncertain", "Waiting for work recovery", "blocked"},
		{"idle", "idle", "", "Idle; no eligible work", "idle"},
		{"paused", "paused", "founder-paused", "Paused by Founder", "paused"},
	} {
		h := WorkerHealth{Mode: "ai", Phase: item.phase, Activity: item.activity, Blocker: item.blocker, ActionKind: "task", ActionID: "PRIVATE_ACTION_ID", TaskID: "PRIVATE_TASK_ID", JournalID: "PRIVATE_JOURNAL_ID", Model: "/PRIVATE_MODEL_PATH"}
		summary := SummarizeActivity(h)
		if summary.Text != item.text || summary.State != item.state || !ValidHash(summary.ContextKey) || strings.Contains(summary.Text, "PRIVATE_") || utf8.RuneCountInString(summary.Text) > AgentStatusMaxRunes {
			t.Fatal("unsafe or imprecise public summary", item, summary)
		}
	}
}

func TestBookkeepingDoesNotCreateStatusChurnOrRecoveryClaims(t *testing.T) {
	for _, phase := range []string{"checking", "reconciling", "preparing", "provider-check"} {
		if status := SummarizeActivity(WorkerHealth{Mode: "ai", Phase: phase, Activity: "scope"}); status.Text != "" {
			t.Fatal("polling became public activity", phase, status)
		}
	}
	idle := ActivitySummary{Text: "Idle; no eligible work", State: "idle"}
	work := ActivitySummary{Text: "Reviewing task scope", State: "working"}
	blocked := ActivitySummary{Text: "Waiting for clarification", State: "blocked"}
	if ImportantStatusChange(idle, work) || ImportantStatusChange(blocked, blocked) || ImportantStatusChange(blocked, ActivitySummary{}) {
		t.Fatal("normal/unchanged/bookkeeping status bypassed publication interval")
	}
	if !ImportantStatusChange(work, blocked) || !ImportantStatusChange(blocked, work) {
		t.Fatal("meaningful blocker/recovery did not request prompt publication")
	}
	legacy := SummarizeActivity(WorkerHealth{Mode: "ai", Phase: "invoking", ActionKind: "message", ActionID: "message_private"})
	if legacy.Text != "Replying to a message" {
		t.Fatal("old health lost safe fallback summary", legacy)
	}
}
