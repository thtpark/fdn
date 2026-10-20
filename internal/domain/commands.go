package domain

type EnrollRequest struct {
	Command
	InvitationToken string `json:"invitation_token"`
	Name            string `json:"name"`
	CredentialHash  string `json:"credential_hash"`
}

type InvitationRequest struct {
	Command
	TokenHash string `json:"token_hash"`
	ExpiresAt int64  `json:"expires_at"`
}

type AgentRequest struct {
	Command
	Name string `json:"name,omitempty"`
}

type ProjectRequest struct {
	Command
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ProjectChange struct {
	Command
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Archived    *bool   `json:"archived,omitempty"`
	Reason      string  `json:"reason"`
}

type GrantRequest struct {
	Command
	AgentID string `json:"agent_id"`
	Revoke  bool   `json:"revoke"`
}

type TaskRequest struct {
	Command
	ProjectID          string `json:"project_id"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	ParentID           string `json:"parent_id,omitempty"`
	Repetitive         bool   `json:"repetitive"`
	Priority           int    `json:"priority"`
	Importance         int    `json:"importance"`
}

type TaskChange struct {
	Command
	Kind                string          `json:"kind"`
	Reason              string          `json:"reason"`
	Title               *string         `json:"title,omitempty"`
	Description         *string         `json:"description,omitempty"`
	AcceptanceCriteria  *string         `json:"acceptance_criteria,omitempty"`
	Priority            *int            `json:"priority,omitempty"`
	Importance          *int            `json:"importance,omitempty"`
	Status              Status          `json:"status,omitempty"`
	AgentID             string          `json:"agent_id,omitempty"`
	Provider            string          `json:"provider,omitempty"`
	Model               string          `json:"model,omitempty"`
	WorkspaceRef        string          `json:"workspace_ref,omitempty"`
	Policy              *ApprovalPolicy `json:"policy,omitempty"`
	RetryPolicy         *RetryPolicy    `json:"retry_policy,omitempty"`
	SpecGeneration      int64           `json:"spec_generation,omitempty"`
	ResultGeneration    int64           `json:"result_generation,omitempty"`
	Decision            string          `json:"decision,omitempty"`
	RunID               string          `json:"run_id,omitempty"`
	Attempt             int             `json:"attempt,omitempty"`
	HistorySequence     int64           `json:"history_sequence,omitempty"`
	AnswerIDs           []string        `json:"answer_ids,omitempty"`
	Waiting             *Waiting        `json:"waiting,omitempty"`
	StoppedAcknowledged bool            `json:"stopped_acknowledged,omitempty"`
}

type EntryRequest struct {
	Command
	Kind     string `json:"kind"`
	Body     string `json:"body"`
	ParentID string `json:"parent_id,omitempty"`
}

type AttentionRequest struct {
	Command
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}

type MessageRequest struct {
	Command
	RecipientID     string `json:"recipient_id"`
	Body            string `json:"body"`
	Origin          string `json:"origin"`
	Kind            string `json:"kind"`
	Urgent          bool   `json:"urgent"`
	ResponseRequest bool   `json:"response_requested"`
}

type MessageReceipt struct {
	Command
	State string `json:"state"`
}

type SessionRequest struct {
	Command
	TargetID  string `json:"target_id"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id,omitempty"`
	Mode      string `json:"mode"`
	Purpose   string `json:"purpose"`
	Provider  string `json:"provider,omitempty"`
	Reason    string `json:"reason"`
	ExpiresAt int64  `json:"expires_at"`
}

type SessionChange struct {
	Command
	Action string `json:"action"`
}

type ClaimRequest struct {
	Command
	SpecGeneration int64  `json:"spec_generation"`
	TokenHash      string `json:"token_hash"`
	Provider       string `json:"provider"`
	Model          string `json:"model,omitempty"`
}

type ReportRequest struct {
	Command
	RunID          string   `json:"run_id"`
	Token          string   `json:"token"`
	SpecGeneration int64    `json:"spec_generation"`
	Outcome        string   `json:"outcome"`
	Summary        string   `json:"summary"`
	Evidence       []string `json:"evidence"`
	Checkpoint     string   `json:"checkpoint,omitempty"`
	SafeToRetry    bool     `json:"safe_to_retry"`
	Waiting        *Waiting `json:"waiting,omitempty"`
}

type ReportOutcome struct {
	Accepted bool `json:"accepted"`
	Stale    bool `json:"stale"`
	Task     Task `json:"task"`
	Run      Run  `json:"run"`
}
