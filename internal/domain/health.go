package domain

const (
	WorkerHealthVersion              = 1
	HealthHeartbeatSeconds     int64 = 90
	HealthReportSeconds        int64 = 90
	HealthDeadlineGraceSeconds int64 = 30
	HealthReplyWaitSeconds     int64 = 120
)

// WorkerHealth is an observation, never work, mode, retry or recovery authority.
type WorkerHealth struct {
	Version                  int             `json:"version"`
	Sequence                 int64           `json:"sequence"`
	ObservedAt               int64           `json:"observed_at"`
	Mode                     string          `json:"mode"`
	Provider                 string          `json:"provider"`
	PreferredProvider        string          `json:"preferred_provider"`
	Model                    string          `json:"model,omitempty"`
	ProviderVersion          string          `json:"provider_version,omitempty"`
	Authentication           string          `json:"authentication"`
	AuthenticationObservedAt int64           `json:"authentication_observed_at,omitempty"`
	Phase                    string          `json:"phase"`
	Activity                 string          `json:"activity,omitempty"`
	PhaseStartedAt           int64           `json:"phase_started_at"`
	DeadlineAt               int64           `json:"deadline_at,omitempty"`
	LastProgressAt           int64           `json:"last_progress_at,omitempty"`
	StepStartedAt            int64           `json:"step_started_at,omitempty"`
	StepFinishedAt           int64           `json:"step_finished_at,omitempty"`
	ActionKind               string          `json:"action_kind,omitempty"`
	ActionID                 string          `json:"action_id,omitempty"`
	TaskID                   string          `json:"task_id,omitempty"`
	JournalID                string          `json:"journal_id,omitempty"`
	LastActionKind           string          `json:"last_action_kind,omitempty"`
	LastActionID             string          `json:"last_action_id,omitempty"`
	LastActionAt             int64           `json:"last_action_at,omitempty"`
	PendingReports           int             `json:"pending_reports"`
	Blocker                  string          `json:"blocker,omitempty"`
	BlockedSinceAt           int64           `json:"blocked_since_at,omitempty"`
	Problems                 []HealthProblem `json:"problems"`
}

type HealthProblem struct {
	Component   string `json:"component"`
	Code        Code   `json:"code"`
	FirstSeenAt int64  `json:"first_seen_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
	ResolvedAt  int64  `json:"resolved_at,omitempty"`
}

type HeartbeatRequest struct {
	Command
	Health *WorkerHealth `json:"health,omitempty"`
}

type HealthIncident struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Since      int64  `json:"since"`
	ResolvedAt int64  `json:"resolved_at,omitempty"`
	Reason     string `json:"reason"`
	NextAction string `json:"next_action"`
}

type AgentDiagnostics struct {
	AgentID        string           `json:"agent_id"`
	Name           string           `json:"name"`
	Mode           string           `json:"mode"`
	State          string           `json:"state"`
	LastSeen       int64            `json:"last_seen"`
	ReceivedAt     int64            `json:"received_at,omitempty"`
	ObservedAt     int64            `json:"observed_at"`
	Stale          bool             `json:"stale"`
	Health         *WorkerHealth    `json:"health,omitempty"`
	PendingReplies int              `json:"pending_replies"`
	OldestReplyAt  int64            `json:"oldest_reply_at,omitempty"`
	Reason         string           `json:"reason"`
	NextAction     string           `json:"next_action"`
	Incident       *HealthIncident  `json:"incident,omitempty"`
	Status         *AgentTextStatus `json:"status,omitempty"`
}

type MessageDiagnostics struct {
	MessageID         string `json:"message_id"`
	RecipientID       string `json:"recipient_id"`
	ReadAt            int64  `json:"read_at,omitempty"`
	ResponseRequested bool   `json:"response_requested"`
	State             string `json:"state"`
	Stage             string `json:"stage"`
	ObservedAt        int64  `json:"observed_at"`
	HealthReceivedAt  int64  `json:"health_received_at,omitempty"`
	Stale             bool   `json:"stale"`
	Reason            string `json:"reason"`
	NextAction        string `json:"next_action"`
}
