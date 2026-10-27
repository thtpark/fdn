package foundation

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAssistanceHelpRoutingPrivacyAndImmutableReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project, err := f.s.Project(ctx, f.worker.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.other.ID, f.founder.ID} {
		project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: id})
		if err != nil {
			t.Fatal(err)
		}
	}
	publishAssistanceScreens(t, f, assistanceScreens())
	worker, err := f.s.Me(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.AssistanceHelpRequest{Command: op(), Mode: "vnc", Purpose: "authentication", Provider: "copilot", ProjectID: project.ID, TaskID: task.ID, ScreenID: "screen-1", ApplicationID: "browser", Reason: "Human sign-in is required on the selected display"}
	help, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || !strings.HasPrefix(help.ID, "help_") || help.AgentID != f.worker.ID || len(help.Messages) != 2 || help.ExpiresAt != f.now.Unix()+900 {
		t.Fatal("Founder/current delegate routing was not atomic and deduplicated", help, err)
	}
	var recipients []string
	for _, message := range help.Messages {
		recipients = append(recipients, message.RecipientID)
		if message.SenderID != f.worker.ID || !assistanceHelpMessage(message) || !message.Urgent || message.ResponseRequest || message.ExpiresAt != help.ExpiresAt || message.CreatedAt != f.now.Unix() || message.State != "pending" || message.Revision != 1 {
			t.Fatal("help message lost protected service-owned fields", message)
		}
		for _, text := range []string{"selected screen screen-1", "application browser", "agent-cli assistance grant --input -", "agent-cli assistance open GRANT_ID --ttl 300 --provider copilot", "SSH-forwarded localhost", "900 seconds", "starts no application, login, consent or inference"} {
			if !strings.Contains(message.Body, text) {
				t.Fatal("help message omitted exact scope, supported action or safety boundary", text, message.Body)
			}
		}
		var grant domain.AssistanceGrantRequest
		for _, line := range strings.Split(message.Body, "\n") {
			if strings.HasPrefix(line, "{\"operation_id\"") {
				if err := json.Unmarshal([]byte(line), &grant); err != nil {
					t.Fatal("help's grant action was not valid typed JSON", err)
				}
			}
		}
		if !domain.ValidOperation(grant.OperationID) || grant.RequesterID != f.founder.ID || grant.TargetID != f.worker.ID || grant.ProjectID != project.ID || grant.Mode != req.Mode || grant.Purpose != req.Purpose || grant.Provider != req.Provider || grant.ScreenID != req.ScreenID || grant.ApplicationID != req.ApplicationID || grant.DesktopFallback {
			t.Fatal("help's exact grant proposal changed requested scope or identity", grant)
		}
		for _, actor := range []string{f.worker.ID, message.RecipientID} {
			if read, err := f.s.Message(ctx, actor, message.ID); err != nil || !reflect.DeepEqual(read, message) {
				t.Fatal("private party could not read help message", read, err)
			}
		}
		outsider := f.other.ID
		if message.RecipientID == f.other.ID {
			outsider = f.founder.ID
		}
		_, err := f.s.Message(ctx, outsider, message.ID)
		assertCode(t, err, domain.Denied)
	}
	wantRecipients := []string{f.founder.ID, f.other.ID}
	slices.Sort(wantRecipients)
	if !reflect.DeepEqual(recipients, wantRecipients) {
		t.Fatal("help recipients included unrelated parties or duplicate Founder", recipients)
	}
	for _, table := range []string{"assistance_grants", "sessions", "runs"} {
		var count int
		if err := f.s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("asking for assistance implicitly authorized access or started work", table, count, err)
		}
	}
	if current, err := f.s.Me(ctx, f.worker.ID); err != nil || !reflect.DeepEqual(current, worker) {
		t.Fatal("help changed worker mode, identity or display inventory", current, err)
	}
	if current, err := f.s.Task(ctx, f.worker.ID, task.ID); err != nil || !reflect.DeepEqual(current, task) {
		t.Fatal("help changed task authority", current, err)
	}
	if current, err := f.s.Project(ctx, f.worker.ID, project.ID); err != nil || !reflect.DeepEqual(current, project) {
		t.Fatal("help changed project authority", current, err)
	}
	_, err = f.s.Histories(ctx, f.worker.ID, help.ID)
	assertCode(t, err, domain.Denied)
	var receipt []byte
	if err := f.s.db.QueryRow("SELECT result FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&receipt); err != nil || len(receipt) > 1024 || bytes.Contains(receipt, []byte(req.Reason)) {
		t.Fatal("help receipt duplicated private message payloads instead of references", string(receipt), err)
	}
	message := help.Messages[0]
	if _, err := f.s.ReceiveMessage(ctx, message.RecipientID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "handled"}); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, help) {
		t.Fatal("help replay changed immutable receipt after recipient disposition", replayed, err)
	}
	changed := req
	changed.ApplicationID = "terminal"
	_, err = f.s.RequestAssistanceHelp(ctx, f.worker.ID, changed)
	assertCode(t, err, domain.Conflict)
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != len(help.Messages) {
		t.Fatal("help replay delivered duplicate notices", count, err)
	}
}

