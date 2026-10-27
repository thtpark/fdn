package domain

type UpgradeRelease struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Platform     string `json:"platform"`
	BundleURL    string `json:"bundle_url"`
	BundleSHA256 string `json:"bundle_sha256"`
	BundleBytes  int64  `json:"bundle_bytes"`
}

type UpgradeChallenge struct {
	ID             string `json:"id"`
	Nonce          string `json:"nonce"`
	InstanceID     string `json:"instance_id"`
	TargetID       string `json:"target_id"`
	Role           string `json:"role"`
	Generation     int64  `json:"generation"`
	CurrentVersion string `json:"current_version"`
	CurrentCommit  string `json:"current_commit"`
	PolicySHA256   string `json:"policy_sha256"`
	ExpiresAt      int64  `json:"expires_at"`
}

type UpgradeProposal struct {
	Challenge       UpgradeChallenge `json:"challenge"`
	Release         UpgradeRelease   `json:"release"`
	BackupRecipient string           `json:"backup_recipient"`
	Reason          string           `json:"reason"`
}

type UpgradeAuthorizationRequest struct {
	Command
	Proposal UpgradeProposal `json:"proposal"`
}

type UpgradePermit struct {
	Proposal        UpgradeProposal `json:"proposal"`
	ActorID         string          `json:"actor_id"`
	ActorGeneration int64           `json:"actor_generation"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expires_at"`
	Signature       string          `json:"signature"`
}

type UpgradeContext struct {
	InstanceID string  `json:"instance_id"`
	Version    string  `json:"version"`
	Commit     string  `json:"commit"`
	Agents     []Agent `json:"agents"`
}

type UpgradeNativeRequest struct {
	Action string         `json:"action"`
	ID     string         `json:"id"`
	Permit *UpgradePermit `json:"permit,omitempty"`
}

type UpgradeNativeReply struct {
	Challenge    *UpgradeChallenge `json:"challenge,omitempty"`
	State        string            `json:"state"`
	Version      string            `json:"version,omitempty"`
	Commit       string            `json:"commit,omitempty"`
	BackupSHA256 string            `json:"backup_sha256,omitempty"`
	Error        *Fault            `json:"error,omitempty"`
	Lease        *UpgradeLease     `json:"lease,omitempty"`
	Code         string            `json:"code,omitempty"`
}

type UpgradeLeaseRequest struct {
	PermitSHA256 string `json:"permit_sha256"`
	Nonce        string `json:"nonce"`
}

type UpgradeLease struct {
	PermitSHA256 string `json:"permit_sha256"`
	Nonce        string `json:"nonce"`
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Signature    string `json:"signature"`
}
