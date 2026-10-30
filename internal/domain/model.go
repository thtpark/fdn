package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const SchemaVersion = 2
const APIVersion = 2
const MaxBody = 1 << 20
const HistoryLimit = 3
const AuthenticationRequestTTL int64 = 900

var Version = "dev"
var Commit = "unknown"

var operationPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func NewID(prefix string) string {
	return prefix + "_" + hex.EncodeToString(randomBytes(16))
}

func NewToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("operating system random source unavailable")
	}
	return b
}

func Hash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func ValidHash(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && value == strings.ToLower(value)
}

func ValidOperation(value string) bool { return operationPattern.MatchString(value) }

func CheckText(value, name string, max int, required bool) error {
	if (required && strings.TrimSpace(value) == "") || len(value) > max || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return Fail(Invalid, name+" is empty, too long, or contains a NUL byte")
	}
	return nil
}

type Command struct {
	OperationID      string `json:"operation_id"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}

type Agent struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Founder         bool             `json:"founder"`
	Active          bool             `json:"active"`
	Banned          bool             `json:"banned,omitempty"`
	BanReason       string           `json:"ban_reason,omitempty"`
	SponsorID       string           `json:"sponsor_id,omitempty"`
	CreatedAt       int64            `json:"created_at"`
	LastSeen        int64            `json:"last_seen"`
	Revision        int64            `json:"revision"`
	Mode            string           `json:"mode"`
	ModeRequestedBy string           `json:"mode_requested_by,omitempty"`
	ModeReason      string           `json:"mode_reason,omitempty"`
	ModeChangedAt   int64            `json:"mode_changed_at,omitempty"`
	Screens         []Screen         `json:"screens,omitempty"`
	Status          *AgentTextStatus `json:"status,omitempty"`
}

type Project struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	OwnerID     string   `json:"owner_id"`
	Delegates   []string `json:"delegates"`
	Revision    int64    `json:"revision"`
	Epoch       int64    `json:"epoch"`
	Archived    bool     `json:"archived"`
	CreatedAt   int64    `json:"created_at"`
}

type Status string

const (
	Draft           Status = "draft"
	Backlogged      Status = "backlogged"
	Todo            Status = "todo"
	Running         Status = "in-progress"
	Paused          Status = "paused"
	Completed       Status = "completed"
	Done            Status = "done"
	WontDo          Status = "wont-do"
	CancelledStatus Status = "cancelled"
)

func (s Status) Terminal() bool { return s == Done || s == WontDo || s == CancelledStatus }

func (s Status) Valid() bool {
	switch s {
	case Draft, Backlogged, Todo, Running, Paused, Completed, Done, WontDo, CancelledStatus:
		return true
	default:
		return false
	}
}

type ApprovalPolicy struct {
	RequiredActors          []string `json:"required_actors"`
	IndependentReadiness    bool     `json:"independent_readiness"`
	IndependentVerification bool     `json:"independent_verification"`
}

const AdhocProjectID = "project_adhoc"

func EligibleApprover(a Agent, p Project, t Task, verification bool) bool {
	ownAdhoc := !verification && p.ID == AdhocProjectID && t.ProjectID == p.ID &&
		t.CreatedBy == a.ID && t.AssigneeID == a.ID
	if !a.Active || a.Banned || p.Archived || (!a.Founder && !slices.Contains(p.Delegates, a.ID) && !ownAdhoc) {
		return false
	}
	if len(t.Policy.RequiredActors) != 0 && !slices.Contains(t.Policy.RequiredActors, a.ID) {
		return false
	}
	independent := t.Policy.IndependentReadiness
	if verification {
		independent = t.Policy.IndependentVerification
	}
	return !independent || a.ID != t.AssigneeID
}

const ExecutionRun = "execution"
const VerificationRun = "verification"

type RetryPolicy struct {
	MaxRetries     int   `json:"max_retries"`
	BackoffSeconds int64 `json:"backoff_seconds"`
}

func (p RetryPolicy) Validate() error {
	if p.MaxRetries < 0 || p.MaxRetries > 10 || p.BackoffSeconds < 1 || p.BackoffSeconds > 86400 {
		return Fail(Invalid, "retry policy requires 0-10 retries and 1-86400 backoff seconds")
	}
	return nil
}

type Approval struct {
	ActorID          string `json:"actor_id"`
	SpecGeneration   int64  `json:"spec_generation"`
	ResultGeneration int64  `json:"result_generation,omitempty"`
	ProjectEpoch     int64  `json:"project_epoch"`
	RunID            string `json:"run_id,omitempty"`
	Attempt          int    `json:"attempt,omitempty"`
	At               int64  `json:"at"`
	OperationID      string `json:"operation_id"`
}

type Clarity struct {
	ActorID              string   `json:"actor_id"`
	SpecGeneration       int64    `json:"spec_generation"`
	DiscussionGeneration int64    `json:"discussion_generation"`
	ProjectEpoch         int64    `json:"project_epoch"`
	Rationale            string   `json:"rationale"`
	AnswerIDs            []string `json:"answer_ids"`
}

type Waiting struct {
	Kind         string   `json:"kind"`
	Reason       string   `json:"reason"`
	RequestedIDs []string `json:"requested_ids"`
	QuestionIDs  []string `json:"question_ids"`
	Since        int64    `json:"since"`
}

type ActiveRun struct {
	ID             string `json:"id"`
	Kind           string `json:"kind,omitempty"`
	AgentID        string `json:"agent_id"`
	SpecGeneration int64  `json:"spec_generation"`
	Attempt        int    `json:"attempt"`
	StopRequested  bool   `json:"stop_requested"`
	AfterStop      Status `json:"after_stop,omitempty"`
}

type Result struct {
	RunID          string   `json:"run_id"`
	SpecGeneration int64    `json:"spec_generation"`
	Summary        string   `json:"summary"`
	Evidence       []string `json:"evidence"`
}

type Task struct {
	ID                   string         `json:"id"`
	ProjectID            string         `json:"project_id"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	AcceptanceCriteria   string         `json:"acceptance_criteria"`
	Status               Status         `json:"status"`
	Priority             int            `json:"priority"`
	Importance           int            `json:"importance"`
	AssigneeID           string         `json:"assignee_id,omitempty"`
	Provider             string         `json:"provider,omitempty"`
	Model                string         `json:"model,omitempty"`
	WorkspaceRef         string         `json:"workspace_ref,omitempty"`
	ParentID             string         `json:"parent_id,omitempty"`
	Repetitive           bool           `json:"repetitive"`
	Revision             int64          `json:"revision"`
	SpecGeneration       int64          `json:"spec_generation"`
	DiscussionGeneration int64          `json:"discussion_generation"`
	ResultGeneration     int64          `json:"result_generation"`
	CreatedBy            string         `json:"created_by"`
	CreatedAt            int64          `json:"created_at"`
	UpdatedAt            int64          `json:"updated_at"`
	ArchivedAt           int64          `json:"archived_at,omitempty"`
	Policy               ApprovalPolicy `json:"policy"`
	RetryPolicy          RetryPolicy    `json:"retry_policy"`
	Readiness            *Approval      `json:"readiness,omitempty"`
	Verification         *Approval      `json:"verification,omitempty"`
	RetryApproval        *Approval      `json:"retry_approval,omitempty"`
	Clarity              *Clarity       `json:"clarity,omitempty"`
	Waiting              *Waiting       `json:"waiting,omitempty"`
	ActiveRun            *ActiveRun     `json:"active_run,omitempty"`
	LastRunID            string         `json:"last_run_id,omitempty"`
	Attempts             int            `json:"attempts"`
	Failures             int            `json:"failures"`
	NextAttemptAt        int64          `json:"next_attempt_at,omitempty"`
	UnsafeRetry          bool           `json:"unsafe_retry"`
	Result               *Result        `json:"result,omitempty"`
}