func TestAssistanceHelpExpiryPreservesOrdinaryMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := domain.AssistanceHelpRequest{Command: op(), Mode: "tmux", Purpose: "terminal", Reason: "Headless terminal help requires no display"}
	help, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || len(help.Messages) != 1 {
		t.Fatal(help, err)
	}
	message := help.Messages[0]
	deferred, err := f.s.ReceiveMessage(ctx, f.founder.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "deferred"})
	if err != nil {
		t.Fatal(err)
	}
	pendingReq := req
	pendingReq.Command = op()
	pending, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, pendingReq)
	if err != nil || len(pending.Messages) != 1 {
		t.Fatal("pending help expiry fixture", pending, err)
	}
	var ordinary []string
	for _, kind := range []string{"direct", "session"} {
		m, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Ordinary immutable message remains unexpired", Origin: "service", Kind: kind, Urgent: true})
		if err != nil {
			t.Fatal(err)
		}
		ordinary = append(ordinary, m.ID)
		_, err = f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Cannot forge a service help deadline", Origin: "service", Kind: kind, ExpiresAt: help.ExpiresAt})
		assertCode(t, err, domain.Invalid)
	}
	var before, fingerprint, receipt []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&fingerprint, &receipt); err != nil {
		t.Fatal(err)
	}
	f.now = time.Unix(help.ExpiresAt, 0)
	current, err := f.s.Message(ctx, f.founder.ID, message.ID)
	if err != nil || current.State != "expired" || current.Revision != deferred.Revision || current.ExpiresAt != help.ExpiresAt {
		t.Fatal("help failed to expire at its exact immutable deadline", current, err)
	}
	if current, err := f.s.Message(ctx, f.founder.ID, pending.Messages[0].ID); err != nil || current.State != "expired" || current.Revision != 1 {
		t.Fatal("pending help failed to expire without a receipt mutation", current, err)
	}
	for _, urgent := range []bool{false, true} {
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 2 {
				t.Fatal("help expiry disrupted inbox pagination")
			}
			page, err := f.s.Inbox(ctx, f.founder.ID, cursor, 1, urgent)
			if err != nil || len(page.Items) != 1 || page.Items[0].State != "pending" || page.Items[0].ExpiresAt != 0 {
				t.Fatal("ordinary direct/session behavior changed", page, err)
			}
			got = append(got, page.Items[0].ID)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		slices.Sort(got)
		slices.Sort(ordinary)
		if !reflect.DeepEqual(got, ordinary) {
			t.Fatal("expired help remained in inbox or hid ordinary messages", got)
		}
	}
	for _, state := range []string{"handled", "pending", "deferred"} {
		_, err := f.s.ReceiveMessage(ctx, f.founder.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: deferred.Revision}, State: state})
		assertCode(t, err, domain.Conflict)
	}
	var after []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("expiry projection or rejected revival rewrote the notice", err)
	}
	if _, err := f.s.ReceiveMessage(ctx, f.founder.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: deferred.Revision}, State: "stale"}); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, help) {
		t.Fatal("expired/stale help replay renewed or changed its original receipt", replayed, err)
	}
	var afterFingerprint, afterReceipt []byte
	if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&afterFingerprint, &afterReceipt); err != nil || !bytes.Equal(fingerprint, afterFingerprint) || !bytes.Equal(receipt, afterReceipt) {
		t.Fatal("expiry rewrote operation evidence", err)
	}
}

