package domain

import (
	"strings"
	"testing"
)

func inspectionValidationFixture() AgentInspection {
	h := healthValidationFixture()
	return AgentInspection{ObservedAt: h.ObservedAt, Health: &h, WorkSlot: "free", Provider: &ProviderObservation{Provider: "copilot", Version: "1.2.3", Authentication: "unknown", ObservedAt: h.ObservedAt, ProblemCode: Unavailable}}
}

func TestAgentOperationRequestValidation(t *testing.T) {
	valid := SessionRecoveryRequest{Command: Command{OperationID: "exact-operation"}, SessionID: "session_exact", ExpectedFence: Hash("exact durable fence"), Reason: "Inspect only the closed session fence"}
	if err := ValidateSessionRecoveryRequest(valid); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiagnosticRefreshRequest(DiagnosticRefreshRequest{Command: valid.Command, Reason: valid.Reason}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SessionRecoveryRequest){
		"operation":          func(r *SessionRecoveryRequest) { r.OperationID = "../private" },
		"revision":           func(r *SessionRecoveryRequest) { r.ExpectedRevision = 1 },
		"negative revision":  func(r *SessionRecoveryRequest) { r.ExpectedRevision = -1 },
		"empty session":      func(r *SessionRecoveryRequest) { r.SessionID = "" },
		"session path":       func(r *SessionRecoveryRequest) { r.SessionID = "/private/session" },
		"empty fence":        func(r *SessionRecoveryRequest) { r.ExpectedFence = "" },
		"noncanonical fence": func(r *SessionRecoveryRequest) { r.ExpectedFence = strings.ToUpper(r.ExpectedFence) },
		"short fence":        func(r *SessionRecoveryRequest) { r.ExpectedFence = "aa" },
		"reason":             func(r *SessionRecoveryRequest) { r.Reason = " \n\t" },
		"large reason":       func(r *SessionRecoveryRequest) { r.Reason = strings.Repeat("r", 8193) },
		"nul reason":         func(r *SessionRecoveryRequest) { r.Reason = "reason\x00" },
		"invalid UTF-8":      func(r *SessionRecoveryRequest) { r.Reason = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			change(&r)
			if err := ValidateSessionRecoveryRequest(r); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("invalid recovery request accepted", err)
			}
		})
	}
}

func TestAgentInspectionClosedSafeObservations(t *testing.T) {
	valid := inspectionValidationFixture()
	valid.SessionFence = &SessionFenceObservation{SessionID: "session_exact", Fingerprint: Hash("durable-fence"), CleanupProven: true, SessionState: "closed", RecoveryState: "recoverable"}
	if err := ValidateAgentInspection(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*AgentInspection){
		"time":             func(i *AgentInspection) { i.ObservedAt = 0 },
		"future time":      func(i *AgentInspection) { i.ObservedAt = 253402300800 },
		"slot":             func(i *AgentInspection) { i.WorkSlot = "private error" },
		"health raw phase": func(i *AgentInspection) { i.Health.Phase = "raw provider output" },
		"health raw error": func(i *AgentInspection) {
			i.Health.Problems = []HealthProblem{{Component: "control", Code: "token=secret", FirstSeenAt: 1, LastSeenAt: 1}}
		},
		"health newer than capture":     func(i *AgentInspection) { i.Health.ObservedAt++ },
		"provider path":                 func(i *AgentInspection) { i.Provider.Version = "/private/bin/provider" },
		"provider output":               func(i *AgentInspection) { i.Provider.Version = "1.2.3\nprivate" },
		"provider kind":                 func(i *AgentInspection) { i.Provider.Provider = "shell" },
		"provider auth":                 func(i *AgentInspection) { i.Provider.Authentication = "cookie=secret" },
		"provider error":                func(i *AgentInspection) { i.Provider.ProblemCode = "private output" },
		"failed probe healthy fallback": func(i *AgentInspection) { i.Provider.Authentication = "authenticated" },
		"provider future":               func(i *AgentInspection) { i.Provider.ObservedAt++ },
		"fence path":                    func(i *AgentInspection) { i.SessionFence.SessionID = "../private" },
		"fence hash":                    func(i *AgentInspection) { i.SessionFence.Fingerprint = "private output" },
		"fence session state":           func(i *AgentInspection) { i.SessionFence.SessionState = "raw error" },
		"fence recovery state":          func(i *AgentInspection) { i.SessionFence.RecoveryState = "acknowledge-stopped" },
		"live recoverable":              func(i *AgentInspection) { i.SessionFence.SessionState = "active" },
		"unproved recoverable":          func(i *AgentInspection) { i.SessionFence.CleanupProven = false },
		"unproved contradictory":        func(i *AgentInspection) { i.SessionFence.RecoveryState = "cleanup-unproven" },
	} {
		t.Run(name, func(t *testing.T) {
			i := valid
			h, p, f := *valid.Health, *valid.Provider, *valid.SessionFence
			i.Health, i.Provider, i.SessionFence = &h, &p, &f
			change(&i)
			if err := ValidateAgentInspection(i); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("unsafe inspection accepted", err)
			}
		})
	}
	for _, component := range []string{"configuration", "mode-control", "provider-update", "controller", "watch", "heartbeat", "connection", "session", "control"} {
		valid.Health.Problems = append(valid.Health.Problems, HealthProblem{Component: component, Code: Unavailable, FirstSeenAt: 900, LastSeenAt: 1000})
	}
	if MaxHealthProblems < 9 || ValidateAgentInspection(valid) != nil {
		t.Fatal("independent control-loop health component does not fit the safe problem budget")
	}
}

