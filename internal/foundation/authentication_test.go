package foundation

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func authenticationMessageFixture(t *testing.T, f *fixture, kind string) domain.Message {
	t.Helper()
	var message domain.Message
	var err error
	if kind == "authentication-refresh" {
		message, err = f.s.RequestAuthentication(context.Background(), f.founder.ID, f.worker.ID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "Founder explicitly requested a fresh sign-in status"})
	} else {
		message, err = f.s.SendMessage(context.Background(), f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "Nonsecret provider sign-in handoff", Origin: "service", Kind: kind, Urgent: true})
	}
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func seedAuthenticationMessage(t *testing.T, store *Store, message domain.Message) []byte {
	t.Helper()
	data, err := encode(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,?)", message.ID, data, message.SenderID, message.RecipientID, message.State, message.Urgent); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAuthenticationRefreshFounderAuthorityAndPrivateControl(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "refresh scope"})
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.AuthenticationRefreshRequest{Command: op(), Provider: "copilot", ProjectID: project.ID, Reason: "Obtain a new status after the previous request expired"}
	for _, actor := range []string{f.worker.ID, f.other.ID} {
		_, err := f.s.RequestAuthentication(ctx, actor, f.worker.ID, req)
		assertCode(t, err, domain.Denied)
	}
	message, err := f.s.RequestAuthentication(ctx, f.founder.ID, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.AuthenticationRefresh{Provider: req.Provider, ProjectID: project.ID, Reason: req.Reason}
	if message.SenderID != f.founder.ID || message.RecipientID != f.worker.ID || message.Kind != "authentication-refresh" || message.Origin != "service" || !message.Urgent || message.ResponseRequest || message.State != "pending" || message.Revision != 1 || message.CreatedAt != f.now.Unix() || message.ExpiresAt != message.CreatedAt+domain.AuthenticationRequestTTL || message.AuthenticationRefresh == nil || *message.AuthenticationRefresh != want {
		t.Fatalf("invalid service-owned refresh control: %+v", message)
	}
	if strings.Contains(message.Body, req.Reason) || !strings.Contains(message.Body, req.Provider) || domain.CheckText(message.Body, "body", 32768, true) != nil {
		t.Fatal("refresh body must be a bounded nonsecret service explanation")
	}
	for _, actor := range []string{f.founder.ID, f.worker.ID} {
		read, err := f.s.Message(ctx, actor, message.ID)
		if err != nil || !reflect.DeepEqual(read, message) {
			t.Fatal("private party could not read exact control", read, err)
		}
	}
	_, err = f.s.Message(ctx, f.other.ID, message.ID)
	assertCode(t, err, domain.Denied)
	inbox, err := f.s.Inbox(ctx, f.worker.ID, "", 1, true)
	if err != nil || len(inbox.Items) != 1 || !reflect.DeepEqual(inbox.Items[0], message) {
		t.Fatal("urgent durable control missing from inbox", inbox, err)
	}
	currentProject, err := f.s.Project(ctx, f.worker.ID, project.ID)
	if err != nil || !reflect.DeepEqual(currentProject, project) {
		t.Fatal("refresh changed project authority", currentProject, err)
	}
	worker, err := f.s.Agent(ctx, f.founder.ID, f.worker.ID)
	if err != nil || !reflect.DeepEqual(worker, f.worker) {
		t.Fatal("refresh changed worker mode or identity", worker, err)
	}
	for _, table := range []string{"assistance_grants", "sessions", "runs"} {
		var count int
		if err := f.s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("refresh created unrelated authorization/work", table, count, err)
		}
	}
}

func TestAuthenticationRefreshValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "archived scope"})
	if err != nil {
		t.Fatal(err)
	}
	archived := true
	if _, err := f.s.ChangeProject(ctx, f.founder.ID, project.ID, domain.ProjectChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, Archived: &archived, Reason: "archive fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, provider, project, reason, target string
		code                                    domain.Code
	}{
		{"missing provider", "", "", "explicit refresh", f.worker.ID, domain.Invalid},
		{"unsupported provider", "other", "", "explicit refresh", f.worker.ID, domain.Invalid},
		{"case sensitive provider", "Codex", "", "explicit refresh", f.worker.ID, domain.Invalid},
		{"missing reason", "codex", "", "", f.worker.ID, domain.Invalid},
		{"blank reason", "codex", "", " \n\t", f.worker.ID, domain.Invalid},
		{"long reason", "codex", "", strings.Repeat("r", 8193), f.worker.ID, domain.Invalid},
		{"nul reason", "codex", "", "reason\x00", f.worker.ID, domain.Invalid},
		{"invalid UTF-8 reason", "codex", "", string([]byte{0xff}), f.worker.ID, domain.Invalid},
		{"missing project", "codex", "project_missing", "explicit refresh", f.worker.ID, domain.NotFound},
		{"archived project", "codex", project.ID, "explicit refresh", f.worker.ID, domain.Denied},
		{"missing target", "codex", "", "explicit refresh", "agent_missing", domain.NotFound},
	} {
		t.Run(item.name, func(t *testing.T) {
			_, err := f.s.RequestAuthentication(ctx, f.founder.ID, item.target, domain.AuthenticationRefreshRequest{Command: op(), Provider: item.provider, ProjectID: item.project, Reason: item.reason})
			assertCode(t, err, item.code)
		})
	}
	if _, err := f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RequestAuthentication(ctx, f.founder.ID, f.worker.ID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "explicit refresh"})
	assertCode(t, err, domain.Denied)
	if err := f.s.BanIdentity(ctx, f.other.ID, "fixture security hold"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RequestAuthentication(ctx, f.founder.ID, f.other.ID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "explicit refresh"})
	assertCode(t, err, domain.Denied)
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages WHERE json_extract(data,'$.kind')='authentication-refresh'").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected refresh left a durable control", count, err)
	}
	for _, provider := range []string{"codex", "copilot"} {
		if _, err := f.s.RequestAuthentication(ctx, f.founder.ID, f.founder.ID, domain.AuthenticationRefreshRequest{Command: op(), Provider: provider, Reason: strings.Repeat("r", 8192)}); err != nil {
			t.Fatal("valid unscoped provider refresh rejected", err)
		}
	}
	if err := f.s.BanIdentity(ctx, f.founder.ID, "fixture Founder security hold"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RequestAuthentication(ctx, f.founder.ID, f.founder.ID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "explicit refresh"})
	assertCode(t, err, domain.Denied)
}

func TestAuthenticationMessageDeadlineValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := f.now.Unix()
	for _, item := range []struct {
		name      string
		expiresAt int64
		valid     bool
	}{
		{"absent", 0, true},
		{"one second", now + 1, true},
		{"maximum", now + domain.AuthenticationRequestTTL, true},
		{"now", now, false},
		{"past", now - 1, false},
		{"negative", -1, false},
		{"too long", now + domain.AuthenticationRequestTTL + 1, false},
		{"overflow-sized deadline", 1<<63 - 1, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			message, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "Nonsecret sign-in handoff", Origin: "service", Kind: "authentication", ExpiresAt: item.expiresAt})
			if !item.valid {
				assertCode(t, err, domain.Invalid)
				return
			}
			deadline := item.expiresAt
			if deadline == 0 {
				deadline = now + domain.AuthenticationRequestTTL
			}
			if err != nil || message.ExpiresAt != deadline || message.CreatedAt != now || message.AuthenticationRefresh != nil {
				t.Fatal("invalid authentication notice deadline", message, err)
			}
		})
	}
	for _, kind := range []string{"direct", "session"} {
		for _, expiresAt := range []int64{-1, now + 1} {
			_, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "ordinary notice", Origin: "service", Kind: kind, ExpiresAt: expiresAt})
			assertCode(t, err, domain.Invalid)
		}
		message, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "ordinary notice", Origin: "service", Kind: kind})
		if err != nil || message.ExpiresAt != 0 || message.AuthenticationRefresh != nil {
			t.Fatal("ordinary message semantics changed", message, err)
		}
	}
	_, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "declared service text is not authority", Origin: "service", Kind: "authentication-refresh"})
	assertCode(t, err, domain.Invalid)
	for _, kind := range []string{"authentication", "session"} {
		_, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "protected handoff", Origin: "service", Kind: kind, ResponseRequest: true})
		assertCode(t, err, domain.Invalid)
	}
}