func TestAssistanceHelpValidationAndDesktopFallback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	publishAssistanceScreens(t, f, assistanceScreens())
	otherProject, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Other context"})
	if err != nil {
		t.Fatal(err)
	}
	base := domain.AssistanceHelpRequest{Mode: "vnc", Purpose: "authentication", Provider: "codex", ProjectID: task.ProjectID, TaskID: task.ID, ScreenID: "screen-1", ApplicationID: "browser", Reason: "Public reason for display assistance"}
	for _, test := range []struct {
		name string
		code domain.Code
		edit func(*domain.AssistanceHelpRequest)
	}{
		{"empty reason", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Reason = "" }},
		{"blank reason", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Reason = " \n" }},
		{"long reason", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Reason = strings.Repeat("r", 8193) }},
		{"nul reason", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Reason = "reason\x00" }},
		{"invalid UTF-8 reason", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Reason = string([]byte{0xff}) }},
		{"unsupported mode", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Mode = "shell" }},
		{"unsupported purpose", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Purpose = "login" }},
		{"missing provider", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Provider = "" }},
		{"unsupported provider", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Provider = "other" }},
		{"terminal provider", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.Purpose = "terminal" }},
		{"missing screen", domain.Prerequisite, func(r *domain.AssistanceHelpRequest) { r.ScreenID = "" }},
		{"unknown screen", domain.Prerequisite, func(r *domain.AssistanceHelpRequest) { r.ScreenID = "unknown" }},
		{"unknown application", domain.Prerequisite, func(r *domain.AssistanceHelpRequest) { r.ApplicationID = "other" }},
		{"unsafe screen", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.ScreenID = "/display" }},
		{"implicit desktop fallback", domain.Denied, func(r *domain.AssistanceHelpRequest) { r.ScreenID = "desktop" }},
		{"screen widening permission", domain.Denied, func(r *domain.AssistanceHelpRequest) { r.DesktopFallback = true }},
		{"missing task project", domain.Invalid, func(r *domain.AssistanceHelpRequest) { r.ProjectID = "" }},
		{"mismatched task project", domain.Denied, func(r *domain.AssistanceHelpRequest) { r.ProjectID = otherProject.ID }},
		{"unknown project", domain.NotFound, func(r *domain.AssistanceHelpRequest) { r.ProjectID = "project_missing" }},
		{"unknown task", domain.NotFound, func(r *domain.AssistanceHelpRequest) { r.TaskID = "task_missing" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := base
			req.Command = op()
			test.edit(&req)
			_, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
			assertCode(t, err, test.code)
		})
	}
	foreignTask := base
	foreignTask.Command, foreignTask.Mode, foreignTask.ScreenID, foreignTask.ApplicationID = op(), "tmux", "", ""
	_, err = f.s.RequestAssistanceHelp(ctx, f.other.ID, foreignTask)
	assertCode(t, err, domain.Denied)
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid help proposals leaked partial private messages", count, err)
	}
	desktop := base
	desktop.Command, desktop.ScreenID, desktop.DesktopFallback = op(), "desktop", true
	help, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, desktop)
	if err != nil || len(help.Messages) != 1 || !strings.Contains(help.Messages[0].Body, "WHOLE DESKTOP") || !strings.Contains(help.Messages[0].Body, "\"desktop_fallback\":true") || !strings.Contains(help.Messages[0].Body, "not a promise of application-only isolation") {
		t.Fatal("explicit whole-desktop scope was hidden or rejected", help, err)
	}
	archived := true
	if _, err := f.s.ChangeProject(ctx, f.founder.ID, otherProject.ID, domain.ProjectChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: otherProject.Revision}, Archived: &archived, Reason: "Retire help context"}); err != nil {
		t.Fatal(err)
	}
	base.Command, base.ProjectID, base.TaskID = op(), otherProject.ID, ""
	_, err = f.s.RequestAssistanceHelp(ctx, f.worker.ID, base)
	assertCode(t, err, domain.Denied)
}

