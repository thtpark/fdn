package domain

import (
	"encoding/hex"
	"strings"
)

func ValidSourceCommit(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 20 && value == strings.ToLower(value)
}

type AgentRestartRequest = DiagnosticRefreshRequest

type AgentRestartPermit struct {
	ID                  string `json:"id"`
	InstanceID          string `json:"instance_id"`
	RequesterID         string `json:"requester_id"`
	RequesterBindingID  string `json:"requester_binding_id"`
	RequesterGeneration int64  `json:"requester_generation"`
	TargetID            string `json:"target_id"`
	TargetGeneration    int64  `json:"target_generation"`
	ModeRevision        int64  `json:"mode_revision"`
	Reason              string `json:"reason"`
	CreatedAt           int64  `json:"created_at"`
	ExpiresAt           int64  `json:"expires_at"`
	Signature           string `json:"signature"`
}

type AgentRestartNativeRequest struct {
	Action string             `json:"action"`
	Permit AgentRestartPermit `json:"permit"`
	Lease  *UpgradeLease      `json:"lease,omitempty"`
}

type AgentRestartNativeReply struct {
	State     string                    `json:"state"`
	Challenge *UpgradeChallenge         `json:"challenge,omitempty"`
	Runtime   *NativeRuntimeObservation `json:"runtime,omitempty"`
	Error     *Fault                    `json:"error,omitempty"`
}

type AgentRestartReport struct {
	Command
	ID     string                `json:"id"`
	State  string                `json:"state"`
	Result *AgentOperationResult `json:"result,omitempty"`
}

type AgentRestartLeaseRequest struct {
	Command
	ID           string `json:"id"`
	Nonce        string `json:"nonce"`
	PermitSHA256 string `json:"permit_sha256"`
}

type NativeRuntimeObservation struct {
	Version            string `json:"version"`
	Commit             string `json:"commit"`
	IdentityGeneration int64  `json:"identity_generation"`
	ServicesRunning    bool   `json:"services_running"`
	PendingInvocations int    `json:"pending_invocations"`
	PendingReports     int    `json:"pending_reports"`
	SessionFenced      bool   `json:"session_fenced"`
}
