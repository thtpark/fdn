package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFoundationMaintenanceFailureNoticePreservesTypedMailboxReference(t *testing.T) {
	for _, state := range []string{"declined", "deferred", "failed"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			f := newSecurityFixture(t)
			target, _, _ := f.register(t, "Agent1", 4101)
			request := maintenanceRequest(t, f, true, target)
			if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, request); err != nil {
				t.Fatal(err)
			}
			message := queueMaintenance(t, f, request)[0]
			if state == "failed" {
				message = executeMaintenance(t, f, message)
			}
			report := domain.MessageActionReport{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision},
				State:   state, Reason: "The Foundation native receiver reports its exact refusal/failure.",
			}
			result, err := f.s.ReportSystemMessageAction(ctx, message.ID, report)
			if err != nil || result.Request == nil || result.Request.State != state {
				t.Fatal("Foundation maintenance notice violated the immutable mailbox-party contract", err)
			}
			thread, err := f.s.MessageThread(ctx, f.founder.AgentID, message.ID)
			if err != nil {
				t.Fatal(err)
			}
			updates := 0
			for _, entry := range thread {
				if entry.SenderID == f.founder.InstanceID {
					if entry.Kind != "request-update" || entry.Origin != "service" ||
						entry.RequestUpdate == nil || entry.RequestUpdate.RequestID != message.ID {
						t.Fatal("Foundation system notice lost its exact typed source reference")
					}
					if entry.RequestUpdate.State == state {
						updates++
					}
				}

			}
			if updates < 2 {
				t.Fatal("native decision and blocked-maintenance notice were not both retained")
			}
			replayed, err := f.s.ReportSystemMessageAction(ctx, message.ID, report)
			if err != nil || replayed.Revision != result.Revision {
				t.Fatal("exact pending native report could not be reconciled", err)
			}
			again, err := f.s.MessageThread(ctx, f.founder.AgentID, message.ID)
			if err != nil || len(again) != len(thread) {
				t.Fatal("native report replay duplicated notices", err)
			}
			record, err := readMaintenance(ctx, f.s.db, request.ID)
			actor, modeErr := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
			if err != nil || modeErr != nil || record.State != "blocked" || actor.Mode != "manual" {
				t.Fatal("notice reconciliation hid failure or changed the authoritative pause")
			}
		})
	}
}