type Run struct {
	ID                   string   `json:"id"`
	Kind                 string   `json:"kind,omitempty"`
	TaskID               string   `json:"task_id"`
	AgentID              string   `json:"agent_id"`
	SpecGeneration       int64    `json:"spec_generation"`
	ResultGeneration     int64    `json:"result_generation,omitempty"`
	DiscussionGeneration int64    `json:"discussion_generation,omitempty"`
	ProjectEpoch         int64    `json:"project_epoch,omitempty"`
	AgentRevision        int64    `json:"agent_revision,omitempty"`
	Decision             string   `json:"decision,omitempty"`
	Attempt              int      `json:"attempt"`
	Provider             string   `json:"provider"`
	Model                string   `json:"model,omitempty"`
	State                string   `json:"state"`
	Summary              string   `json:"summary,omitempty"`
	Evidence             []string `json:"evidence"`
	Checkpoint           string   `json:"checkpoint,omitempty"`
	StartedAt            int64    `json:"started_at"`
	FinishedAt           int64    `json:"finished_at,omitempty"`
}

type ReviewReference struct {
	RunID            string `json:"run_id"`
	SpecGeneration   int64  `json:"spec_generation"`
	ResultGeneration int64  `json:"result_generation"`
	Decision         string `json:"decision"`
}