func TestAgentOperationReportShapeAndExactOutcome(t *testing.T) {
	i := inspectionValidationFixture()
	op := AgentOperation{Version: AgentOperationVersion, ID: "agent_operation_exact", Revision: 1, Kind: "diagnostics", RequesterID: "agent_founder", TargetID: "agent_target", State: "queued", Reason: "Fresh observation", CreatedAt: 900, ExpiresAt: 1800}
	if err := ValidateAgentOperation(op); err != nil {
		t.Fatal("generation zero business fixture rejected", err)
	}
	r := AgentOperationReport{Command: Command{OperationID: "report-exact", ExpectedRevision: 2}, State: "refreshed", Result: &AgentOperationResult{Code: "observations-collected", ModeAtCheck: "ai", Observation: &i}}
	if err := ValidateAgentOperationOutcome(op, r); err != nil {
		t.Fatal("a failed read-only probe must remain a valid captured observation", err)
	}
	for name, change := range map[string]func(*AgentOperationReport){
		"missing revision":     func(r *AgentOperationReport) { r.ExpectedRevision = 0 },
		"missing operation":    func(r *AgentOperationReport) { r.OperationID = "" },
		"queued result":        func(r *AgentOperationReport) { r.State = "queued" },
		"checking result":      func(r *AgentOperationReport) { r.State = "checking" },
		"raw result code":      func(r *AgentOperationReport) { r.Result.Code = "private stdout" },
		"wrong success code":   func(r *AgentOperationReport) { r.Result.Code = "session-recovered" },
		"raw mode":             func(r *AgentOperationReport) { r.Result.ModeAtCheck = "resume" },
		"implicit permission":  func(r *AgentOperationReport) { r.Result.AutomationMayResume = true },
		"missing observations": func(r *AgentOperationReport) { r.Result.Observation = nil },
	} {
		t.Run(name, func(t *testing.T) {
			report, result := r, *r.Result
			report.Result = &result
			change(&report)
			if err := ValidateAgentOperationOutcome(op, report); err == nil {
				t.Fatal("invalid diagnostic result accepted")
			}
		})
	}
	op.Kind, op.SessionID, op.ExpectedFence = "session-recovery", "session_exact", Hash("fence")
	i.SessionFence = &SessionFenceObservation{SessionID: op.SessionID, Fingerprint: op.ExpectedFence, CleanupProven: true, SessionState: "closed", RecoveryState: "recoverable"}
	r.State, r.Result.Code, r.Result.AutomationMayResume = "recovered", "session-recovered", true
	if err := ValidateAgentOperationOutcome(op, r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(){
		func() { i.SessionFence.Fingerprint = Hash("different-fence") },
		func() { i.SessionFence.SessionID = "session_foreign" },
		func() { i.SessionFence.CleanupProven = false },
		func() { i.WorkSlot = "occupied" },
		func() { r.Result.ModeAtCheck = "manual" },
	} {
		fence, slot, mode := *i.SessionFence, i.WorkSlot, r.Result.ModeAtCheck
		change()
		if err := ValidateAgentOperationOutcome(op, r); err == nil {
			t.Fatal("inexact or unproved session recovery accepted")
		}
		*i.SessionFence, i.WorkSlot, r.Result.ModeAtCheck = fence, slot, mode
	}
	op.StartedAt, op.FinishedAt, op.State, op.Result, op.Revision = 950, 1001, "recovered", r.Result, 3
	if err := ValidateAgentOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AgentOperation){
		func(o *AgentOperation) { o.Version++ },
		func(o *AgentOperation) { o.TargetGeneration = -1 },
		func(o *AgentOperation) { o.ExpiresAt++ },
		func(o *AgentOperation) { o.StartedAt = o.ExpiresAt },
		func(o *AgentOperation) { o.FinishedAt = o.CreatedAt - 1 },
		func(o *AgentOperation) { o.CreatedAt = 9223372036854775000 },
	} {
		copy := op
		change(&copy)
		if err := ValidateAgentOperation(copy); err == nil {
			t.Fatal("invalid stored operation accepted", copy)
		}
	}
}
