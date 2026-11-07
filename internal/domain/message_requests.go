package domain

type MessageAction struct {
	Kind         string         `json:"kind"`
	ProjectID    string         `json:"project_id,omitempty"`
	TaskID       string         `json:"task_id,omitempty"`
	Upgrade      *UpgradePermit `json:"upgrade,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
}

type MessageActionState struct {
	Action              MessageAction  `json:"action"`
	State               string         `json:"state"`
	Reason              string         `json:"reason,omitempty"`
	SenderGeneration    int64          `json:"sender_generation"`
	RecipientGeneration int64          `json:"recipient_generation"`
	ProjectEpoch        int64          `json:"project_epoch,omitempty"`
	SpecGeneration      int64          `json:"spec_generation,omitempty"`
	ResultGeneration    int64          `json:"result_generation,omitempty"`
	UpdatedAt           int64          `json:"updated_at"`
	UpgradeResult       *UpgradeResult `json:"upgrade_result,omitempty"`
}

type MessageActionReport struct {
	Command
	State         string         `json:"state"`
	Reason        string         `json:"reason"`
	UpgradeResult *UpgradeResult `json:"upgrade_result,omitempty"`
}

type MessageActionUpdate struct {
	RequestID     string         `json:"request_id"`
	State         string         `json:"state"`
	Reason        string         `json:"reason"`
	UpgradeResult *UpgradeResult `json:"upgrade_result,omitempty"`
}

type MessageWorkerCommand struct {
	MessageID       string                  `json:"message_id,omitempty"`
	Report          *MessageActionReport    `json:"report,omitempty"`
	Preflight       bool                    `json:"preflight,omitempty"`
	PreflightReport *UpgradePreflightReport `json:"preflight_report,omitempty"`
}

type MessageWorkerReply struct {
	Messages   []Message          `json:"messages"`
	Message    *Message           `json:"message,omitempty"`
	Error      *Fault             `json:"error,omitempty"`
	Preflights []UpgradePreflight `json:"preflights,omitempty"`
	Preflight  *UpgradePreflight  `json:"preflight,omitempty"`
}
