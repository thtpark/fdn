package foundation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func sharingTerminals() []domain.Terminal {
	return []domain.Terminal{
		{ID: "shell", Name: "Help terminal", Generation: "terminal_shell", Kind: "shell", CanInput: true},
		{ID: "other", Name: "Second terminal", Generation: "terminal_other", Kind: "shell", CanInput: true},
		{ID: "execution", Name: "Current execution output", Generation: "invocation_execution", Kind: "execution"},
	}
}

func publishSharingFixture(t *testing.T, f *fixture) domain.Sharing {
	t.Helper()
	publishAssistanceScreens(t, f, assistanceScreens())
	record, err := f.s.PublishSharing(context.Background(), f.worker.ID, domain.SharingPublishRequest{Command: op(), Terminals: sharingTerminals()})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func requestSharingFixture(t *testing.T, f *fixture, requester, grant string, record domain.Sharing, mode string) domain.Session {
	t.Helper()
	req := domain.SessionRequest{Command: op(), TargetID: f.worker.ID, Purpose: "sharing", Mode: mode,
		GrantID: grant, Reason: "Explicit Foundation-only selected-resource viewing", LifetimeSeconds: domain.MaxSharingSeconds,
		SelectionRevision: record.SelectionRevision}
	if mode == "tmux" {
		req.TerminalID = record.TerminalID
	} else {
		req.ScreenID = record.ScreenID
	}
	session, err := f.s.RequestSession(context.Background(), requester, req)
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(context.Background(), requester, session.ID, domain.SessionChange{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSharingFounderViewingDoesNotChangeBusyWorkAndInputWaitsForSafeHandoff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := publishSharingFixture(t, f)
	task := f.ready(t)
	token := domain.NewToken()
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration,
		TokenHash: domain.Hash(token), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := requestSharingFixture(t, f, f.founder.ID, "", record, "tmux")
	if _, err := f.s.SessionAccess(ctx, f.founder.ID, session.ID, "requester"); err != nil {
		t.Fatal("Founder could not view during active execution", err)
	}
	after, err := f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("viewing changed the ongoing task", err)
	}
	_, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "request-input"})
	assertCode(t, err, domain.Prerequisite)
	record, err = f.s.SelectSharing(ctx, f.founder.ID, f.worker.ID, domain.SharingSelectRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: record.Revision}, Mode: "tmux", ResourceID: "shell", Reason: "Select an input-capable help terminal"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.Session(ctx, f.founder.ID, session.ID)
	if err != nil || session.TerminalID != "shell" || session.SelectionRevision != record.SelectionRevision {
		t.Fatal("target selection did not update the same sharing session", session, err)
	}
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "request-input"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.s.Session(ctx, f.worker.ID, session.InputLeaseID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ChangeSession(ctx, f.worker.ID, lease.ID, domain.SessionChange{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: lease.Revision}, Action: "grant-input"})
	assertCode(t, err, domain.Prerequisite)
	if _, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: run.SpecGeneration, Outcome: "completed", Summary: "Actual bounded work completed", Evidence: []string{"fixture process stopped"}}); err != nil {
		t.Fatal(err)
	}
	lease, err = f.s.ChangeSession(ctx, f.worker.ID, lease.ID, domain.SessionChange{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: lease.Revision}, Action: "grant-input"})
	if err != nil || !lease.InputGranted {
		t.Fatal("target could not acknowledge the safe handoff", lease, err)
	}
	_, err = f.s.SelectSharing(ctx, f.worker.ID, f.worker.ID, domain.SharingSelectRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: record.Revision}, Mode: "tmux", ResourceID: "other", Reason: "Cannot silently move an active input handoff"})
	assertCode(t, err, domain.Prerequisite)
}

