package foundation

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func maintenanceMode(t *testing.T, f *securityFixture, id, mode, reason string) domain.Agent {
	t.Helper()
	ctx := context.Background()
	current, err := f.s.Agent(ctx, f.founder.AgentID, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.s.SetMode(ctx, f.founder.AgentID, id, domain.ModeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: current.Revision}, Mode: mode, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func maintenanceRequest(t *testing.T, f *securityFixture, server bool, targets ...domain.AgentIdentity) domain.UpgradeMaintenanceRequest {
	t.Helper()
	req := domain.UpgradeMaintenanceRequest{Command: op(), ID: domain.NewID("upgrade")}
	if server {
		proposal := upgradeFixtureRequest(t, f, f.founder).Proposal
		proposal.Challenge.Role, proposal.Challenge.TargetID, proposal.Challenge.Generation = "foundation", f.founder.InstanceID, 1
		req.Proposals = append(req.Proposals, proposal)
	}
	for _, target := range targets {
		req.Proposals = append(req.Proposals, upgradeFixtureRequest(t, f, target).Proposal)
	}
	for i := range req.Proposals {
		req.Proposals[i].Challenge.ID = req.ID + "." + strconv.Itoa(i)
	}
	return req
}

func queueMaintenance(t *testing.T, f *securityFixture, req domain.UpgradeMaintenanceRequest) []domain.Message {
	t.Helper()
	ctx := context.Background()
	messages := []domain.Message{}
	dependencies := []string{}
	for _, proposal := range req.Proposals {
		permit, err := f.s.AuthorizeUpgrade(ctx, f.founder.AgentID, domain.UpgradeAuthorizationRequest{
			Command: op(), Proposal: proposal, MaintenanceID: req.ID}, func(p domain.UpgradePermit) (string, error) {
			return "fixture-signature-" + p.Proposal.Challenge.ID, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		message, err := f.s.SendMessage(ctx, f.founder.AgentID, domain.MessageRequest{
			Command: op(), RecipientID: proposal.Challenge.TargetID, Body: "Exact temporary native maintenance.",
			Origin: "human", Kind: "request", ResponseRequest: true,
			Request: &domain.MessageAction{Kind: "upgrade", Upgrade: &permit, Dependencies: slices.Clone(dependencies)}})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
		dependencies = append(dependencies, message.ID)
	}
	return messages
}

func maintenanceReport(t *testing.T, f *securityFixture, message domain.Message, req domain.MessageActionReport) domain.Message {
	t.Helper()
	var result domain.Message
	var err error
	if message.RecipientID == f.founder.InstanceID {
		result, err = f.s.ReportSystemMessageAction(context.Background(), message.ID, req)
	} else {
		result, err = f.s.ReportMessageAction(context.Background(), message.RecipientID, message.ID, req)
	}
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func executeMaintenance(t *testing.T, f *securityFixture, message domain.Message) domain.Message {
	t.Helper()
	for _, state := range []string{"accepted", "executing"} {
		message = maintenanceReport(t, f, message, domain.MessageActionReport{
			Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision},
			State:   state, Reason: "Target reports its exact native " + state + " admission."})
	}
	return message
}

func verifiedMaintenanceReport(message domain.Message) domain.MessageActionReport {
	permit := message.Request.Action.Upgrade
	return domain.MessageActionReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision},
		State: "completed", Reason: "Target verified the installed native source and encrypted backup.",
		UpgradeResult: &domain.UpgradeResult{ChallengeID: permit.Proposal.Challenge.ID, PermitSHA256: domain.Hash(permit.Signature),
			Version: permit.Proposal.Release.Version, Commit: permit.Proposal.Release.Commit, BackupSHA256: domain.Hash("verified fixture backup"), ResumeSafe: true}}
}

func TestTemporaryMaintenanceRestoresOnlyPriorAIAfterWholeExactVerifiedRollout(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	first, _, _ := f.register(t, "Agent1", 4101)
	manual, _, _ := f.register(t, "Agent2", 4102)
	unmodified, _, _ := f.register(t, "Agent3", 4103)
	priorManual := maintenanceMode(t, f, manual.AgentID, "manual", "Founder keeps this computer intentionally manual")
	maintenanceMode(t, f, f.founder.AgentID, "ai", "Founder enables AI")
	req := maintenanceRequest(t, f, true, first, manual)
	paused, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req)
	if err != nil || paused.State != "paused" || len(paused.PausedModes) != 3 {
		t.Fatal("temporary pause did not retain only the three actual prior AI modes", paused, err)
	}
	messages := queueMaintenance(t, f, req)
	var final domain.Message
	var finalReport domain.MessageActionReport
	for i, message := range messages {
		message = executeMaintenance(t, f, message)
		report := verifiedMaintenanceReport(message)
		final = maintenanceReport(t, f, message, report)
		finalReport = report
		if i < len(messages)-1 {
			for _, mode := range paused.PausedModes {
				agent, err := f.s.Agent(ctx, f.founder.AgentID, mode.AgentID)
				if err != nil || agent.Mode != "manual" || agent.Revision != mode.Revision {
					t.Fatal("partial rollout prematurely restored AI intent", agent, err)
				}
			}
		}
	}
	for _, id := range []string{first.AgentID, unmodified.AgentID, f.founder.AgentID} {
		agent, err := f.s.Agent(ctx, f.founder.AgentID, id)
		pause := paused.PausedModes[slices.IndexFunc(paused.PausedModes, func(p domain.UpgradePausedMode) bool { return p.AgentID == id })]
		if err != nil || agent.Mode != "ai" || agent.Revision != pause.Revision+1 || agent.ModeRequestedBy != f.founder.AgentID {
			t.Fatal("verified temporary pause did not restore its exact prior AI intent", agent, err)
		}
	}
	current, err := f.s.Agent(ctx, f.founder.AgentID, manual.AgentID)
	if err != nil || current.Mode != "manual" || current.Revision != priorManual.Revision || current.ModeReason != priorManual.ModeReason {
		t.Fatal("preexisting Founder manual intent was overwritten", current, err)
	}
	record, err := readMaintenance(ctx, f.s.db, req.ID)
	if err != nil || record.State != "completed" || record.Reason == "" || final.Request.UpgradeResult == nil {
		t.Fatal("verified restoration/result provenance was not retained", record, err)
	}
	f.now = f.now.Add(20 * time.Minute)
	replayed, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req)
	if err != nil || !reflect.DeepEqual(replayed, paused) {
		t.Fatal("exact pause replay changed the original baseline or renewed expiry", replayed, err)
	}
	maintenanceReport(t, f, final, finalReport)
	current, err = f.s.Agent(ctx, f.founder.AgentID, first.AgentID)
	if err != nil || current.Mode != "ai" {
		t.Fatal("replaying completed maintenance paused the worker again", current, err)
	}
	req.Command = op()
	_, err = f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req)
	assertCode(t, err, domain.Conflict)
	inbox, err := f.s.Inbox(ctx, f.founder.AgentID, "", 200, false)
	restorationNotices := 0
	for _, notice := range inbox.Items {
		if strings.Contains(notice.Body, "Temporary upgrade "+req.ID+" completed:") {
			restorationNotices++
			if notice.ResponseRequest {
				t.Fatal("maintenance restoration notification started inference")
			}
		}
	}
	if err != nil || restorationNotices != 1 {
		t.Fatal("exact completion replay duplicated/missed the non-inference restoration notice", restorationNotices, err)
	}
}

