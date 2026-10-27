package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestScopedMessageRequestsRetainDelegateAuthorityAndRevocation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project, err := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	send := func(action domain.MessageAction) (domain.Message, error) {
		return f.s.SendMessage(ctx, f.other.ID, domain.MessageRequest{
			Command: op(), RecipientID: f.worker.ID, Body: "Please review this scoped request when safe.",
			Origin: "ai", Kind: "request", ResponseRequest: true, Request: &action,
		})
	}
	action := domain.MessageAction{Kind: "work", ProjectID: project.ID}
	_, err = send(action)
	assertCode(t, err, domain.Denied)
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	source, err := send(action)
	if err != nil || source.Request.ProjectEpoch != project.Epoch {
		t.Fatal("current delegate could not request scoped work", source, err)
	}
	_, err = send(domain.MessageAction{Kind: "upgrade"})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ReportMessageAction(ctx, f.worker.ID, source.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: source.Revision},
		State:   "accepted", Reason: "Try to accept authority from an obsolete project snapshot."})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.ReportMessageAction(ctx, f.worker.ID, source.ID, domain.MessageActionReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: source.Revision},
		State:   "declined", Reason: "The requester no longer has current project delegation.",
	}); err != nil {
		t.Fatal("revocation prevented an honest target refusal", err)
	}
}