func TestAuthenticationOperationsReplayAfterExpiry(t *testing.T) {
	for _, kind := range []string{"authentication", "authentication-refresh"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			command := op()
			notice := domain.MessageRequest{Command: command, RecipientID: f.worker.ID, Body: "sign-in required", Origin: "service", Kind: "authentication", ExpiresAt: f.now.Unix() + 1}
			refresh := domain.AuthenticationRefreshRequest{Command: command, Provider: "codex", Reason: "explicit refresh"}
			send := func() (domain.Message, error) {
				if kind == "authentication" {
					return f.s.SendMessage(ctx, f.founder.ID, notice)
				}
				return f.s.RequestAuthentication(ctx, f.founder.ID, f.worker.ID, refresh)
			}
			message, err := send()
			if err != nil {
				t.Fatal(err)
			}
			var before, receipt, fingerprint []byte
			if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.founder.ID, command.OperationID).Scan(&fingerprint, &receipt); err != nil {
				t.Fatal(err)
			}
			f.now = time.Unix(message.ExpiresAt, 0).Add(time.Second)
			read, err := f.s.Message(ctx, f.worker.ID, message.ID)
			if err != nil || read.State != "expired" || read.ExpiresAt != message.ExpiresAt || read.Revision != message.Revision {
				t.Fatal("read did not project expiry", read, err)
			}
			page, err := f.s.Inbox(ctx, f.worker.ID, "", 1, false)
			if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatal("expired handoff remained in inbox", page, err)
			}
			replayed, err := send()
			if err != nil || !reflect.DeepEqual(replayed, message) {
				t.Fatal("exact expired operation lost its immutable message receipt", replayed, err)
			}
			var after, afterReceipt, afterFingerprint []byte
			if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatal("expiry rewrote stored message evidence", err)
			}
			if err := f.s.db.QueryRow("SELECT fingerprint,result FROM operations WHERE actor_id=? AND operation_id=?", f.founder.ID, command.OperationID).Scan(&afterFingerprint, &afterReceipt); err != nil || !bytes.Equal(fingerprint, afterFingerprint) || !bytes.Equal(receipt, afterReceipt) {
				t.Fatal("expiry rewrote operation evidence", err)
			}
			var count int
			if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != 1 {
				t.Fatal("expiry automatically renewed a request", count, err)
			}
			notice.Body, refresh.Reason = "changed proposal", "changed proposal"
			_, err = send()
			assertCode(t, err, domain.Conflict)
			if kind == "authentication-refresh" {
				refresh.Reason = "explicit refresh"
				_, err = f.s.RequestAuthentication(ctx, f.founder.ID, f.other.ID, refresh)
				assertCode(t, err, domain.Conflict)
				refresh.Command = op()
				fresh, err := send()
				if err != nil || fresh.ID == message.ID || fresh.ExpiresAt != f.now.Unix()+domain.AuthenticationRequestTTL {
					t.Fatal("new explicit Founder request did not create a fresh deadline", fresh, err)
				}
			}
		})
	}
}

func TestAuthenticationLegacySendReplayDoesNotRenewDeadline(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := domain.MessageRequest{Command: op(), RecipientID: f.founder.ID, Body: "retained legacy sign-in notice", Origin: "service", Kind: "authentication", Urgent: true}
	message, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec("UPDATE messages SET data=json_remove(data,'$.expires_at') WHERE id=?", message.ID); err != nil {
		t.Fatal(err)
	}
	message.ExpiresAt = 0
	f.now = f.now.Add(time.Duration(domain.AuthenticationRequestTTL+1) * time.Second)
	read, err := f.s.Message(ctx, f.founder.ID, message.ID)
	if err != nil || read.State != "expired" || read.ExpiresAt != message.CreatedAt+domain.AuthenticationRequestTTL {
		t.Fatal("legacy notice deadline was not derived from original creation", read, err)
	}
	replayed, err := f.s.SendMessage(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, message) {
		t.Fatal("upgrade or operation replay minted a fresh legacy deadline", replayed, err)
	}
	page, err := f.s.Inbox(ctx, f.founder.ID, "", 1, false)
	if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("legacy replay revived an expired notice", page, err)
	}
	var count int
	if err := f.s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy replay generated another notice", count, err)
	}
}

func TestAuthenticationExpiryProjectsLegacyWithoutMutatingEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	clock := f.now
	for _, kind := range []string{"authentication", "authentication-refresh", "permission", "direct", "session"} {
		for _, state := range []string{"pending", "deferred", "handled", "stale"} {
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/legacy=%t", kind, state, legacy), func(t *testing.T) {
					message := domain.Message{ID: domain.NewID("message"), SenderID: f.founder.ID, RecipientID: f.worker.ID, Body: "retained nonsecret handoff", Origin: "service", Kind: kind, State: state, CreatedAt: clock.Unix() - domain.AuthenticationRequestTTL, Revision: 4}
					if !legacy {
						message.ExpiresAt = clock.Unix()
					}
					before := seedAuthenticationMessage(t, f.s, message)
					f.now = clock.Add(-time.Second)
					read, err := f.s.Message(ctx, f.worker.ID, message.ID)
					if err != nil || read.State != state {
						t.Fatal("message expired before its exact deadline", read, err)
					}
					f.now = clock
					read, err = f.s.Message(ctx, f.worker.ID, message.ID)
					wantState, wantExpiry := state, message.ExpiresAt
					if kind == "authentication" || kind == "authentication-refresh" || kind == "permission" {
						wantExpiry = clock.Unix()
						if state == "pending" || state == "deferred" {
							wantState = "expired"
						}
					}
					if err != nil || read.State != wantState || read.ExpiresAt != wantExpiry || read.Revision != message.Revision {
						t.Fatal("incorrect expiry/final-receipt projection", read, err)
					}
					var after []byte
					if err := f.s.db.QueryRow("SELECT data FROM messages WHERE id=?", message.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
						t.Fatal("reading legacy expiry changed retained payload", err)
					}
				})
			}
		}
	}
}

