package foundation

import (
	"context"
	"testing"
)

func TestFoundationMaintenanceBlockedNoticePreservesRealCompletion(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	request := maintenanceRequest(t, f, true)
	if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, request); err != nil {
		t.Fatal(err)
	}
	message := executeMaintenance(t, f, queueMaintenance(t, f, request)[0])
	report := verifiedMaintenanceReport(message)
	report.UpgradeResult.ResumeSafe = false
	result, err := f.s.ReportSystemMessageAction(ctx, message.ID, report)
	if err != nil || result.Request.State != "completed" {
		t.Fatal("blocked automation restoration rewrote actual Foundation completion", err)
	}
	thread, err := f.s.MessageThread(ctx, f.founder.AgentID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, entry := range thread {
		if entry.SenderID == f.founder.InstanceID && entry.ReplyToID == message.ID &&
			entry.RequestUpdate != nil && entry.RequestUpdate.State == "completed" {
			updates++
			if entry.RequestUpdate.UpgradeResult == nil || entry.RequestUpdate.UpgradeResult.ResumeSafe {
				t.Fatal("blocked system notice changed the real native result")
			}
		}
	}
	record, err := readMaintenance(ctx, f.s.db, request.ID)
	if err != nil || record.State != "blocked" || updates != 2 {
		t.Fatal("independent resumption fence or typed Root notice was hidden", err)
	}
}
