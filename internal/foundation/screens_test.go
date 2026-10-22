package foundation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func assistanceScreens() []domain.Screen {
	return []domain.Screen{
		{ID: "screen-1", Name: "Virtual screen 1", Scope: "screen", Width: 1920, Height: 1080, Primary: true, Applications: []string{"browser", "terminal"}},
		{ID: "screen-2", Name: "Physical screen 2", Scope: "screen", Width: 1280, Height: 720, Applications: []string{"browser", "terminal"}},
		{ID: "desktop", Name: "Whole desktop", Scope: "desktop", Width: 3200, Height: 1080, Applications: []string{"browser"}},
	}
}

func publishAssistanceScreens(t *testing.T, f *fixture, screens []domain.Screen) {
	t.Helper()
	if _, err := f.s.PublishScreens(context.Background(), f.worker.ID, domain.ScreensRequest{Command: op(), Screens: screens}); err != nil {
		t.Fatal(err)
	}
}

func TestScreenPublicationObservationReplayAndAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.s.Screens(ctx, f.other.ID, f.worker.ID)
	if err != nil || initial == nil || len(initial) != 0 {
		t.Fatal("legacy/headless inventory must be explicitly empty", initial, err)
	}
	req := domain.ScreensRequest{Command: op(), Screens: assistanceScreens()}
	req.ExpectedRevision = 9999
	published, err := f.s.PublishScreens(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(published, req.Screens) {
		t.Fatal("public display observations were not retained", published, err)
	}
	worker, err := f.s.Agent(ctx, f.worker.ID, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker.Screens = nil
	if !reflect.DeepEqual(worker, f.worker) {
		t.Fatal("display observation changed mode, work revision, identity or heartbeat", worker)
	}
	paused, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Founder pause concurrent with observations"})
	if err != nil {
		t.Fatal("screen publication invalidated the authoritative mode revision", err)
	}
	updated := assistanceScreens()
	updated[0].Name = "Renamed screen"
	if _, err := f.s.PublishScreens(ctx, f.worker.ID, domain.ScreensRequest{Command: op(), Screens: updated}); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.PublishScreens(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, published) {
		t.Fatal("screen operation did not replay its original bounded result", replayed, err)
	}
	current, err := f.s.Screens(ctx, f.founder.ID, f.worker.ID)
	if err != nil || !reflect.DeepEqual(current, updated) {
		t.Fatal("replay overwrote a newer observation", current, err)
	}
	worker, err = f.s.Agent(ctx, f.worker.ID, f.worker.ID)
	if err != nil || worker.Mode != paused.Mode || worker.Revision != paused.Revision || worker.ModeReason != paused.ModeReason {
		t.Fatal("observation or replay overwrote a Founder mode decision", worker, err)
	}
	changed := req
	changed.Screens = nil
	_, err = f.s.PublishScreens(ctx, f.worker.ID, changed)
	assertCode(t, err, domain.Conflict)
	bad := assistanceScreens()
	bad[0].Applications = []string{"browser --arbitrary-command"}
	_, err = f.s.PublishScreens(ctx, f.worker.ID, domain.ScreensRequest{Command: op(), Screens: bad})
	assertCode(t, err, domain.Invalid)
	cleared, err := f.s.PublishScreens(ctx, f.worker.ID, domain.ScreensRequest{Command: op()})
	if err != nil || cleared == nil || len(cleared) != 0 {
		t.Fatal("headless/offline publication did not clear capabilities", cleared, err)
	}
	if _, err := f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.PublishScreens(ctx, f.worker.ID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Screens(ctx, f.other.ID, f.worker.ID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Screens(ctx, f.worker.ID, f.other.ID)
	assertCode(t, err, domain.Denied)
}

func TestScreenAwareAssistanceBindingAndRelayRecheck(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	grantReq := domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "vnc", Purpose: "authentication", Provider: "copilot", Reason: "Explicit selected-screen assistance", ScreenID: "screen-1", ApplicationID: "browser"}
	_, err := f.s.CreateAssistance(ctx, f.founder.ID, grantReq)
	assertCode(t, err, domain.Prerequisite)
	publishAssistanceScreens(t, f, assistanceScreens())
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, grantReq)
	if err != nil || grant.ScreenID != grantReq.ScreenID || grant.ApplicationID != grantReq.ApplicationID || grant.DesktopFallback {
		t.Fatal("grant lost selected-screen scope", grant, err)
	}
	req := domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: grant.TargetID, Mode: grant.Mode, Purpose: grant.Purpose, Provider: grant.Provider, Reason: "Explicit human open", ExpiresAt: f.now.Unix() + 120, ScreenID: grant.ScreenID, ApplicationID: grant.ApplicationID}
	for _, change := range []func(*domain.SessionRequest){
		func(r *domain.SessionRequest) { r.ScreenID = "screen-2" },
		func(r *domain.SessionRequest) { r.ApplicationID = "terminal" },
		func(r *domain.SessionRequest) { r.ApplicationID = "" },
		func(r *domain.SessionRequest) { r.ScreenID, r.DesktopFallback = "desktop", true },
		func(r *domain.SessionRequest) { r.DesktopFallback = true },
	} {
		changed := req
		changed.Command = op()
		change(&changed)
		_, err := f.s.RequestSession(ctx, f.founder.ID, changed)
		assertCode(t, err, domain.Denied)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, req)
	if err != nil || session.ScreenID != grant.ScreenID || session.ApplicationID != grant.ApplicationID || session.DesktopFallback || session.State != "consented" || session.OpenRequested {
		t.Fatal("session lost grant scope or opened implicitly", session, err)
	}
	session, err = f.s.ChangeSession(ctx, f.founder.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	for _, party := range []struct{ id, side string }{{f.founder.ID, "requester"}, {f.worker.ID, "target"}} {
		if _, err := f.s.SessionAccess(ctx, party.id, session.ID, party.side); err != nil {
			t.Fatal("current matching scope could not use existing relay policy", err)
		}
	}
	for _, screens := range [][]domain.Screen{
		assistanceScreens()[1:],
		{{ID: "screen-1", Name: "Screen without app", Scope: "screen", Width: 100, Height: 100, Applications: []string{"terminal"}}},
		nil,
	} {
		publishAssistanceScreens(t, f, screens)
		_, err := f.s.SessionAccess(ctx, f.founder.ID, session.ID, "requester")
		assertCode(t, err, domain.Prerequisite)
		fresh := req
		fresh.Command = op()
		_, err = f.s.RequestSession(ctx, f.founder.ID, fresh)
		assertCode(t, err, domain.Prerequisite)
	}
	widened := assistanceScreens()
	widened[0].Scope = "desktop"
	publishAssistanceScreens(t, f, widened)
	_, err = f.s.SessionAccess(ctx, f.founder.ID, session.ID, "requester")
	assertCode(t, err, domain.Denied)
	fresh := req
	fresh.Command, fresh.DesktopFallback = op(), true
	_, err = f.s.RequestSession(ctx, f.founder.ID, fresh)
	assertCode(t, err, domain.Denied)
	publishAssistanceScreens(t, f, assistanceScreens())
	if _, err := f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target"); err != nil {
		t.Fatal("same exact screen/app grant could not reconnect", err)
	}
	desktopReq := grantReq
	desktopReq.Command, desktopReq.ScreenID = op(), "desktop"
	_, err = f.s.CreateAssistance(ctx, f.founder.ID, desktopReq)
	assertCode(t, err, domain.Denied)
	desktopReq.Command, desktopReq.DesktopFallback = op(), true
	desktop, err := f.s.CreateAssistance(ctx, f.founder.ID, desktopReq)
	if err != nil || !desktop.DesktopFallback {
		t.Fatal("explicit whole-desktop fallback was not supported", desktop, err)
	}
	desktopSessionReq := req
	desktopSessionReq.Command, desktopSessionReq.GrantID, desktopSessionReq.ScreenID, desktopSessionReq.DesktopFallback = op(), desktop.ID, desktop.ScreenID, true
	if _, err := f.s.RequestSession(ctx, f.founder.ID, desktopSessionReq); err != nil {
		t.Fatal("explicit fallback grant did not authorize its exact session", err)
	}
	if _, err := f.s.RevokeAssistance(ctx, f.founder.ID, grant.ID, domain.AssistanceRevoke{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: grant.Revision}, Reason: "End display authority"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target")
	assertCode(t, err, domain.Denied)
}

func TestScreenAwareAssistanceLegacyUnscopedVNCFailsClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	publishAssistanceScreens(t, f, assistanceScreens())
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Seed immutable legacy fixture"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Seed legacy session", ExpiresAt: f.now.Unix() + 120})
	if err != nil {
		t.Fatal(err)
	}
	grant.Mode, session.Mode, session.OpenRequested = "vnc", "vnc", true
	for _, record := range []struct {
		table, id string
		value     any
	}{{"assistance_grants", grant.ID, grant}, {"sessions", session.ID, session}} {
		data, err := encode(record.value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.db.Exec("UPDATE "+record.table+" SET data=? WHERE id=?", data, record.id); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.s.SessionAccess(ctx, f.founder.ID, session.ID, "requester")
	assertCode(t, err, domain.Prerequisite)
	_, err = f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, Mode: "vnc", Purpose: "terminal", Reason: "Cannot upgrade legacy authority silently", ExpiresAt: f.now.Unix() + 120, ScreenID: "desktop", DesktopFallback: true})
	assertCode(t, err, domain.Denied)
	retained, err := f.s.Assistance(ctx, f.founder.ID, grant.ID)
	if err != nil || retained.ScreenID != "" || retained.DesktopFallback {
		t.Fatal("legacy evidence was rewritten instead of requiring fresh explicit scope", retained, err)
	}
}