func TestAuthenticationReceiptsCannotReviveExpiredHandoffs(t *testing.T) {
	for _, kind := range []string{"authentication", "authentication-refresh", "permission"} {
		for _, disposition := range []string{"pending", "deferred"} {
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/legacy=%t", kind, disposition, legacy), func(t *testing.T) {
					f := newFixture(t)
					ctx := context.Background()
					message := authenticationMessageFixture(t, f, kind)
					if legacy {
						if _, err := f.s.db.Exec("UPDATE messages SET data=json_remove(data,'$.expires_at') WHERE id=?", message.ID); err != nil {
							t.Fatal(err)
						}
						message.ExpiresAt = 0
					}
					deferred := domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "deferred"}
					var err error
					if disposition == "deferred" {
						message, err = f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, deferred)
						if err != nil {
							t.Fatal(err)
						}
					}
					f.now = f.now.Add(time.Duration(domain.AuthenticationRequestTTL) * time.Second)
					for _, state := range []string{"handled", "pending", "deferred"} {
						_, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: state})
						assertCode(t, err, domain.Conflict)
					}
					_, err = f.s.ReceiveMessage(ctx, f.founder.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "stale"})
					assertCode(t, err, domain.Denied)
					if disposition == "deferred" {
						replayed, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, deferred)
						if err != nil || !reflect.DeepEqual(replayed, message) {
							t.Fatal("expiry rejected an exact existing receipt operation", replayed, err)
						}
					}
					read, err := f.s.Message(ctx, f.worker.ID, message.ID)
					if err != nil || read.State != "expired" || read.Revision != message.Revision {
						t.Fatal("rejected receipt revived or modified an expired message", read, err)
					}
					stale, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "stale"})
					if err != nil || stale.State != "stale" || stale.Revision != message.Revision+1 || stale.ExpiresAt != message.ExpiresAt {
						t.Fatal("recipient could not retain a stale receipt without rewriting expiry", stale, err)
					}
					_, err = f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: stale.Revision}, State: "pending"})
					assertCode(t, err, domain.Conflict)
				})
			}
		}
	}
}

func TestAuthenticationHandledReceiptSurvivesExpiry(t *testing.T) {
	for _, kind := range []string{"authentication", "authentication-refresh", "permission"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			message := authenticationMessageFixture(t, f, kind)
			req := domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: message.Revision}, State: "handled"}
			handled, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, req)
			if err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Duration(domain.AuthenticationRequestTTL+1) * time.Second)
			read, err := f.s.Message(ctx, f.worker.ID, message.ID)
			if err != nil || !reflect.DeepEqual(read, handled) {
				t.Fatal("expiry overwrote handled evidence", read, err)
			}
			replayed, err := f.s.ReceiveMessage(ctx, f.worker.ID, message.ID, req)
			if err != nil || !reflect.DeepEqual(replayed, handled) {
				t.Fatal("expiry invalidated an exact handled receipt replay", replayed, err)
			}
		})
	}
}

func TestAuthenticationInboxFiltersBeforePagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := f.now.Unix()
	for _, item := range []struct {
		id, kind, state string
		created, expiry int64
		urgent          bool
	}{
		{"message_000", "authentication", "pending", now - 900, 0, true},
		{"message_001", "authentication-refresh", "pending", now - 900, now, true},
		{"message_002", "direct", "pending", now - 10000, 0, true},
		{"message_003", "authentication", "pending", now - 900, now - 1, true},
		{"message_004", "authentication", "pending", now - 100, 0, true},
		{"message_005", "authentication-refresh", "pending", now - 901, 0, true},
		{"message_006", "session", "pending", now - 10000, 0, false},
		{"message_007", "authentication-refresh", "pending", now - 900, now, true},
		{"message_008", "authentication-refresh", "pending", now, now + 1, true},
		{"message_009", "authentication", "handled", now, now + 900, true},
		{"message_010", "authentication", "deferred", now, now + 900, true},
		{"message_011", "authentication", "pending", now - 900, 0, true},
	} {
		seedAuthenticationMessage(t, f.s, domain.Message{ID: item.id, SenderID: f.founder.ID, RecipientID: f.worker.ID, Body: "nonsecret inbox fixture", Origin: "service", Kind: item.kind, State: item.state, CreatedAt: item.created, ExpiresAt: item.expiry, Urgent: item.urgent, Revision: 1})
	}
	for _, urgent := range []bool{false, true} {
		for _, limit := range []int{1, 2, 200} {
			t.Run(fmt.Sprintf("urgent=%t/limit=%d", urgent, limit), func(t *testing.T) {
				var ids []string
				cursor := ""
				for pageNumber := 0; ; pageNumber++ {
					if pageNumber > 4 {
						t.Fatal("inbox pagination did not terminate")
					}
					page, err := f.s.Inbox(ctx, f.worker.ID, cursor, limit, urgent)
					if err != nil || len(page.Items) == 0 || len(page.Items) > limit {
						t.Fatal("expiry filter produced an empty/truncated intermediate page", page, err)
					}
					for _, message := range page.Items {
						ids = append(ids, message.ID)
						if message.State != "pending" || (message.ID == "message_004" && message.ExpiresAt != now+800) {
							t.Fatal("inbox did not project live legacy deadline", message)
						}
					}
					if page.NextCursor == "" {
						break
					}
					cursor = page.NextCursor
				}
				want := []string{"message_002", "message_004", "message_006", "message_008"}
				if urgent {
					want = []string{"message_002", "message_004", "message_008"}
				}
				if !reflect.DeepEqual(ids, want) {
					t.Fatal("inbox lost or repeated records while filtering expiry", ids, want)
				}
			})
		}
	}
	first, err := f.s.Inbox(ctx, f.worker.ID, "", 1, false)
	if err != nil || first.NextCursor == "" {
		t.Fatal("missing cursor", first, err)
	}
	_, err = f.s.Inbox(ctx, f.other.ID, first.NextCursor, 1, false)
	assertCode(t, err, domain.Conflict)
	_, err = f.s.Inbox(ctx, f.worker.ID, first.NextCursor, 1, true)
	assertCode(t, err, domain.Conflict)
	f.now = f.now.Add(time.Second)
	page, err := f.s.Inbox(ctx, f.worker.ID, first.NextCursor, 200, false)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "message_004" || page.Items[1].ID != "message_006" || page.NextCursor != "" {
		t.Fatal("clock advance between pages revived or lost records", page, err)
	}
}

func TestAuthenticationRefreshPreservesActorIdentityFences(t *testing.T) {
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "refresh target", 4801)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	targetCtx := securityOwnerContext(t, f.s, target)
	req := domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "explicit fenced refresh"}
	message, err := f.s.RequestAuthentication(founderCtx, f.founder.AgentID, target.AgentID, req)
	if err != nil || message.RecipientID != target.AgentID {
		t.Fatal("distinct target was incorrectly checked as the certificate actor", message, err)
	}
	if _, err := f.s.Message(targetCtx, target.AgentID, message.ID); err != nil {
		t.Fatal("fenced target could not read private refresh control", err)
	}
	_, err = f.s.RequestAuthentication(targetCtx, f.founder.AgentID, target.AgentID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RequestAuthentication(founderCtx, target.AgentID, target.AgentID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "spoofed actor"})
	assertCode(t, err, domain.Denied)
	securityOwnerContext(t, f.s, f.founder)
	ci := founderCtx.Value(identityFenceKey{}).(domain.CertificateIdentity)
	owner, err := f.s.IdentityOwner(context.Background(), ci.BindingID(), ci.OwnerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Me(founderCtx, f.founder.AgentID); err != nil {
		t.Fatal("additional Founder connection retired an existing owner", err)
	}
	if err := f.s.ReleaseIdentityOwner(context.Background(), ci.BindingID(), ci.OwnerGeneration, owner.OwnerKey); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.RequestAuthentication(founderCtx, f.founder.AgentID, target.AgentID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RequestAuthentication(founderCtx, f.founder.AgentID, target.AgentID, domain.AuthenticationRefreshRequest{Command: op(), Provider: "codex", Reason: "stale owner"})
	assertCode(t, err, domain.Denied)
}
