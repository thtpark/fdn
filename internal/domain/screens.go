package domain

// Screen contains public display capabilities, never local paths or credentials.
type Screen struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Scope        string   `json:"scope"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Primary      bool     `json:"primary"`
	Applications []string `json:"applications"`
}

type ScreensRequest struct {
	Command
	Screens []Screen `json:"screens"`
}

type AssistanceHelpRequest struct {
	Command
	Mode            string `json:"mode"`
	Purpose         string `json:"purpose"`
	Provider        string `json:"provider,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	ScreenID        string `json:"screen_id,omitempty"`
	ApplicationID   string `json:"application_id,omitempty"`
	DesktopFallback bool   `json:"desktop_fallback,omitempty"`
	Reason          string `json:"reason"`
}

type AssistanceHelp struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Messages  []Message `json:"messages"`
	ExpiresAt int64     `json:"expires_at"`
}
