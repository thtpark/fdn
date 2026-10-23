package domain

import "slices"

func ValidateDiagnosticRefreshRequest(req DiagnosticRefreshRequest) error {
	return validateAgentOperationRequest(req.Command, req.Reason)
}

func ValidateSessionRecoveryRequest(req SessionRecoveryRequest) error {
	if err := validateAgentOperationRequest(req.Command, req.Reason); err != nil {
		return err
	}
	if !ValidOperation(req.SessionID) || !ValidHash(req.ExpectedFence) {
		return Fail(Invalid, "session recovery requires an exact session_id and canonical SHA256 expected_fence")
	}
	return nil
}

func validateAgentOperationRequest(command Command, reason string) error {
	if !ValidOperation(command.OperationID) || command.ExpectedRevision != 0 {
		return Fail(Invalid, "a new agent operation requires operation_id and no expected_revision")
	}
	return CheckText(reason, "nonsecret agent operation reason", 8192, true)
}

func AgentOperationTerminal(state string) bool {
	return slices.Contains([]string{"refreshed", "recovered", "rejected", "operator-required", "expired"}, state)
}

func TerminalSessionState(state string) bool {
	return slices.Contains([]string{"denied", "revoked", "closed", "expired", "failed"}, state)
}

func ValidAgentOperationCode(code string) bool {
	return slices.Contains([]string{
		"observations-collected", "configuration-unavailable", "slot-occupied", "session-live",
		"session-unavailable", "fence-mismatch", "cleanup-unproven", "session-recovered",
		"operation-expired", "authority-changed", "unsupported-operation", "inspection-unavailable", "recovery-unavailable",
	}, code)
}

func ValidateAgentInspection(inspection AgentInspection) error {
	if inspection.ObservedAt <= 0 || inspection.ObservedAt > maxHealthTimestamp || !slices.Contains([]string{"free", "occupied", "unknown"}, inspection.WorkSlot) {
		return Fail(Invalid, "inspection requires a bounded observation time and supported work slot")
	}
	if h := inspection.Health; h != nil {
		if err := ValidateWorkerHealth(*h); err != nil {
			return err
		}
		if h.ObservedAt > inspection.ObservedAt {
			return Fail(Invalid, "health cannot postdate its inspection")
		}
	}
	if p := inspection.Provider; p != nil {
		if !ValidProvider(p.Provider) || !ValidHealthVersion(p.Version) || p.ObservedAt <= 0 || p.ObservedAt > inspection.ObservedAt {
			return Fail(Invalid, "provider observation requires supported provider, safe version and bounded time")
		}
		if !slices.Contains([]string{"unknown", "authenticated", "login-required", "credentials-present-unverified", "signed-in", "sign-in-required"}, p.Authentication) {
			return Fail(Invalid, "provider authentication observation is unsupported")
		}
		if p.ProblemCode != "" && !safeObservationProblem(p.ProblemCode) {
			return Fail(Invalid, "provider problem must be a supported safe code")
		}
		if p.ProblemCode != "" && slices.Contains([]string{"authenticated", "signed-in"}, p.Authentication) {
			return Fail(Invalid, "a failed provider probe cannot assert successful authentication")
		}
	}
	if f := inspection.SessionFence; f != nil {
		if !ValidOperation(f.SessionID) || !ValidHash(f.Fingerprint) || !slices.Contains([]string{"requested", "consented", "active", "denied", "revoked", "closed", "expired", "failed", "unknown"}, f.SessionState) {
			return Fail(Invalid, "session fence requires exact safe identifiers and a supported session state")
		}
		if !slices.Contains([]string{"recoverable", "session-live", "cleanup-unproven", "slot-occupied", "session-unavailable", "unsupported-evidence"}, f.RecoveryState) {
			return Fail(Invalid, "session fence recovery state is unsupported")
		}
		if (f.RecoveryState == "recoverable" && (!f.CleanupProven || !TerminalSessionState(f.SessionState))) ||
			(f.RecoveryState == "cleanup-unproven" && f.CleanupProven) ||
			(f.RecoveryState == "session-live" && !slices.Contains([]string{"requested", "consented", "active"}, f.SessionState)) ||
			(f.RecoveryState == "session-unavailable" && f.SessionState != "unknown") {
			return Fail(Invalid, "session fence recovery observation contradicts its evidence")
		}
	}
	return nil
}

