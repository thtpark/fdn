package domain

// Update is a bodyless invalidation hint, not a grant of authority or a receipt.
// Private source references are delivered only to their current feed parties.
type Update struct {
	Sequence  int64  `json:"sequence"`
	Topic     string `json:"topic"`
	SourceID  string `json:"source_id"`
	TaskID    string `json:"task_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Urgent    bool   `json:"urgent,omitempty"`
}
