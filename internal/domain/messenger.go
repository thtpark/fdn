package domain

import (
	"slices"
	"strings"
)

const (
	RoomKindDirect  = "direct"
	RoomKindGroup   = "group"
	RoomKindProject = "project"

	MaxRoomMembers = 64
)

func ValidRoomKind(kind string) bool {
	return kind == RoomKindDirect || kind == RoomKindGroup || kind == RoomKindProject
}

func DirectPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

func DirectRoomID(a, b string) string {
	return "room_" + Hash("direct-room:" + DirectPairKey(a, b))[:32]
}

func ProjectRoomID(projectID string) string {
	return "room_" + Hash("project-room:" + projectID)[:32]
}

type RoomReadReceipt struct {
	AgentID string `json:"agent_id"`
	ReadAt  int64  `json:"read_at"`
}

type RoomReplyPreview struct {
	ID              string `json:"id"`
	SenderID        string `json:"sender_id"`
	Body            string `json:"body"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

type MessageRoom struct {
	ID                 string   `json:"id"`
	Kind               string   `json:"kind"`
	Name               string   `json:"name,omitempty"`
	ProjectID          string   `json:"project_id,omitempty"`
	MemberIDs          []string `json:"member_ids"`
	AddedMemberIDs     []string `json:"added_member_ids,omitempty"`
	CreatedBy          string   `json:"created_by"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	LastMessageAt      int64    `json:"last_message_at,omitempty"`
	LastMessageID      string   `json:"last_message_id,omitempty"`
	LastMessagePreview string   `json:"last_message_preview,omitempty"`
	LastSenderID       string   `json:"last_sender_id,omitempty"`
	Revision           int64    `json:"revision"`
	UnreadCount        int      `json:"unread_count,omitempty"`
	MentionCount       int      `json:"mention_count,omitempty"`
	LastReadAt         int64    `json:"last_read_at,omitempty"`
}

type RoomMessage struct {
	ID              string            `json:"id"`
	RoomID          string            `json:"room_id"`
	RoomKind        string            `json:"room_kind,omitempty"`
	ProjectID       string            `json:"project_id,omitempty"`
	SenderID        string            `json:"sender_id"`
	Body            string            `json:"body"`
	Origin          string            `json:"origin"`
	Kind            string            `json:"kind"`
	Urgent          bool              `json:"urgent,omitempty"`
	ResponseRequest bool              `json:"response_requested"`
	ReplyToID       string            `json:"reply_to_id,omitempty"`
	ReplyPreview    *RoomReplyPreview `json:"reply_preview,omitempty"`
	Mentions        []string          `json:"mentions,omitempty"`
	TargetAgentIDs  []string          `json:"target_agent_ids,omitempty"`
	Attachments     []FileReference   `json:"attachments,omitempty"`
	ReadBy          []RoomReadReceipt `json:"read_by,omitempty"`
	CreatedAt       int64             `json:"created_at"`
	Revision        int64             `json:"revision"`
}

type RoomDelivery struct {
	Message         RoomMessage `json:"message"`
	Room            MessageRoom `json:"room"`
	AgentID         string      `json:"agent_id"`
	State           string      `json:"state"`
	ResponseRequest bool        `json:"response_requested"`
	Urgent          bool        `json:"urgent,omitempty"`
	ReadAt          int64       `json:"read_at,omitempty"`
	HandledAt       int64       `json:"handled_at,omitempty"`
	Revision        int64       `json:"revision"`
}

type RoomCreateRequest struct {
	Command
	Kind      string   `json:"kind"`
	Name      string   `json:"name,omitempty"`
	ProjectID string   `json:"project_id,omitempty"`
	MemberIDs []string `json:"member_ids,omitempty"`
}

func (r RoomCreateRequest) Validate() error {
	if !ValidRoomKind(r.Kind) {
		return Fail(Invalid, "room kind must be direct, group or project")
	}
	if err := CheckText(r.Name, "room name", 160, false); err != nil {
		return err
	}
	if r.Kind != RoomKindGroup && strings.TrimSpace(r.Name) != "" {
		return Fail(Invalid, "only group rooms accept a custom name")
	}
	if r.Kind == RoomKindProject {
		if !ValidOperation(r.ProjectID) || r.ProjectID == AdhocProjectID {
			return Fail(Invalid, "project rooms require an active non-Adhoc project ID")
		}
	} else if r.ProjectID != "" {
		return Fail(Invalid, "only project rooms may specify project_id")
	}
	if len(r.MemberIDs) > MaxRoomMembers {
		return Fail(Invalid, "room exceeds the maximum member count")
	}
	for _, id := range r.MemberIDs {
		if !ValidOperation(id) {
			return Fail(Invalid, "invalid room member agent ID")
		}
	}
	return nil
}

type RoomChangeRequest struct {
	Command
	Name         *string  `json:"name,omitempty"`
	AddMemberIDs []string `json:"add_member_ids,omitempty"`
}

func (r RoomChangeRequest) Validate() error {
	if r.Name == nil && len(r.AddMemberIDs) == 0 {
		return Fail(Invalid, "room change requires a new name or members to add")
	}
	if r.Name != nil {
		if err := CheckText(*r.Name, "room name", 160, false); err != nil {
			return err
		}
	}
	if len(r.AddMemberIDs) > MaxRoomMembers {
		return Fail(Invalid, "too many members in one room update")
	}
	seen := make(map[string]bool, len(r.AddMemberIDs))
	for _, id := range r.AddMemberIDs {
		if !ValidOperation(id) || seen[id] {
			return Fail(Invalid, "room member additions must be unique valid agent IDs")
		}
		seen[id] = true
	}
	return nil
}

type RoomMessageRequest struct {
	Command
	Body            string   `json:"body"`
	Origin          string   `json:"origin,omitempty"`
	Urgent          bool     `json:"urgent,omitempty"`
	ResponseRequest *bool    `json:"response_requested,omitempty"`
	ReplyToID       string   `json:"reply_to_id,omitempty"`
	AttachmentIDs   []string `json:"attachment_ids,omitempty"`
}

func (r RoomMessageRequest) Validate() error {
	if err := CheckText(r.Body, "message body", 32768, len(r.AttachmentIDs) == 0); err != nil {
		return err
	}
	if r.Origin != "" && !slices.Contains([]string{"human", "ai"}, r.Origin) {
		return Fail(Invalid, "room message origin must be human or ai")
	}
	if r.ReplyToID != "" && !ValidOperation(r.ReplyToID) {
		return Fail(Invalid, "invalid reply_to_id")
	}
	if len(r.AttachmentIDs) > MaxAttachments {
		return Fail(Invalid, "at most 32 files may be attached")
	}
	for _, id := range r.AttachmentIDs {
		if !ValidOperation(id) {
			return Fail(Invalid, "invalid attachment ID")
		}
	}
	return nil
}

type RoomReadRequest struct {
	Command
	UpToMessageID string `json:"up_to_message_id,omitempty"`
}

type RoomReadResult struct {
	RoomID    string `json:"room_id"`
	ReadCount int    `json:"read_count"`
	ReadAt    int64  `json:"read_at"`
}

type RoomDeliveryReceipt struct {
	Command
	State string `json:"state"`
}
