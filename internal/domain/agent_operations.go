package domain

const (
	AgentOperationVersion       = 1
	AgentOperationTTL     int64 = 900
)

// AgentOperation is a Founder-authorized control, not a model prompt or task approval.
type AgentOperation struct {
	Version             int                   `json:"version"`
	ID                  string                `json:"id"`
	Revision            int64                 `json:"revision"`
	Kind                string                `json:"kind"`
	RequesterID         string                `json:"requester_id"`
	TargetID            string                `json:"target_id"`
	RequesterGeneration int64                 `json:"requester_generation"`
	TargetGeneration    int64                 `json:"target_generation"`
	State               string                `json:"state"`
	Reason              string                `json:"reason"`
	SessionID           string                `json:"session_id,omitempty"`
	ExpectedFence       string                `json:"expected_fence,omitempty"`
	CreatedAt           int64                 `json:"created_at"`
	ExpiresAt           int64                 `json:"expires_at"`
	StartedAt           int64                 `json:"started_at,omitempty"`
	FinishedAt          int64                 `json:"finished_at,omitempty"`
	ServerObservedAt    int64                 `json:"server_observed_at"`
	Result              *AgentOperationResult `json:"result,omitempty"`
}

type DiagnosticRefreshRequest struct {
	Command
	Reason string `json:"reason"`
}

type SessionRecoveryRequest struct {
	Command
	SessionID     string `json:"session_id"`
	ExpectedFence string `json:"expected_fence"`
	Reason        string `json:"reason"`
}

type AgentOperationReport struct {
	Command
	State  string                `json:"state"`
	Result *AgentOperationResult `json:"result,omitempty"`
}

type AgentOperationResult struct {
	Code                string           `json:"code"`
	CapturedAt          int64            `json:"captured_at"`
	ModeAtCheck         string           `json:"mode_at_check"`
	AutomationMayResume bool             `json:"automation_may_resume"`
	Observation         *AgentInspection `json:"observation,omitempty"`
}

type AgentInspection struct {
	ObservedAt   int64                    `json:"observed_at"`
	Health       *WorkerHealth            `json:"health,omitempty"`
	Provider     *ProviderObservation     `json:"provider,omitempty"`
	SessionFence *SessionFenceObservation `json:"session_fence,omitempty"`
	WorkSlot     string                   `json:"work_slot"`
}

type ProviderObservation struct {
	Provider       string `json:"provider"`
	Version        string `json:"version,omitempty"`
	Authentication string `json:"authentication"`
	ObservedAt     int64  `json:"observed_at"`
	ProblemCode    Code   `json:"problem_code,omitempty"`
}

type SessionFenceObservation struct {
	SessionID     string `json:"session_id"`
	Fingerprint   string `json:"fingerprint"`
	CleanupProven bool   `json:"cleanup_proven"`
	SessionState  string `json:"session_state"`
	RecoveryState string `json:"recovery_state"`
}