func TestScreenAwareAssistanceCurrentProjectAndIdentityAuthority(t *testing.T) {
	for _, mutation := range []string{"delegate-removed", "project-archived", "requester-revoked", "target-revoked", "founder-banned", "expired"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			publishAssistanceScreens(t, f, assistanceScreens())
			project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Current display authority"})
			if err != nil {
				t.Fatal(err)
			}
			project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.other.ID, TargetID: f.worker.ID, ProjectID: project.ID, Mode: "vnc", Purpose: "terminal", Reason: "Project-scoped selected screen", ScreenID: "screen-1"})
			if err != nil {
				t.Fatal(err)
			}
			session, err := f.s.RequestSession(ctx, f.other.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, ProjectID: project.ID, Mode: "vnc", Purpose: "terminal", Reason: "Current project access", ExpiresAt: f.now.Unix() + 120, ScreenID: "screen-1"})
			if err != nil {
				t.Fatal(err)
			}
			session, err = f.s.ChangeSession(ctx, f.other.ID, session.ID, domain.SessionChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.SessionAccess(ctx, f.worker.ID, session.ID, "target"); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "delegate-removed":
				_, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true})
			case "project-archived":
				archived := true
				_, err = f.s.ChangeProject(ctx, f.founder.ID, project.ID, domain.ProjectChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, Archived: &archived, Reason: "Archive assistance context"})
			case "requester-revoked":
				_, err = f.s.Revoke(ctx, f.founder.ID, f.other.ID, domain.AgentRequest{Command: op()})
			case "target-revoked":
				_, err = f.s.Revoke(ctx, f.founder.ID, f.worker.ID, domain.AgentRequest{Command: op()})
			case "founder-banned":
				err = f.s.BanIdentity(ctx, f.founder.ID, "Fixture Founder identity retirement")
			case "expired":
				f.now = time.Unix(session.ExpiresAt, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			actor, side := f.other.ID, "requester"
			if mutation == "requester-revoked" {
				actor, side = f.worker.ID, "target"
			}
			_, err = f.s.SessionAccess(ctx, actor, session.ID, side)
			code := domain.Denied
			if mutation == "expired" {
				code = domain.Prerequisite
			}
			assertCode(t, err, code)
		})
	}
}
