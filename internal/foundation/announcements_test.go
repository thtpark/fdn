package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func announcementRequest(audience domain.AnnouncementAudience) domain.AnnouncementRequest {
	req := domain.AnnouncementRequest{Command: op(), Scope: audience.Scope, ProjectID: audience.ProjectID, ProjectEpoch: audience.ProjectEpoch,
		Kind: "request", Title: "Check project setup", Body: "Inspect the permitted workspace and report actual results."}
	for _, recipient := range audience.Recipients {
		req.RecipientIDs = append(req.RecipientIDs, recipient.AgentID)
	}
	return req
}

func TestAnnouncementsRequireFounderOrCurrentDelegateAndProjectAudienceIsExact(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.s.AnnouncementAudience(ctx, f.worker.ID, "global", ""); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary worker acquired broadcast authority", err)
	}
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Announcement project"})
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.worker.ID})
	if err != nil {
		t.Fatal(err)
	}
	audience, err := f.s.AnnouncementAudience(ctx, f.worker.ID, "project", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(audience.Recipients) != 1 || audience.Recipients[0].AgentID != f.founder.ID || audience.ProjectEpoch != project.Epoch {
		t.Fatal("project audience included an unrelated worker", audience)
	}
	global, err := f.s.AnnouncementAudience(ctx, f.worker.ID, "global", "")
	if err != nil || len(global.Recipients) != 2 {
		t.Fatal("authorized delegate could not send globally", global, err)
	}
	request := announcementRequest(global)
	result, err := f.s.Announce(ctx, f.worker.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.Announce(ctx, f.worker.ID, request)
	if err != nil || !reflect.DeepEqual(result, replayed) {
		t.Fatal("announcement lost-response replay changed the delivery set", replayed, err)
	}
	for _, delivery := range result.Deliveries {
		message, err := f.s.Message(ctx, delivery.AgentID, delivery.MessageID)
		if err != nil || message.Announcement == nil || message.Announcement.ID != result.ID || !message.ResponseRequest ||
			message.Kind != "direct" || message.ReadAt != 0 || message.Request != nil {
			t.Fatal("broadcast did not preserve the autonomous direct-message flow and private receipts", message, err)
		}
	}
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.worker.ID, Revoke: true}); err != nil {
		t.Fatal(err)
	}
	messageID := result.Deliveries[0].MessageID
	targetID := result.Deliveries[0].AgentID
	if _, err := f.s.AnnouncementMessageAuthority(ctx, targetID, messageID); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("revoked delegate's retained announcement still authorized action", err)
	}
}

func TestAnnouncementsRejectChangedAudiencesAndUpgradeIntentNeverForgesNativePermits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	audience, err := f.s.AnnouncementAudience(ctx, f.founder.ID, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	request := announcementRequest(audience)
	request.Kind = "upgrade-request"
	request.Body = "Prepare the supported Foundation upgrade and obtain approval for replacement."
	f.enroll(t, "new-broadcast-recipient")
	if _, err := f.s.Announce(ctx, f.founder.ID, request); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("changed audience was silently expanded", err)
	}
	audience, err = f.s.AnnouncementAudience(ctx, f.founder.ID, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	request = announcementRequest(audience)
	request.Kind = "upgrade-request"
	result, err := f.s.Announce(ctx, f.founder.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range result.Deliveries {
		message, err := f.s.Message(ctx, delivery.AgentID, delivery.MessageID)
		if err != nil || message.Request != nil || message.Announcement.Kind != "upgrade-request" || !message.ResponseRequest {
			t.Fatal("upgrade intent was converted to unauthorized native installation", message, err)
		}
	}
	request = announcementRequest(audience)
	request.Kind = "notice"
	result, err = f.s.Announce(ctx, f.founder.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.s.Message(ctx, result.Deliveries[0].AgentID, result.Deliveries[0].MessageID)
	if err != nil || message.ResponseRequest {
		t.Fatal("informational announcement started an automatic reply", err)
	}
}