func safeObservationProblem(code Code) bool {
	return slices.Contains([]Code{Invalid, NotFound, Denied, Conflict, Prerequisite, Unavailable, Provider, Internal, Cancelled}, code)
}

func ValidateAgentOperationReport(req AgentOperationReport) error {
	if !ValidOperation(req.OperationID) || req.ExpectedRevision < 1 {
		return Fail(Invalid, "agent operation reports require operation_id and expected_revision")
	}
	if req.State == "checking" {
		if req.Result != nil {
			return Fail(Invalid, "checking cannot contain a result or observation")
		}
		return nil
	}
	if !AgentOperationTerminal(req.State) || req.Result == nil {
		return Fail(Invalid, "an agent operation report requires a supported final state and result")
	}
	r := req.Result
	if !ValidAgentOperationCode(r.Code) || !slices.Contains([]string{"ai", "manual"}, r.ModeAtCheck) {
		return Fail(Invalid, "agent operation result requires a supported safe code and observed mode")
	}
	if r.CapturedAt < 0 || r.CapturedAt > maxHealthTimestamp {
		return Fail(Invalid, "agent operation capture bound must be a bounded server-clock estimate")
	}
	if r.Observation != nil {
		if err := ValidateAgentInspection(*r.Observation); err != nil {
			return err
		}
	}
	if r.AutomationMayResume && (req.State != "recovered" || r.ModeAtCheck != "ai") {
		return Fail(Invalid, "only an exact recovered session in ai mode may indicate automation eligibility")
	}
	if r.Code == "cleanup-unproven" && req.State != "operator-required" {
		return Fail(Invalid, "unproven session cleanup requires an explicit operator-required outcome")
	}
	switch req.State {
	case "refreshed":
		if r.Code != "observations-collected" || r.Observation == nil || (r.Observation.Health == nil && r.Observation.Provider == nil) {
			return Fail(Invalid, "refreshed requires captured health or provider observations, not a readiness claim")
		}
	case "recovered":
		if r.Code != "session-recovered" || r.Observation == nil || r.Observation.WorkSlot != "free" || r.Observation.SessionFence == nil || !r.Observation.SessionFence.CleanupProven || r.Observation.SessionFence.RecoveryState != "recoverable" || !TerminalSessionState(r.Observation.SessionFence.SessionState) {
			return Fail(Invalid, "recovered requires exact terminal-session cleanup evidence and a free slot")
		}
	case "expired":
		if r.Code != "operation-expired" {
			return Fail(Invalid, "expired requires operation-expired")
		}
	case "operator-required":
		if !slices.Contains([]string{"cleanup-unproven", "fence-mismatch", "session-unavailable", "recovery-unavailable", "inspection-unavailable", "configuration-unavailable", "slot-occupied"}, r.Code) {
			return Fail(Invalid, "operator-required requires a safe unresolved-evidence code")
		}
	case "rejected":
		if slices.Contains([]string{"observations-collected", "session-recovered", "operation-expired"}, r.Code) {
			return Fail(Invalid, "rejected cannot assert completion or expiry")
		}
	}
	return nil
}