type Entry struct {
	Review     *ReviewReference `json:"review,omitempty"`
	ID         string           `json:"id"`
	TaskID     string           `json:"task_id"`
	Kind       string           `json:"kind"`
	Body       string           `json:"body"`
	ParentID   string           `json:"parent_id,omitempty"`
	AuthorID   string           `json:"author_id"`
	Mentions   []string         `json:"mentions"`
	Generation int64            `json:"generation"`
	Revision   int64            `json:"revision"`
	CreatedAt  int64            `json:"created_at"`
	UpdatedAt  int64            `json:"updated_at"`
}

type Attention struct {
	EntryID    string `json:"entry_id"`
	TaskID     string `json:"task_id"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}

type AuthenticationRefresh struct {
	Provider  string `json:"provider"`
	ProjectID string `json:"project_id,omitempty"`
	Reason    string `json:"reason"`
}

type Message struct {
	ID                    string                 `json:"id"`
	SenderID              string                 `json:"sender_id"`
	RecipientID           string                 `json:"recipient_id"`
	Body                  string                 `json:"body"`
	Origin                string                 `json:"origin"`
	Kind                  string                 `json:"kind"`
	Urgent                bool                   `json:"urgent"`
	ResponseRequest       bool                   `json:"response_requested"`
	State                 string                 `json:"state"`
	CreatedAt             int64                  `json:"created_at"`
	ReadAt                int64                  `json:"read_at,omitempty"`
	ExpiresAt             int64                  `json:"expires_at,omitempty"`
	Revision              int64                  `json:"revision"`
	AuthenticationRefresh *AuthenticationRefresh `json:"authentication_refresh,omitempty"`
	AgentOperation        *AgentOperation        `json:"agent_operation,omitempty"`
	ThreadID              string                 `json:"thread_id,omitempty"`
	ReplyToID             string                 `json:"reply_to_id,omitempty"`
	Insistence            bool                   `json:"insistence,omitempty"`
	Request               *MessageActionState    `json:"request,omitempty"`
	RequestUpdate         *MessageActionUpdate   `json:"request_update,omitempty"`
}

type Session struct {
	ID              string `json:"id"`
	RequesterID     string `json:"requester_id"`
	TargetID        string `json:"target_id"`
	ProjectID       string `json:"project_id"`
	TaskID          string `json:"task_id,omitempty"`
	Mode            string `json:"mode"`
	Purpose         string `json:"purpose"`
	Provider        string `json:"provider,omitempty"`
	Reason          string `json:"reason"`
	State           string `json:"state"`
	OpenRequested   bool   `json:"open_requested"`
	ExpiresAt       int64  `json:"expires_at"`
	Revision        int64  `json:"revision"`
	CreatedAt       int64  `json:"created_at"`
	GrantID         string `json:"grant_id,omitempty"`
	ScreenID        string `json:"screen_id,omitempty"`
	ApplicationID   string `json:"application_id,omitempty"`
	DesktopFallback bool   `json:"desktop_fallback,omitempty"`
}

type AssistanceGrant struct {
	ID              string `json:"id"`
	RequesterID     string `json:"requester_id"`
	TargetID        string `json:"target_id"`
	ProjectID       string `json:"project_id,omitempty"`
	Mode            string `json:"mode"`
	Purpose         string `json:"purpose"`
	Provider        string `json:"provider,omitempty"`
	Active          bool   `json:"active"`
	Revision        int64  `json:"revision"`
	CreatedBy       string `json:"created_by"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	Reason          string `json:"reason"`
	ScreenID        string `json:"screen_id,omitempty"`
	ApplicationID   string `json:"application_id,omitempty"`
	DesktopFallback bool   `json:"desktop_fallback,omitempty"`
}

type History struct {
	Sequence  int64           `json:"sequence"`
	SourceID  string          `json:"source_id"`
	ActorID   string          `json:"actor_id"`
	Action    string          `json:"action"`
	Reason    string          `json:"reason,omitempty"`
	CreatedAt int64           `json:"created_at"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

type Event struct {
	Sequence  int64  `json:"sequence"`
	Topic     string `json:"topic"`
	SourceID  string `json:"source_id"`
	TaskID    string `json:"task_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type ListQuery struct {
	ProjectID       string `json:"project_id,omitempty"`
	AssigneeID      string `json:"assignee_id,omitempty"`
	Status          Status `json:"status,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Limit           int    `json:"limit"`
	Cursor          string `json:"cursor,omitempty"`
}

func ValidProvider(name string) bool { return name == "codex" || name == "copilot" }