func TestAssistanceHelpCurrentRecipientsFounderRequiredAndAtomicDelivery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Current help recipients"})
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.AssistanceHelpRequest{Command: op(), Mode: "tmux", Purpose: "terminal", ProjectID: project.ID, Reason: "Coordinate explicitly authorized terminal help"}
	if _, err := f.s.db.Exec("CREATE TRIGGER reject_second_help BEFORE INSERT ON messages WHEN (SELECT count(*) FROM messages)=1 BEGIN SELECT RAISE(ABORT, 'fixture help delivery failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req); err == nil {
		t.Fatal("partial delivery fixture was unexpectedly accepted")
	}
	for _, query := range []string{"SELECT count(*) FROM messages", "SELECT count(*) FROM operations WHERE operation_id='" + req.OperationID + "'"} {
		var count int
		if err := f.s.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed multi-recipient help retained a partial message or receipt", count, err)
		}
	}
	if _, err := f.s.db.Exec("DROP TRIGGER reject_second_help"); err != nil {
		t.Fatal(err)
	}
	original, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || len(original.Messages) != 2 {
		t.Fatal("retry did not atomically create one full receipt", original, err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true})
	if err != nil {
		t.Fatal(err)
	}
	newDelegate := f.enroll(t, "new-current-delegate")
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: newDelegate.ID}); err != nil {
		t.Fatal(err)
	}
	fresh := req
	fresh.Command = op()
	current, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, fresh)
	if err != nil || len(current.Messages) != 2 {
		t.Fatal(current, err)
	}
	for _, message := range current.Messages {
		if message.RecipientID == f.other.ID || (message.RecipientID != f.founder.ID && message.RecipientID != newDelegate.ID) {
			t.Fatal("help used stale or arbitrary project recipients", message)
		}
	}
	if _, err := f.s.Revoke(ctx, f.founder.ID, newDelegate.ID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	fresh.Command = op()
	current, err = f.s.RequestAssistanceHelp(ctx, f.worker.ID, fresh)
	if err != nil || len(current.Messages) != 1 || current.Messages[0].RecipientID != f.founder.ID {
		t.Fatal("inactive delegate retained help routing", current, err)
	}
	if err := f.s.BanIdentity(ctx, f.founder.ID, "Fixture Founder unavailable"); err != nil {
		t.Fatal(err)
	}
	var beforeCount, afterCount int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&beforeCount); err != nil {
		t.Fatal(err)
	}
	fresh.Command = op()
	_, err = f.s.RequestAssistanceHelp(ctx, f.worker.ID, fresh)
	assertCode(t, err, domain.Prerequisite)
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&afterCount); err != nil || beforeCount != afterCount {
		t.Fatal("missing mandatory Founder caused partial help delivery", beforeCount, afterCount, err)
	}
	replayed, err := f.s.RequestAssistanceHelp(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, original) {
		t.Fatal("current routing changes rewrote the immutable original receipt", replayed, err)
	}
}

func TestAssistanceHelpConcurrentExactOperation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := domain.AssistanceHelpRequest{Command: op(), Mode: "tmux", Purpose: "terminal", Reason: "One durable request despite concurrent acknowledgement retries"}
	const attempts = 8
	results := make([]domain.AssistanceHelp, attempts)
	errors := make([]error, attempts)
	var group sync.WaitGroup
	for i := range attempts {
		group.Go(func() { results[i], errors[i] = f.s.RequestAssistanceHelp(ctx, f.worker.ID, req) })
	}
	group.Wait()
	for i := range attempts {
		if errors[i] != nil || !reflect.DeepEqual(results[i], results[0]) || len(results[i].Messages) != 1 {
			t.Fatal("concurrent exact retries minted different help receipts", i, results[i], errors[i])
		}
	}
	var messages, receipts int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&messages); err != nil || messages != 1 {
		t.Fatal("concurrent help retries duplicated private delivery", messages, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("concurrent help retries duplicated operation evidence", receipts, err)
	}
}
