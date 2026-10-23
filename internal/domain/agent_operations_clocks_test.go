package domain

import "testing"

func TestAgentOperationClockBoundsDoNotRewriteOrCompareWorkerUTC(t *testing.T) {
	const serverNow int64 = 1700000000
	for _, skew := range []int64{-3600, 3600} {
		i := inspectionValidationFixture()
		i.ObservedAt = serverNow + skew
		i.Health.ObservedAt, i.Provider.ObservedAt = i.ObservedAt, i.ObservedAt
		r := AgentOperationReport{Command: Command{OperationID: "capture-report", ExpectedRevision: 2}, State: "refreshed", Result: &AgentOperationResult{Code: "observations-collected", ModeAtCheck: "ai", CapturedAt: serverNow + 2, Observation: &i}}
		o := AgentOperation{Version: AgentOperationVersion, ID: "agent_operation_capture", Revision: 3, Kind: "diagnostics", RequesterID: "agent_founder", TargetID: "agent_target", State: "refreshed", Reason: "Capture bounded observations", CreatedAt: serverNow, ExpiresAt: serverNow + AgentOperationTTL, StartedAt: serverNow, FinishedAt: serverNow + 4, ServerObservedAt: serverNow + 4, Result: r.Result}
		if err := ValidateAgentOperationReport(r); err != nil {
			t.Fatal("worker UTC was compared with a server capture estimate", skew, err)
		}
		if err := ValidateAgentOperation(o); err != nil {
			t.Fatal("worker UTC was compared with server lifecycle clocks", skew, err)
		}
		if i.ObservedAt != serverNow+skew || i.Health.ObservedAt != i.ObservedAt || i.Provider.ObservedAt != i.ObservedAt || r.Result.CapturedAt != serverNow+2 {
			t.Fatal("validating capture clocks rewrote independent observations")
		}
		for _, bad := range []int64{-1, maxHealthTimestamp + 1} {
			copy := *r.Result
			copy.CapturedAt = bad
			r.Result = &copy
			if err := ValidateAgentOperationReport(r); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("unbounded capture clock accepted", bad, err)
			}
			current := o
			current.ServerObservedAt = bad
			if err := ValidateAgentOperation(current); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("unbounded projected server clock accepted", bad, err)
			}
		}
	}
}
