package domain

import "strings"

const (
	CompanyNotes   = "company"
	ProjectNotes   = "project"
	AgentNotes     = "agent"
	MaxNoteContent = 64 << 10
)

type Note struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	TargetID  string `json:"target_id,omitempty"`
	Content   string `json:"content"`
	Revision  int64  `json:"revision"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type NoteRequest struct {
	Command
	Scope    string  `json:"scope"`
	TargetID string  `json:"target_id,omitempty"`
	Content  *string `json:"content"`
	Reason   string  `json:"reason"`
}

func NoteID(scope, target string) (string, error) {
	switch scope {
	case CompanyNotes:
		if target == "" {
			return "note_company", nil
		}
	case ProjectNotes, AgentNotes:
		prefix := scope + "_"
		if strings.HasPrefix(target, prefix) && len(target) > len(prefix) && ValidOperation(target) {
			return "note_" + target, nil
		}
	}
	return "", Fail(Invalid, "notes require scope company without a target, project with a project ID, or agent with an agent ID")
}
