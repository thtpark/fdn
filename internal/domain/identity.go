package domain

// CertificateIdentity is signed into client certificates, never taken from HTTP headers.
type CertificateIdentity struct {
	InstanceID      string `json:"instance_id"`
	AgentID         string `json:"agent_id"`
	Generation      int64  `json:"generation"`
	OwnerGeneration int64  `json:"owner_generation,omitempty"`
	Purpose         string `json:"purpose"`
}

type PublicIdentity struct {
	Version   int    `json:"version"`
	RequestID string `json:"request_id"`
	Name      string `json:"name"`
	SSHKey    string `json:"ssh_key"`
	AuthKey   string `json:"auth_key"`
	SSHProof  string `json:"ssh_proof"`
	AuthProof string `json:"auth_proof"`
}

type RegisterIdentityRequest struct {
	Command
	Identity PublicIdentity `json:"identity"`
}

type AgentIdentity struct {
	AgentID         string `json:"agent_id"`
	InstanceID      string `json:"instance_id"`
	RequestID       string `json:"request_id"`
	SSHUser         string `json:"ssh_user"`
	UID             int64  `json:"uid"`
	SSHKey          string `json:"ssh_key"`
	SSHFingerprint  string `json:"ssh_fingerprint"`
	AuthKey         string `json:"auth_key"`
	AuthFingerprint string `json:"auth_fingerprint"`
	Generation      int64  `json:"generation"`
	State           string `json:"state"`
	Assurance       string `json:"assurance"`
	ApprovedBy      string `json:"approved_by"`
	BanReason       string `json:"ban_reason,omitempty"`
	OwnerGeneration int64  `json:"owner_generation"`
	OwnerKey        string `json:"owner_key,omitempty"`
}

type IdentityChallenge struct {
	Nonce      string `json:"nonce"`
	AgentID    string `json:"agent_id"`
	InstanceID string `json:"instance_id"`
	Generation int64  `json:"generation"`
	ExpiresAt  int64  `json:"expires_at"`
}

type ActivateIdentityRequest struct {
	Command
	Nonce string `json:"nonce"`
	Proof string `json:"proof"`
}

type IdentityCertificate struct {
	Agent       Agent         `json:"agent"`
	Identity    AgentIdentity `json:"identity"`
	Certificate string        `json:"certificate"`
}

type OwnerRequest struct {
	OwnerKey string `json:"owner_key"`
	Nonce    string `json:"nonce"`
	Proof    string `json:"proof"`
}

type OwnerCertificate struct {
	Certificate string            `json:"certificate"`
	Generation  int64             `json:"generation"`
	Challenge   IdentityChallenge `json:"challenge"`
}

type RecoverIdentityRequest struct {
	Command
	Reason   string         `json:"reason"`
	Identity PublicIdentity `json:"identity"`
}
