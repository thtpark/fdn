package domain

const FoundationProjectID = "project_foundation"

type ImprovementPolicy struct {
	Revision        int64    `json:"revision"`
	Enabled         bool     `json:"enabled"`
	ProjectIDs      []string `json:"project_ids"`
	WindowSeconds   int      `json:"window_seconds"`
	SliceSeconds    int      `json:"slice_seconds"`
	PerAgentSeconds int      `json:"per_agent_seconds"`
	FleetSeconds    int      `json:"fleet_seconds"`
	UpdatedBy       string   `json:"updated_by,omitempty"`
	UpdatedAt       int64    `json:"updated_at,omitempty"`
}

type ImprovementPolicyRequest struct {
	Command
	Enabled         bool     `json:"enabled"`
	ProjectIDs      []string `json:"project_ids"`
	WindowSeconds   int      `json:"window_seconds"`
	SliceSeconds    int      `json:"slice_seconds"`
	PerAgentSeconds int      `json:"per_agent_seconds"`
	FleetSeconds    int      `json:"fleet_seconds"`
	Reason          string   `json:"reason"`
}

type Improvement struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	ProductID          string `json:"product_id,omitempty"`
	Kind               string `json:"kind"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	RelatedTaskID      string `json:"related_task_id,omitempty"`
	IncidentID         string `json:"incident_id,omitempty"`
	RequesterID        string `json:"requester_id,omitempty"`
	Origin             string `json:"origin"`
	State              string `json:"state"`
	TaskID             string `json:"task_id,omitempty"`
	DuplicateOf        string `json:"duplicate_of,omitempty"`
	DecisionReason     string `json:"decision_reason,omitempty"`
	Revision           int64  `json:"revision"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type ImprovementRequest struct {
	Command
	ProjectID          string `json:"project_id"`
	ProductID          string `json:"product_id,omitempty"`
	Kind               string `json:"kind"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	RelatedTaskID      string `json:"related_task_id,omitempty"`
}

type ImprovementDecision struct {
	Command
	Decision    string      `json:"decision"`
	Reason      string      `json:"reason"`
	AssigneeID  string      `json:"assignee_id,omitempty"`
	DuplicateOf string      `json:"duplicate_of,omitempty"`
	Source      *TaskSource `json:"source,omitempty"`
}

type ImprovementResult struct {
	Request Improvement `json:"request"`
	Task    *Task       `json:"task,omitempty"`
}

type MaintenanceReservation struct {
	ID             string `json:"id"`
	AgentID        string `json:"agent_id"`
	ProjectID      string `json:"project_id"`
	SourceID       string `json:"source_id"`
	Kind           string `json:"kind"`
	PolicyRevision int64  `json:"policy_revision"`
	Seconds        int    `json:"seconds"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	State          string `json:"state"`
	RunID          string `json:"run_id,omitempty"`
}

type MaintenanceRequest struct {
	Command
	ProjectID string `json:"project_id"`
	SourceID  string `json:"source_id"`
	Kind      string `json:"kind"`
	Seconds   int    `json:"seconds"`
}

type MaintenanceSettlement struct {
	Command
}

type MaintenanceUsage struct {
	ServerObservedAt int64             `json:"server_observed_at"`
	Policy           ImprovementPolicy `json:"policy"`
	WindowStart      int64             `json:"window_start"`
	AgentSeconds     int               `json:"agent_seconds"`
	FleetSeconds     int               `json:"fleet_seconds"`
	AgentID          string            `json:"agent_id"`
	Reservations     int               `json:"reservations"`
}

type MaintenanceStatus struct {
	Reservation      MaintenanceReservation `json:"reservation"`
	ServerObservedAt int64                  `json:"server_observed_at"`
}
