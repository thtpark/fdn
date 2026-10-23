package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func healthValidationFixture() WorkerHealth {
	return WorkerHealth{Version: WorkerHealthVersion, Sequence: 1, ObservedAt: 1000, Mode: "ai", Provider: "codex", PreferredProvider: "copilot", ProviderVersion: "1.2.3+fixture", Model: "vendor/model:v2", Authentication: "authenticated", Phase: "idle", PhaseStartedAt: 900}
}

func TestHealthValidationClosedBoundedObservations(t *testing.T) {
	valid := healthValidationFixture()
	if err := ValidateWorkerHealth(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*WorkerHealth){
		"future schema":      func(h *WorkerHealth) { h.Version++ },
		"negative sequence":  func(h *WorkerHealth) { h.Sequence = -1 },
		"zero sequence work": func(h *WorkerHealth) { h.Sequence = 0 },
		"raw phase":          func(h *WorkerHealth) { h.Phase = "private-error" },
		"raw blocker":        func(h *WorkerHealth) { h.Blocker = "private-error" },
		"raw auth":           func(h *WorkerHealth) { h.Authentication = "token=private" },
		"provider":           func(h *WorkerHealth) { h.Provider = "unapproved" },
		"preference":         func(h *WorkerHealth) { h.PreferredProvider = "unapproved" },
		"version path":       func(h *WorkerHealth) { h.ProviderVersion = "/private/bin/provider" },
		"version output":     func(h *WorkerHealth) { h.ProviderVersion = "1.2.3\nprivate-error" },
		"large version":      func(h *WorkerHealth) { h.ProviderVersion = strings.Repeat("v", 129) },
		"model text":         func(h *WorkerHealth) { h.Model = "model with private output" },
		"large model":        func(h *WorkerHealth) { h.Model = strings.Repeat("x", 257) },
		"action reference":   func(h *WorkerHealth) { h.ActionKind, h.ActionID = "message", "../private" },
		"large reference":    func(h *WorkerHealth) { h.JournalID = strings.Repeat("x", 129) },
		"missing action":     func(h *WorkerHealth) { h.ActionKind = "task" },
		"future progress":    func(h *WorkerHealth) { h.LastProgressAt = h.ObservedAt + 1 },
		"negative clock":     func(h *WorkerHealth) { h.BlockedSinceAt = -1 },
		"unbounded deadline": func(h *WorkerHealth) { h.DeadlineAt = h.ObservedAt + 8*86400 },
		"pending budget":     func(h *WorkerHealth) { h.PendingReports = MaxHealthPendingReports + 1 },
		"raw problem component": func(h *WorkerHealth) {
			h.Problems = []HealthProblem{{Component: "private-path", Code: Unavailable, FirstSeenAt: 900, LastSeenAt: 1000}}
		},
		"raw problem code": func(h *WorkerHealth) {
			h.Problems = []HealthProblem{{Component: "watch", Code: "private-error", FirstSeenAt: 900, LastSeenAt: 1000}}
		},
		"problem clock": func(h *WorkerHealth) {
			h.Problems = []HealthProblem{{Component: "watch", Code: Unavailable, FirstSeenAt: 1001, LastSeenAt: 1000}}
		},
		"problem budget": func(h *WorkerHealth) { h.Problems = make([]HealthProblem, MaxHealthProblems+1) },
	} {
		t.Run(name, func(t *testing.T) {
			h := valid
			change(&h)
			if err := ValidateWorkerHealth(h); err == nil || AsFault(err).Code != Invalid {
				t.Fatalf("unsafe health accepted: %+v (%v)", h, err)
			}
		})
	}
	valid.Sequence, valid.Phase = 0, "unknown"
	valid.Problems = []HealthProblem{{Component: "controller", Code: Prerequisite, FirstSeenAt: 900, LastSeenAt: 1000}}
	if err := ValidateWorkerHealth(valid); err != nil {
		t.Fatal("pre-controller diagnostic observation rejected", err)
	}
}

func TestHealthRelativeClocksAndHeartbeatDTOCompatibility(t *testing.T) {
	h := healthValidationFixture()
	h.ObservedAt, h.PhaseStartedAt, h.DeadlineAt = 11000, 10900, 11200
	if got := HealthAge(h.ObservedAt, h.PhaseStartedAt, 1000, 1020); got != 120 {
		t.Fatal("worker/server clock skew changed relative age", got)
	}
	if got := HealthDeadlineRemaining(h, 1000, 1020); got != 180 {
		t.Fatal("deadline was compared against server UTC", got)
	}
	if HealthAge(h.ObservedAt, 0, 1000, 1020) != -1 || HealthAge(11000, 10900, 1000, 990) != 100 {
		t.Fatal("unknown or backwards server clock fabricated an age")
	}
	var req HeartbeatRequest
	if err := json.Unmarshal([]byte(`{"operation_id":"old-heartbeat","expected_revision":2}`), &req); err != nil || req.Health != nil || req.OperationID != "old-heartbeat" || req.ExpectedRevision != 2 {
		t.Fatal("old heartbeat command failed to decode", req, err)
	}
	data, err := json.Marshal(HeartbeatRequest{Command: Command{OperationID: "old-heartbeat"}})
	if err != nil || strings.Contains(string(data), `"health"`) {
		t.Fatal("optional health broke the command-only heartbeat shape", string(data), err)
	}
}
