package provider

import "github.com/thtpark/fdn/internal/domain"

type ImprovementChoice struct {
	Decision    string             `json:"decision"`
	AssigneeID  string             `json:"assignee_id,omitempty"`
	DuplicateOf string             `json:"duplicate_of,omitempty"`
	Source      *domain.TaskSource `json:"source,omitempty"`
}
