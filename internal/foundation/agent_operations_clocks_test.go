package foundation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentOperationResultsKeepTargetUTCSeparateFromServerCaptureClock(t *testing.T) {
	for _, kind := range []string{"diagnostics", "session-recovery"} {
		for _, skew := range []int64{-3600, 3600} {
			for _, late := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/worker-skew-%d/late-%t", kind, skew, late), func(t *testing.T) {
					f := newFixture(t)
					ctx := context.Background()
					var operation domain.AgentOperation
					if kind == "diagnostics" {
						operation, _ = requestDiagnosticOperation(t, f)
					} else {
						session := seedOperationSession(t, f, f.worker.ID, "closed", f.now.Unix()+600)
						operation = requestRecoveryOperation(t, f, session)
					}
					operation, _ = checkingAgentOperation(t, f, operation)
					if operation.ServerObservedAt != f.now.Unix() {
						t.Fatal("authenticated checking response lacks its server clock", operation)
					}
					report := diagnosticOperationReport(f, operation)
					if kind == "session-recovery" {
						report = recoveryOperationReport(f, operation)
					}
					captureBound := operation.ServerObservedAt + 2
					report.Result.CapturedAt = captureBound
					workerUTC := f.now.Unix() + skew
					observation := report.Result.Observation
					observation.ObservedAt, observation.Health.ObservedAt, observation.Provider.ObservedAt = workerUTC, workerUTC, workerUTC
					input, err := json.Marshal(report)
					if err != nil {
						t.Fatal(err)
					}
					f.now = f.now.Add(5 * time.Second)
					if late {
						f.now = time.Unix(operation.ExpiresAt+60, 0)
						projected, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
						if err != nil || projected.State != "expired" {
							t.Fatal("deadline did not stop dispatch before late evidence arrived", projected, err)
						}
					}
					final, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
					if err != nil || final.State != report.State || final.Result == nil || final.Result.CapturedAt != captureBound || final.ServerObservedAt != f.now.Unix() || final.FinishedAt != f.now.Unix() {
						t.Fatal("valid operation outcome failed solely because of target UTC skew", final, err)
					}
					got := final.Result.Observation
					if got.ObservedAt != workerUTC || got.Health.ObservedAt != workerUTC || got.Provider.ObservedAt != workerUTC {
						t.Fatal("server clamped or rewrote target UTC evidence", got)
					}
					if final.Result.AutomationMayResume != (kind == "session-recovery" && !late) {
						t.Fatal("late evidence conferred fresh automation eligibility", final.Result)
					}
					raw, err := getAgentOperation(ctx, f.s.db, operation.ID)
					if err != nil || raw.ServerObservedAt != 0 || raw.Result.CapturedAt != report.Result.CapturedAt || raw.Result.AutomationMayResume != report.Result.AutomationMayResume {
						t.Fatal("response projection replaced immutable capture/eligibility evidence", raw, err)
					}
					f.now = f.now.Add(time.Second)
					replayed, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
					if err != nil || replayed.State != final.State || replayed.Result.CapturedAt != captureBound || replayed.Result.Observation.ObservedAt != workerUTC || replayed.ServerObservedAt != f.now.Unix() {
						t.Fatal("lost-ack replay needed a changed report or fabricated a new capture", replayed, err)
					}
					after, err := json.Marshal(report)
					if err != nil || !bytes.Equal(input, after) {
						t.Fatal("server processing mutated the retained report payload", err)
					}
					if late && kind == "session-recovery" {
						f.now = time.Unix(operation.StartedAt+5, 0)
						projected, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
						if err != nil || projected.Result.AutomationMayResume {
							t.Fatal("a server-clock rollback revived late-result eligibility", projected, err)
						}
					}
					changed := report
					result := *report.Result
					changed.Result = &result
					changed.Result.CapturedAt++
					_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, changed)
					assertCode(t, err, domain.Conflict)
				})
			}
		}
	}
}

func TestAgentOperationOnTimeReceiptDoesNotRequireTargetServerClockEstimate(t *testing.T) {
	for _, skew := range []int64{-3600, 3600} {
		t.Run(fmt.Sprintf("worker-skew-%d", skew), func(t *testing.T) {
			f := newFixture(t)
			operation, _ := requestDiagnosticOperation(t, f)
			operation, _ = checkingAgentOperation(t, f, operation)
			report := diagnosticOperationReport(f, operation)
			report.Result.CapturedAt = 0
			report.Result.Observation = operationInspection(f.now.Unix()+skew, "ai")
			final, err := f.s.ReportAgentOperation(context.Background(), f.worker.ID, operation.ID, report)
			if err != nil || final.State != "refreshed" || final.FinishedAt >= final.ExpiresAt || final.Result.CapturedAt != 0 || final.Result.Observation.ObservedAt != report.Result.Observation.ObservedAt {
				t.Fatal("on-time server receipt depended on a worker UTC deadline comparison", final, err)
			}
		})
	}
}

func TestAgentOperationLateEvidenceNeedsExplicitCaptureBoundNotObservationUTC(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	operation, _ := requestDiagnosticOperation(t, f)
	operation, _ = checkingAgentOperation(t, f, operation)
	report := diagnosticOperationReport(f, operation)
	f.now = time.Unix(operation.ExpiresAt+1, 0)
	for _, capture := range []int64{0, operation.StartedAt - 1, operation.ExpiresAt, operation.ExpiresAt + 1} {
		report.Command.OperationID = domain.NewID("late_report")
		report.Result.CapturedAt = capture
		_, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
		assertCode(t, err, domain.Conflict)
	}
	report.Command.OperationID = domain.NewID("valid_late_report")
	report.Result.CapturedAt = operation.ExpiresAt - 1
	final, err := f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, report)
	if err != nil || final.State != "refreshed" || final.Result.AutomationMayResume {
		t.Fatal("bounded retained evidence could not be recorded after expiry", final, err)
	}
}

func TestAgentOperationServerClockIsDynamicAndNeverPersistsOrRenews(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	operation, request := requestDiagnosticOperation(t, f)
	var before []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", operation.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(12 * time.Second)
	for _, read := range []func() (domain.AgentOperation, error){
		func() (domain.AgentOperation, error) { return f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID) },
		func() (domain.AgentOperation, error) {
			return f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, request)
		},
	} {
		current, err := read()
		if err != nil || current.ServerObservedAt != f.now.Unix() || current.ID != operation.ID || current.CreatedAt != operation.CreatedAt || current.ExpiresAt != operation.ExpiresAt || current.Revision != operation.Revision {
			t.Fatal("fresh authenticated server clock changed the immutable operation", current, err)
		}
	}
	page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ServerObservedAt != f.now.Unix() {
		t.Fatal("pending collection did not project its server clock", page, err)
	}
	var after []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", operation.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("dynamic server clock was stored as mutable control evidence", err)
	}
}
