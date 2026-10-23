package domain

import (
	"fmt"
	"slices"
)

const (
	AgentStatusVersion        = 1
	AgentStatusInterval int64 = 60
	AgentStatusMaxRunes       = 160
)

type AgentTextStatus struct {
	Text       string `json:"text"`
	Source     string `json:"source"`
	State      string `json:"state"`
	UpdatedAt  int64  `json:"updated_at"`
	ObservedAt int64  `json:"observed_at"`
	Stale      bool   `json:"stale"`
}

type AgentStatusRequest struct {
	Command
	Text *string `json:"text"`
}

type AgentStatusReceipt struct {
	AgentID      string           `json:"agent_id"`
	Status       *AgentTextStatus `json:"status,omitempty"`
	Pending      bool             `json:"pending"`
	NextUpdateAt int64            `json:"next_update_at,omitempty"`
}

// ActivitySummary contains only allowlisted non-secret controller wording.
// ContextKey scopes an optional authored note without publishing private IDs.
type ActivitySummary struct {
	Text       string `json:"text"`
	State      string `json:"state"`
	ContextKey string `json:"context_key"`
}

func SummarizeActivity(h WorkerHealth) ActivitySummary {
	status := ActivitySummary{State: "working"}
	switch {
	case h.Mode == "manual" || h.Phase == "paused" || h.Blocker == "founder-paused":
		status.Text, status.State = "Paused by Founder", "paused"
	case h.Blocker != "":
		status.State = "blocked"
		switch h.Blocker {
		case "authentication-required":
			status.Text = "Waiting for provider sign-in"
		case "authentication-unknown":
			status.Text = "Waiting for provider authentication checks"
		case "session-fence":
			status.Text = "Waiting for session recovery"
		case "session-active":
			status.Text, status.State = "Using an assistance session", "working"
		case "invocation-uncertain", "unreconciled-claim":
			status.Text = "Waiting for work recovery"
		case "workspace-permission":
			status.Text = "Waiting for workspace permission"
		case "provider-permission":
			status.Text = "Waiting for provider permission"
		case "provider-unavailable", "provider-model-unavailable":
			status.Text = "Waiting for an available provider"
		case "approval-required":
			status.Text = "Waiting for task approval"
			if h.Activity == "retry" {
				status.Text = "Waiting for retry approval"
			}
		case "retry-wait":
			status.Text = "Waiting for retry backoff"
		case "report-pending":
			status.Text = "Waiting to deliver work results"
		case "maintenance":
			status.Text = "Waiting for maintenance to finish"
		case "configuration":
			status.Text = "Waiting for configuration repair"
		case "transport-unavailable", "identity-unavailable":
			status.Text = "Waiting for the Foundation connection"
		case "slot-occupied":
			status.Text = "Waiting for the shared work slot"
		default:
			switch h.Activity {
			case "clarification":
				status.Text = "Waiting for clarification"
			case "approval":
				status.Text = "Waiting for task approval"
			case "retry":
				status.Text = "Waiting for retry approval"
			default:
				status.Text = "Waiting for a work prerequisite"
			}
		}
	case h.PendingReports > 0 || h.Phase == "reporting":
		status.Text = "Delivering work results"
	case h.Phase == "applying":
		status.Text = "Saving work results"
	case h.Phase == "session":
		status.Text = "Using an assistance session"
	case h.Phase == "invoking":
		switch h.Activity {
		case "onboarding":
			status.Text = "Reviewing computer capabilities"
		case "scope":
			status.Text = "Reviewing task scope"
		case "acceptance":
			status.Text = "Reviewing acceptance criteria"
		case "preflight":
			status.Text = "Checking task readiness"
		case "execution":
			status.Text = "Working on an approved task"
		case "verification":
			status.Text = "Verifying completed work"
		case "message":
			status.Text = "Replying to a message"
		case "attention":
			status.Text = "Reviewing a mention"
		case "research":
			status.Text = "Researching a queued task"
		default:
			switch h.ActionKind {
			case "message":
				status.Text = "Replying to a message"
			case "attention":
				status.Text = "Reviewing a mention"
			case "onboarding":
				status.Text = "Reviewing computer capabilities"
			case "research":
				status.Text = "Researching a queued task"
			default:
				status.Text = "Working on an assigned task"
			}
		}
	case h.Phase == "idle":
		status.Text, status.State = "Idle; no eligible work", "idle"
	case h.Phase == "blocked":
		status.Text, status.State = "Waiting for a work prerequisite", "blocked"
	case h.Phase == "unknown":
		status.Text, status.State = "Starting up", "unknown"
	default:
		// Checking/reconciling/preparing polls are not meaningful activity changes.
		return ActivitySummary{}
	}
	status.ContextKey = Hash(fmt.Sprintf("activity-v1:%s:%s:%s:%s:%s", h.Activity, h.ActionKind, h.ActionID, h.Blocker, status.State))
	return status
}

func ImportantStatusChange(before, after ActivitySummary) bool {
	if after.Text == "" || (before.Text == after.Text && before.State == after.State) {
		return false
	}
	return slices.Contains([]string{"blocked", "paused"}, after.State) || slices.Contains([]string{"blocked", "paused"}, before.State)
}