func TestSharingApprovalIsFixedServerTimedAndNeverExceeds24Hours(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := publishSharingFixture(t, f)
	_, err := f.s.Sharing(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
	req := domain.AssistanceGrantRequest{Command: op(), RequesterID: f.other.ID, TargetID: f.worker.ID,
		Mode: "both", Purpose: "sharing", DurationSeconds: domain.MaxSharingSeconds, Reason: "Approve selected-resource access once"}
	for _, seconds := range []int64{0, -1, domain.MaxSharingSeconds + 1} {
		bad := req
		bad.Command, bad.DurationSeconds = op(), seconds
		_, err := f.s.CreateAssistance(ctx, f.founder.ID, bad)
		assertCode(t, err, domain.Invalid)
	}
	_, err = f.s.CreateAssistance(ctx, f.worker.ID, req)
	assertCode(t, err, domain.Denied)
	short := req
	short.Command, short.DurationSeconds = op(), 1
	oneSecond, err := f.s.CreateAssistance(ctx, f.founder.ID, short)
	if err != nil || oneSecond.ExpiresAt-oneSecond.CreatedAt != 1 {
		t.Fatal("minimum one-second approval was not exact", oneSecond, err)
	}
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, req)
	if err != nil || grant.ExpiresAt-grant.CreatedAt != domain.MaxSharingSeconds {
		t.Fatal("approval was not fixed at exactly 24 hours", grant, err)
	}
	session := requestSharingFixture(t, f, f.other.ID, grant.ID, record, "tmux")
	if session.ExpiresAt != grant.ExpiresAt {
		t.Fatal("session can outlive its approval", session.ExpiresAt, grant.ExpiresAt)
	}
	_, err = f.s.SelectSharing(ctx, f.other.ID, f.worker.ID, domain.SharingSelectRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: record.Revision}, Mode: "tmux", ResourceID: "other", Reason: "Delegate cannot select another endpoint"})
	assertCode(t, err, domain.Denied)
	_, err = f.s.ChangeSession(ctx, f.other.ID, session.ID, domain.SessionChange{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "request-input"})
	assertCode(t, err, domain.Denied)
	f.now = f.now.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	replayed, err := f.s.CreateAssistance(ctx, f.founder.ID, req)
	if err != nil || replayed.ExpiresAt != grant.ExpiresAt {
		t.Fatal("replay renewed the approval", replayed, err)
	}
	again := requestSharingFixture(t, f, f.other.ID, grant.ID, record, "vnc")
	if again.ExpiresAt != grant.ExpiresAt {
		t.Fatal("reconnect extended approval lifetime")
	}
	f.now = f.now.Add(time.Second)
	_, err = f.s.SessionAccess(ctx, f.other.ID, session.ID, "requester")
	assertCode(t, err, domain.Prerequisite)
	_, err = f.s.Sharing(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
}

func TestSharingTargetSelectionAndRevocationAreCurrentForEveryRelay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := publishSharingFixture(t, f)
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(),
		RequesterID: f.other.ID, TargetID: f.worker.ID, Mode: "both", Purpose: "sharing", DurationSeconds: 120, Reason: "Bounded delegate observation"})
	if err != nil {
		t.Fatal(err)
	}
	session := requestSharingFixture(t, f, f.other.ID, grant.ID, record, "vnc")
	expires := session.ExpiresAt
	record, err = f.s.SelectSharing(ctx, f.worker.ID, f.worker.ID, domain.SharingSelectRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: record.Revision}, Mode: "vnc", ResourceID: "screen-2", Reason: "Target selects the screen needing help"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.s.SessionAccess(ctx, f.other.ID, session.ID, "requester")
	if err != nil || current.ScreenID != "screen-2" || current.SelectionRevision != record.SelectionRevision || current.ExpiresAt != expires {
		t.Fatal("switch did not keep one selected-resource session and original expiry", current, err)
	}
	_, err = f.s.SelectSharing(ctx, f.founder.ID, f.worker.ID, domain.SharingSelectRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: record.Revision}, Mode: "vnc", ResourceID: "screen-2", Protected: true, Reason: "Protect a credential screen"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SessionAccess(ctx, f.other.ID, session.ID, "requester")
	assertCode(t, err, domain.Denied)
	if _, err := f.s.RevokeAssistance(ctx, f.founder.ID, grant.ID, domain.AssistanceRevoke{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: grant.Revision}, Reason: "Revoke remaining access"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Sharing(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
}
