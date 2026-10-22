package foundation

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestPermissionMessageProtectedValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := f.now.Unix()
	for _, item := range []struct {
		name      string
		expiresAt int64
		valid     bool
	}{
		{"default", 0, true}, {"next second", now + 1, true}, {"maximum", now + 900, true},
		{"now", now, false}, {"past", now - 1, false}, {"negative", -1, false}, {"too long", now + 901, false}, {"overflow", 1<<63 - 1, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			message, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Nonsecret capability help", Kind: "permission", Origin: "service", ExpiresAt: item.expiresAt})
			if !item.valid {
				assertCode(t, err, domain.Invalid)
				return
			}
			deadline := item.expiresAt
			if deadline == 0 {
				deadline = now + domain.AuthenticationRequestTTL
			}
			if err != nil || message.Kind != "permission" || message.Origin != "service" || message.ResponseRequest || message.ExpiresAt != deadline || message.AuthenticationRefresh != nil {
				t.Fatal("permission handoff lost protected fixed-deadline semantics", message, err)
			}
		})
	}
	for _, origin := range []string{"human", "ai", "", "root"} {
		_, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Nonsecret capability help", Kind: "permission", Origin: origin})
		assertCode(t, err, domain.Invalid)
	}
	_, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Nonsecret capability help", Kind: "permission", Origin: "service", ResponseRequest: true})
	assertCode(t, err, domain.Invalid)
	for _, table := range []string{"assistance_grants", "sessions", "runs"} {
		var count int
		if err := f.s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("notice created authorization, a session or inference", table, count, err)
		}
	}
	worker, err := f.s.Agent(ctx, f.worker.ID, f.worker.ID)
	if err != nil || !reflect.DeepEqual(worker, f.worker) {
		t.Fatal("permission notice changed worker mode or identity", worker, err)
	}
}

func TestPermissionMessagePrivateImmutableReplayAndExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "permission help scope"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID}); err != nil {
		t.Fatal(err)
	}
	req := domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Task capability help is data, never an approval", Origin: "service", Kind: "permission", Urgent: true, ExpiresAt: f.now.Unix() + 900}
	message, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{f.worker.ID, f.founder.ID} {
		read, err := f.s.Message(ctx, actor, message.ID)
		if err != nil || !reflect.DeepEqual(read, message) {
			t.Fatal("private party could not read exact permission handoff", read, err)
		}
	}
	_, err = f.s.Message(ctx, f.other.ID, message.ID)
	assertCode(t, err, domain.Denied)
	var before, fingerprint, receipt []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&fingerprint, &receipt); err != nil {
		t.Fatal(err)
	}
	f.now = time.Unix(message.ExpiresAt, 0)
	read, err := f.s.Message(ctx, f.founder.ID, message.ID)
	if err != nil || read.State != "expired" || read.ExpiresAt != message.ExpiresAt || read.Revision != message.Revision {
		t.Fatal("permission handoff did not expire at its exact deadline", read, err)
	}
	for _, urgent := range []bool{false, true} {
		page, err := f.s.Inbox(ctx, f.founder.ID, "", 1, urgent)
		if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatal("expired permission handoff remained discoverable in inbox", page, err)
		}
	}
	replayed, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, message) {
		t.Fatal("expired permission operation lost its immutable receipt", replayed, err)
	}
	var after, afterFingerprint, afterReceipt []byte
	if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("expiry rewrote stored permission message", err)
	}
	if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.worker.ID, req.OperationID).Scan(&afterFingerprint, &afterReceipt); err != nil || !bytes.Equal(fingerprint, afterFingerprint) || !bytes.Equal(receipt, afterReceipt) {
		t.Fatal("expiry rewrote immutable operation evidence", err)
	}
	req.Body = "changed immutable proposal"
	_, err = f.s.SendMessage(ctx, f.worker.ID, req)
	assertCode(t, err, domain.Conflict)
}

func TestPermissionInboxFiltersExpiredAndLegacyBeforePagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := f.now.Unix()
	for _, item := range []struct {
		id, kind string
		created  int64
		expires  int64
	}{
		{"message_000", "permission", now - 900, 0},
		{"message_001", "permission", now - 900, now},
		{"message_002", "direct", now - 900, 0},
		{"message_003", "permission", now - 900, now - 1},
		{"message_004", "permission", now - 100, 0},
		{"message_005", "permission", now, now + 900},
	} {
		seedAuthenticationMessage(t, f.s, domain.Message{ID: item.id, SenderID: f.worker.ID, RecipientID: f.founder.ID, Body: "Nonsecret permission inbox fixture", Origin: "service", Kind: item.kind, State: "pending", CreatedAt: item.created, ExpiresAt: item.expires, Revision: 1, Urgent: true})
	}
	for _, urgent := range []bool{false, true} {
		var ids []string
		cursor := ""
		for pageNumber := 0; ; pageNumber++ {
			if pageNumber > 3 {
				t.Fatal("permission inbox pagination did not terminate")
			}
			page, err := f.s.Inbox(ctx, f.founder.ID, cursor, 1, urgent)
			if err != nil || len(page.Items) != 1 || page.Items[0].State != "pending" {
				t.Fatal("expired permission message disrupted pagination", page, err)
			}
			if message := page.Items[0]; message.ID == "message_004" && message.ExpiresAt != now+800 {
				t.Fatal("live legacy permission deadline was not projected", message)
			}
			ids = append(ids, page.Items[0].ID)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if !reflect.DeepEqual(ids, []string{"message_002", "message_004", "message_005"}) {
			t.Fatal("permission expiry filter lost or duplicated inbox records", ids)
		}
	}
}