func TestTemporaryMaintenanceNeverOverridesNewerFounderModeRequests(t *testing.T) {
	for _, mode := range []string{"manual", "ai"} {
		t.Run(mode, func(t *testing.T) {
			f := newSecurityFixture(t)
			target, _, _ := f.register(t, "Agent1", 4101)
			req := maintenanceRequest(t, f, false, target)
			if _, err := f.s.PauseUpgradeMaintenance(context.Background(), f.founder.AgentID, req); err != nil {
				t.Fatal(err)
			}
			message := executeMaintenance(t, f, queueMaintenance(t, f, req)[0])
			newer := maintenanceMode(t, f, target.AgentID, mode, "Newer explicit Founder decision must be preserved")
			maintenanceReport(t, f, message, verifiedMaintenanceReport(message))
			current, err := f.s.Agent(context.Background(), f.founder.AgentID, target.AgentID)
			if err != nil || current.Mode != newer.Mode || current.Revision != newer.Revision || current.ModeReason != newer.ModeReason {
				t.Fatal("temporary maintenance overrode a newer Founder mode request", current, err)
			}
		})
	}
}

func TestTemporaryMaintenanceKeepsRealCompletionWhenResumptionIsBlocked(t *testing.T) {
	for _, block := range []string{"native-fence", "text-only", "live-session", "changed-generation", "other-maintenance"} {
		t.Run(block, func(t *testing.T) {
			f := newSecurityFixture(t)
			ctx := context.Background()
			target, _, _ := f.register(t, "Agent1", 4101)
			unmodified, _, _ := f.register(t, "Agent2", 4102)
			req := maintenanceRequest(t, f, true, target)
			paused, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req)
			if err != nil {
				t.Fatal(err)
			}
			messages := queueMaintenance(t, f, req)
			server := executeMaintenance(t, f, messages[0])
			maintenanceReport(t, f, server, verifiedMaintenanceReport(server))
			message := executeMaintenance(t, f, messages[1])
			report := verifiedMaintenanceReport(message)
			switch block {
			case "native-fence":
				report.UpgradeResult.ResumeSafe = false
			case "text-only":
				report.UpgradeResult = nil
			case "live-session":
				session := domain.Session{ID: domain.NewID("session"), RequesterID: f.founder.AgentID,
					TargetID: target.AgentID, State: "active", ExpiresAt: f.now.Unix() + 900}
				data, err := encode(session)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.db.ExecContext(ctx, "INSERT INTO sessions(id,data,requester_id,target_id,state,expires_at) VALUES(?,?,?,?,?,?)",
					session.ID, data, session.RequesterID, session.TargetID, session.State, session.ExpiresAt); err != nil {
					t.Fatal(err)
				}
			case "changed-generation":
				if _, err := f.s.db.ExecContext(ctx, "UPDATE agent_identities SET data=json_set(data,'$.generation',json_extract(data,'$.generation')+1) WHERE id=?", unmodified.AgentID); err != nil {
					t.Fatal(err)
				}
			case "other-maintenance":
				other := maintenanceRequest(t, f, false, target)
				if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, other); err != nil {
					t.Fatal(err)
				}
			}
			completed := maintenanceReport(t, f, message, report)
			if completed.Request.State != "completed" || completed.State != "handled" {
				t.Fatal("resumption safety rolled back a legitimate completed installation report", completed)
			}
			record, err := readMaintenance(ctx, f.s.db, req.ID)
			if err != nil || record.State != "blocked" || record.Reason == "" {
				t.Fatal("blocked resumption was silently hidden", record, err)
			}
			for _, pause := range paused.PausedModes {
				current, err := f.s.Agent(ctx, f.founder.AgentID, pause.AgentID)
				if err != nil || current.Mode != "manual" || current.Revision != pause.Revision {
					t.Fatal("resumption preflight partially restored modes before finding a fence", current, err)
				}
			}
			thread, err := f.s.MessageThread(ctx, f.founder.AgentID, message.ID)
			blockNotices := 0
			for _, notice := range thread {
				if strings.Contains(notice.Body, "Temporary upgrade "+req.ID+" blocked:") {
					blockNotices++
					if notice.ResponseRequest || notice.Request != nil || notice.RequestUpdate != nil {
						t.Fatal("blocked restoration was forged as a request failure or inference request", notice)
					}
				}
			}
			if err != nil || blockNotices != 1 {
				t.Fatal("Founder did not receive one explicit blocked-restoration notice", blockNotices, err)
			}
		})
	}
}

