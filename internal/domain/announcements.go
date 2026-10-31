package domain

import (
	"slices"
	"strings"
)

const MaxAnnouncementRecipients = 1000

type AnnouncementRecipient struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
}

type AnnouncementAudience struct {
	ActorID      string                  `json:"actor_id"`
	Scope        string                  `json:"scope"`
	ProjectID    string                  `json:"project_id,omitempty"`
	ProjectEpoch int64                   `json:"project_epoch,omitempty"`
	Recipients   []AnnouncementRecipient `json:"recipients"`
}

type AnnouncementRequest struct {
	Command
	Scope        string   `json:"scope"`
	ProjectID    string   `json:"project_id,omitempty"`
	ProjectEpoch int64    `json:"project_epoch,omitempty"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	RecipientIDs []string `json:"recipient_ids"`
}

type AnnouncementDelivery struct {
	AgentID   string `json:"agent_id"`
	MessageID string `json:"message_id"`
}

type AnnouncementReceipt struct {
	ID         string                 `json:"id"`
	Scope      string                 `json:"scope"`
	ProjectID  string                 `json:"project_id,omitempty"`
	Kind       string                 `json:"kind"`
	CreatedAt  int64                  `json:"created_at"`
	Deliveries []AnnouncementDelivery `json:"deliveries"`
}

type AnnouncementContext struct {
	ID                  string `json:"id"`
	Scope               string `json:"scope"`
	ProjectID           string `json:"project_id,omitempty"`
	ProjectEpoch        int64  `json:"project_epoch,omitempty"`
	Kind                string `json:"kind"`
	SenderGeneration    int64  `json:"sender_generation"`
	RecipientGeneration int64  `json:"recipient_generation"`
}

func ValidateAnnouncement(q AnnouncementRequest) error {
	if !slices.Contains([]string{"global", "project"}, q.Scope) || !slices.Contains([]string{"notice", "request", "upgrade-request"}, q.Kind) ||
		len(q.RecipientIDs) == 0 || len(q.RecipientIDs) > MaxAnnouncementRecipients ||
		(q.Scope == "global" && (q.ProjectID != "" || q.ProjectEpoch != 0)) ||
		(q.Scope == "project" && (q.ProjectID == "" || q.ProjectEpoch <= 0)) {
		return Fail(Invalid, "announcement needs a current global/project audience and supported notice/request kind")
	}
	if err := CheckText(q.Title, "announcement title", 160, true); err != nil {
		return err
	}
	if err := CheckText(q.Title+"\n\n"+q.Body, "announcement body", 32768, true); err != nil {
		return err
	}
	if err := CheckText(q.Body, "announcement body", 32768, true); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range q.RecipientIDs {
		if !ValidOperation(id) || !strings.HasPrefix(id, "agent_") || seen[id] {
			return Fail(Invalid, "announcement audience must contain distinct enrolled agent IDs")
		}
		seen[id] = true
	}
	return nil
}