// ValidateAgentOperationOutcome binds a report to the immutable operation scope.
func ValidateAgentOperationOutcome(operation AgentOperation, req AgentOperationReport) error {
	if err := ValidateAgentOperationReport(req); err != nil {
		return err
	}
	if operation.Version != AgentOperationVersion || !slices.Contains([]string{"diagnostics", "session-recovery"}, operation.Kind) {
		return Fail(Invalid, "agent operation version or kind is unsupported")
	}
	if req.Result == nil {
		return nil
	}
	r := req.Result
	if operation.Kind == "diagnostics" {
		if req.State == "recovered" || slices.Contains([]string{"session-recovered", "session-live", "session-unavailable", "fence-mismatch", "cleanup-unproven", "recovery-unavailable"}, r.Code) {
			return Fail(Invalid, "diagnostics cannot report session recovery")
		}
		return nil
	}
	if req.State == "refreshed" || r.Code == "observations-collected" {
		return Fail(Invalid, "session recovery cannot report a diagnostic refresh")
	}
	if r.Observation != nil && r.Observation.SessionFence != nil {
		f := r.Observation.SessionFence
		if r.Code != "fence-mismatch" && (f.SessionID != operation.SessionID || f.Fingerprint != operation.ExpectedFence) {
			return Fail(Invalid, "recovery observation does not match the exact requested session fence")
		}
		if r.Code == "fence-mismatch" && f.SessionID == operation.SessionID && f.Fingerprint == operation.ExpectedFence {
			return Fail(Invalid, "fence-mismatch requires a different retained fence observation")
		}
		if r.Code == "cleanup-unproven" && f.CleanupProven {
			return Fail(Invalid, "cleanup-unproven contradicts the reported cleanup evidence")
		}
	}
	return nil
}

func ValidateAgentOperation(operation AgentOperation) error {
	if operation.Version != AgentOperationVersion || !ValidOperation(operation.ID) || operation.Revision < 1 || !ValidOperation(operation.RequesterID) || !ValidOperation(operation.TargetID) || operation.RequesterGeneration < 0 || operation.TargetGeneration < 0 {
		return Fail(Invalid, "agent operation identity, version or revision is unsupported")
	}
	if operation.CreatedAt <= 0 || operation.CreatedAt > maxHealthTimestamp-AgentOperationTTL || operation.ExpiresAt != operation.CreatedAt+AgentOperationTTL || operation.StartedAt < 0 || operation.StartedAt > maxHealthTimestamp || operation.FinishedAt < 0 || operation.FinishedAt > maxHealthTimestamp || operation.ServerObservedAt < 0 || operation.ServerObservedAt > maxHealthTimestamp {
		return Fail(Invalid, "agent operation has inconsistent bounded server clocks")
	}
	if err := CheckText(operation.Reason, "nonsecret agent operation reason", 8192, true); err != nil {
		return err
	}
	switch operation.Kind {
	case "diagnostics":
		if operation.SessionID != "" || operation.ExpectedFence != "" {
			return Fail(Invalid, "diagnostics cannot carry recovery authority")
		}
	case "session-recovery":
		if !ValidOperation(operation.SessionID) || !ValidHash(operation.ExpectedFence) {
			return Fail(Invalid, "session recovery requires an exact safe fence")
		}
	default:
		return Fail(Invalid, "agent operation kind is unsupported")
	}
	if operation.StartedAt != 0 && (operation.StartedAt < operation.CreatedAt || operation.StartedAt >= operation.ExpiresAt) {
		return Fail(Invalid, "agent operation must start before its fixed expiry")
	}
	if operation.State == "queued" || operation.State == "checking" {
		if operation.Result != nil || operation.FinishedAt != 0 || (operation.State == "queued" && operation.StartedAt != 0) || (operation.State == "checking" && operation.StartedAt == 0) {
			return Fail(Invalid, "pending agent operation cannot contain a final outcome")
		}
		return nil
	}
	if operation.FinishedAt < operation.CreatedAt || (operation.StartedAt != 0 && operation.FinishedAt < operation.StartedAt) {
		return Fail(Invalid, "agent operation completion precedes its request or start")
	}
	return ValidateAgentOperationOutcome(operation, AgentOperationReport{Command: Command{OperationID: operation.ID, ExpectedRevision: operation.Revision}, State: operation.State, Result: operation.Result})
}