func TestTemporaryMaintenanceRefusalAndFailureNeverResumeModes(t *testing.T) {
	for _, state := range []string{"deferred", "declined", "failed"} {
		t.Run(state, func(t *testing.T) {
			f := newSecurityFixture(t)
			ctx := context.Background()
			target, _, _ := f.register(t, "Agent1", 4101)
			req := maintenanceRequest(t, f, false, target)
			if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req); err != nil {
				t.Fatal(err)
			}
			message := queueMaintenance(t, f, req)[0]
			if state == "failed" {
				message = executeMaintenance(t, f, message)
			}
			maintenanceReport(t, f, message, domain.MessageActionReport{
				Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision},
				State:   state, Reason: "Target explicitly reports its actual safe refusal/failure."})
			record, err := readMaintenance(ctx, f.s.db, req.ID)
			current, modeErr := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
			if err != nil || modeErr != nil || record.State != "blocked" || current.Mode != "manual" {
				t.Fatal("failed/refused upgrade resumed automation or hid its blocker", record, current, err, modeErr)
			}
		})
	}
}

func TestTemporaryMaintenanceRejectsForeignOrChangedProposalsBeforePausing(t *testing.T) {
	for _, invalid := range []string{"non-founder", "empty", "foreign-instance", "expired", "future-expiry", "changed-generation", "mixed-source", "server-last"} {
		t.Run(invalid, func(t *testing.T) {
			f := newSecurityFixture(t)
			ctx := context.Background()
			target, _, _ := f.register(t, "Agent1", 4101)
			req := maintenanceRequest(t, f, false, target)
			actor := f.founder.AgentID
			switch invalid {
			case "non-founder":
				actor = target.AgentID
			case "empty":
				req.Proposals = nil
			case "foreign-instance":
				req.Proposals[0].Challenge.InstanceID = "instance_foreign"
			case "expired":
				req.Proposals[0].Challenge.ExpiresAt = f.now.Unix()
			case "future-expiry":
				req.Proposals[0].Challenge.ExpiresAt = f.now.Unix() + 901
			case "changed-generation":
				req.Proposals[0].Challenge.Generation++
			case "mixed-source":
				other, _, _ := f.register(t, "Agent2", 4102)
				proposal := maintenanceRequest(t, f, false, other).Proposals[0]
				proposal.Challenge.ID, proposal.Release.Commit = req.ID+".1", strings.Repeat("d", 40)
				req.Proposals = append(req.Proposals, proposal)
			case "server-last":
				proposal := maintenanceRequest(t, f, true).Proposals[0]
				proposal.Challenge.ID = req.ID + ".1"
				req.Proposals = append(req.Proposals, proposal)
			}
			if _, err := f.s.PauseUpgradeMaintenance(ctx, actor, req); err == nil {
				t.Fatal("invalid/current-authority mismatch changed the mode baseline")
			}
			current, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
			if err != nil || current.Mode != "ai" {
				t.Fatal("invalid maintenance request left an unintended permanent pause", current, err)
			}
		})
	}
}

