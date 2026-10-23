package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentOperationRawAPICaptureClockPreservesSkewedWorkerUTC(t *testing.T) {
	for _, skew := range []int64{-3600, 3600} {
		t.Run(fmt.Sprintf("worker-skew-%d", skew), func(t *testing.T) {
			f := newAuthenticationAPIFixture(t)
			envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/agents/"+f.target.ID+"/diagnostics", domain.DiagnosticRefreshRequest{Command: domain.Command{OperationID: "clock-skew-refresh"}, Reason: "Inspect without assuming target UTC synchronization"})
			operation := apiCheckOperation(t, f, apiAgentOperation(t, envelope, status))
			if operation.ServerObservedAt < operation.StartedAt {
				t.Fatal("checking response omitted its authenticated server clock", operation)
			}
			report := apiOperationReport(operation)
			report.Result.CapturedAt = operation.ServerObservedAt + 2
			report.Result.Observation.ObservedAt = operation.ServerObservedAt + skew
			report.Result.Observation.Provider.ObservedAt = report.Result.Observation.ObservedAt
			envelope, status = f.request(t, f.target, http.MethodPost, "/v1/agent-operations/"+operation.ID+"/result", report)
			final := apiAgentOperation(t, envelope, status)
			if final.State != "refreshed" || final.Result.CapturedAt != report.Result.CapturedAt || final.Result.Observation.ObservedAt != report.Result.Observation.ObservedAt || final.Result.Observation.Provider.ObservedAt != report.Result.Observation.Provider.ObservedAt {
				t.Fatal("raw API rejected or clamped independent worker UTC observations", final)
			}
		})
	}
}
