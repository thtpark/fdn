package domain

import "testing"

func TestAgentResetStrictRequestAndOutcome(t *testing.T) {
	req := AgentResetRequest{Command: Command{OperationID: "reset_exact"}, Reason: "Reset owned runtime"}
	if err := ValidateAgentResetRequest(req); err != nil {
		t.Fatal(err)
	}
	req.ExpectedRevision = 1
	if err := ValidateAgentResetRequest(req); err == nil {
		t.Fatal("reset accepted an unrelated optimistic entity revision")
	}
	op := AgentOperation{Version: AgentOperationVersion, Kind: "runtime-reset"}
	report := AgentOperationReport{Command: Command{OperationID: "reset_result", ExpectedRevision: 2}, State: "reset",
		Result: &AgentOperationResult{Code: "runtime-reset", ModeAtCheck: "manual", CapturedAt: 1800000000, Observation: &AgentInspection{ObservedAt: 1800000000, WorkSlot: "free"}}}
	if err := ValidateAgentOperationOutcome(op, report); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"diagnostics", "session-recovery"} {
		op.Kind = kind
		if err := ValidateAgentOperationOutcome(op, report); err == nil {
			t.Fatal("another control reported a reset", kind)
		}
	}
	op.Kind = "runtime-reset"
	report.Result.AutomationMayResume = true
	if err := ValidateAgentOperationOutcome(op, report); err == nil {
		t.Fatal("manual reset promised automatic execution")
	}
	report.Result.AutomationMayResume = false
	report.Result.Observation.WorkSlot = "occupied"
	if err := ValidateAgentOperationOutcome(op, report); err == nil {
		t.Fatal("reset succeeded with an occupied slot")
	}
	report.Result.Observation.WorkSlot = "free"
	report.Result.Observation.SessionFence = &SessionFenceObservation{SessionID: "session_retained", Fingerprint: Hash("retained"), SessionState: "closed", RecoveryState: "cleanup-unproven"}
	if err := ValidateAgentOperationOutcome(op, report); err == nil {
		t.Fatal("reset succeeded with unproven session cleanup")
	}
}