func TestTemporaryMaintenanceStorageErrorsRemainExplicitAndAtomic(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	target, _, _ := f.register(t, "Agent1", 4101)
	req := maintenanceRequest(t, f, false, target)
	if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req); err != nil {
		t.Fatal(err)
	}
	message := executeMaintenance(t, f, queueMaintenance(t, f, req)[0])
	if _, err := f.s.db.ExecContext(ctx, `CREATE TRIGGER reject_fixture_resume BEFORE UPDATE ON agents
WHEN json_extract(NEW.data,'$.mode')='ai' BEGIN SELECT RAISE(ABORT,'fixture resume storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.ReportMessageAction(ctx, target.AgentID, message.ID, verifiedMaintenanceReport(message))
	if err == nil {
		t.Fatal("storage failure became a success-shaped maintenance result")
	}
	current, err := f.s.Message(ctx, f.founder.AgentID, message.ID)
	record, recordErr := readMaintenance(ctx, f.s.db, req.ID)
	if err != nil || recordErr != nil || current.Request.State != "executing" || record.State != "paused" {
		t.Fatal("failed transaction partially committed a completion/restoration", current, record, err, recordErr)
	}
}

func TestVerifiedMaintenanceResultCannotChangeItsExactNativeProvenance(t *testing.T) {
	for _, field := range []string{"challenge", "permit", "version", "commit", "backup", "premature"} {
		t.Run(field, func(t *testing.T) {
			f := newSecurityFixture(t)
			ctx := context.Background()
			target, _, _ := f.register(t, "Agent1", 4101)
			req := maintenanceRequest(t, f, false, target)
			if _, err := f.s.PauseUpgradeMaintenance(ctx, f.founder.AgentID, req); err != nil {
				t.Fatal(err)
			}
			message := queueMaintenance(t, f, req)[0]
			if field != "premature" {
				message = executeMaintenance(t, f, message)
			}
			report := verifiedMaintenanceReport(message)
			switch field {
			case "challenge":
				report.UpgradeResult.ChallengeID += ".other"
			case "permit":
				report.UpgradeResult.PermitSHA256 = domain.Hash("another permit")
			case "version":
				report.UpgradeResult.Version = "0.3.4"
			case "commit":
				report.UpgradeResult.Commit = strings.Repeat("c", 40)
			case "backup":
				report.UpgradeResult.BackupSHA256 = "unverified"
			}
			if _, err := f.s.ReportMessageAction(ctx, target.AgentID, message.ID, report); err == nil {
				t.Fatal("changed/unadmitted native result resumed AI")
			}
		})
	}
}
