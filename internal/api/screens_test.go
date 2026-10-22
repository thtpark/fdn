package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func apiScreens() []domain.Screen {
	return []domain.Screen{
		{ID: "screen-1", Name: "Virtual display", Scope: "screen", Width: 1920, Height: 1080, Primary: true, Applications: []string{"browser", "terminal"}},
		{ID: "screen-2", Name: "Physical display", Scope: "screen", Width: 1280, Height: 720, Applications: []string{"browser"}},
		{ID: "legacy-desktop", Name: "Verified whole desktop", Scope: "desktop", Width: 1920, Height: 1080, Applications: []string{"browser"}},
	}
}

func screenAPIValue[T any](t *testing.T, envelope Envelope, status int) T {
	t.Helper()
	if status != http.StatusOK || !envelope.OK || envelope.Error != nil {
		t.Fatalf("unexpected API result: %d %+v", status, envelope)
	}
	var value T
	if err := json.Unmarshal(envelope.Data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func screenAPIFault(t *testing.T, envelope Envelope, status int, code domain.Code) {
	t.Helper()
	if status != domain.HTTPStatus(domain.Fail(code, "fixture")) || envelope.OK || envelope.Error == nil || envelope.Error.Code != code || len(envelope.Data) != 0 {
		t.Fatalf("want private %s response; got %d %+v", code, status, envelope)
	}
}

func TestScreensAPIIsSelfOnlyBoundedAndReplayable(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	path := "/v1/agents/" + f.target.ID + "/screens"
	envelope, status := f.request(t, f.founder, http.MethodGet, path, nil)
	if screens := screenAPIValue[[]domain.Screen](t, envelope, status); screens == nil || len(screens) != 0 {
		t.Fatal("headless capability was not an explicit empty array", screens)
	}
	req := domain.ScreensRequest{Command: domain.Command{OperationID: "publish-public-screens"}, Screens: apiScreens()}
	for _, actor := range []domain.Agent{f.founder, f.delegate} {
		envelope, status := f.request(t, actor, http.MethodPost, path, req)
		screenAPIFault(t, envelope, status, domain.Denied)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	published := screenAPIValue[[]domain.Screen](t, envelope, status)
	if envelope.OperationID != req.OperationID || !reflect.DeepEqual(published, req.Screens) {
		t.Fatal("screen observation envelope lost its exact result", envelope)
	}
	for _, actor := range []domain.Agent{f.founder, f.delegate, f.target} {
		envelope, status := f.request(t, actor, http.MethodGet, path, nil)
		if current := screenAPIValue[[]domain.Screen](t, envelope, status); !reflect.DeepEqual(current, published) {
			t.Fatal("public capability was not readable by active agents", current)
		}
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agents/"+f.target.ID, nil)
	current := screenAPIValue[domain.Agent](t, envelope, status)
	current.Screens = nil
	if !reflect.DeepEqual(current, f.target) {
		t.Fatal("screen publication changed authoritative mode/revision", current)
	}
	for _, value := range []any{
		map[string]any{"operation_id": "forged-observation", "screens": apiScreens(), "mode": "manual"},
		map[string]any{"operation_id": "forged-observation", "screens": apiScreens(), "agent_id": f.delegate.ID},
		map[string]any{"operation_id": "forged-observation", "screens": []map[string]any{{"id": "screen-1", "name": "Display", "scope": "screen", "width": 100, "height": 100, "display": ":0"}}},
		map[string]any{"operation_id": "forged-observation", "screens": []map[string]any{{"id": "screen-1", "name": "Display", "scope": "screen", "width": 100, "height": 100, "screenshot": "private"}}},
		domain.ScreensRequest{Command: domain.Command{OperationID: "unsafe-capability"}, Screens: []domain.Screen{{ID: "screen-1", Name: "Display", Scope: "screen", Width: 100, Height: 100, Applications: []string{"browser --arbitrary-command"}}}},
		domain.ScreensRequest{Command: domain.Command{OperationID: "unbounded-capability"}, Screens: make([]domain.Screen, 17)},
	} {
		envelope, status := f.request(t, f.target, http.MethodPost, path, value)
		screenAPIFault(t, envelope, status, domain.Invalid)
	}
	clear := domain.ScreensRequest{Command: domain.Command{OperationID: "clear-headless-capability"}}
	envelope, status = f.request(t, f.target, http.MethodPost, path, clear)
	if screens := screenAPIValue[[]domain.Screen](t, envelope, status); screens == nil || len(screens) != 0 {
		t.Fatal("headless observation failed to clear inventory", screens)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	if replay := screenAPIValue[[]domain.Screen](t, envelope, status); !reflect.DeepEqual(replay, published) {
		t.Fatal("screen publication replay lost the original result", replay)
	}
	envelope, status = f.request(t, f.founder, http.MethodGet, path, nil)
	if screens := screenAPIValue[[]domain.Screen](t, envelope, status); len(screens) != 0 {
		t.Fatal("old publication replay restored obsolete display capabilities", screens)
	}
	changed := req
	changed.Screens = nil
	envelope, status = f.request(t, f.target, http.MethodPost, path, changed)
	screenAPIFault(t, envelope, status, domain.Conflict)
	if _, err := f.store.Revoke(context.Background(), f.founder.ID, f.target.ID, domain.AgentRequest{Command: domain.Command{OperationID: "revoke-screen-publisher"}}); err != nil {
		t.Fatal(err)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	screenAPIFault(t, envelope, status, domain.Denied)
	envelope, status = f.request(t, f.founder, http.MethodGet, path, nil)
	screenAPIFault(t, envelope, status, domain.Denied)
}

func TestAssistanceHelpAPISelfRoutingPrivacyAndNoAuthority(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	path := "/v1/assistance/requests"
	req := domain.AssistanceHelpRequest{Command: domain.Command{OperationID: "target-asks-human-help"}, Mode: "vnc", Purpose: "authentication", Provider: "codex", ProjectID: f.project.ID, ScreenID: "screen-1", ApplicationID: "browser", Reason: "Human browser sign-in is needed on this display"}
	envelope, status := f.request(t, f.target, http.MethodPost, path, req)
	screenAPIFault(t, envelope, status, domain.Prerequisite)
	envelope, status = f.request(t, f.target, http.MethodPost, "/v1/agents/"+f.target.ID+"/screens", domain.ScreensRequest{Command: domain.Command{OperationID: "publish-help-capability"}, Screens: apiScreens()})
	screenAPIValue[[]domain.Screen](t, envelope, status)
	for _, field := range []string{"sender_id", "target_id", "requester_id", "agent_id", "expires_at", "origin", "kind", "response_requested", "command", "path", "screenshot", "grant_id", "body", "id"} {
		var forged map[string]any
		data, _ := json.Marshal(req)
		if err := json.Unmarshal(data, &forged); err != nil {
			t.Fatal(err)
		}
		forged[field] = "forged"
		forged["operation_id"] = "forged-help-" + field
		envelope, status := f.request(t, f.target, http.MethodPost, path, forged)
		screenAPIFault(t, envelope, status, domain.Invalid)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	help := screenAPIValue[domain.AssistanceHelp](t, envelope, status)
	if envelope.OperationID != req.OperationID || help.AgentID != f.target.ID || len(help.Messages) != 2 || help.ExpiresAt != help.Messages[0].CreatedAt+900 {
		t.Fatal("raw help API changed caller identity or current recipients", help)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, "/v1/messages", map[string]any{"operation_id": "cannot-forge-help-namespace", "recipient_id": f.founder.ID, "body": "Untrusted direct message", "origin": "service", "kind": "session", "id": help.Messages[0].ID})
	screenAPIFault(t, envelope, status, domain.Invalid)
	for _, message := range help.Messages {
		if message.SenderID != f.target.ID || message.ResponseRequest || message.Kind != "session" || message.ExpiresAt != help.ExpiresAt || !strings.HasPrefix(message.ID, "message_help_") || (message.RecipientID != f.founder.ID && message.RecipientID != f.delegate.ID) {
			t.Fatal("raw help API accepted forged control fields", message)
		}
		recipient := f.founder
		outsider := f.delegate
		if message.RecipientID == f.delegate.ID {
			recipient, outsider = f.delegate, f.founder
		}
		for _, actor := range []domain.Agent{f.target, recipient} {
			envelope, status := f.request(t, actor, http.MethodGet, "/v1/messages/"+message.ID, nil)
			if got := screenAPIValue[domain.Message](t, envelope, status); !reflect.DeepEqual(got, message) {
				t.Fatal("help message was not immutable and private to its parties", got)
			}
		}
		envelope, status := f.request(t, outsider, http.MethodGet, "/v1/messages/"+message.ID, nil)
		screenAPIFault(t, envelope, status, domain.Denied)
	}
	envelope, status = f.request(t, f.founder, http.MethodGet, "/v1/assistance", nil)
	if grants := screenAPIValue[[]domain.AssistanceGrant](t, envelope, status); len(grants) != 0 {
		t.Fatal("asking for help issued a grant", grants)
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/sessions", nil)
	if sessions := screenAPIValue[[]domain.Session](t, envelope, status); len(sessions) != 0 {
		t.Fatal("asking for help started a session", sessions)
	}
	envelope, status = f.request(t, f.target, http.MethodGet, "/v1/agents/"+f.target.ID, nil)
	agent := screenAPIValue[domain.Agent](t, envelope, status)
	if agent.Revision != f.target.Revision || agent.Mode != f.target.Mode {
		t.Fatal("help changed mode or work authority", agent)
	}
	if _, err := f.store.Delegate(context.Background(), f.founder.ID, f.project.ID, domain.GrantRequest{Command: domain.Command{OperationID: "remove-help-delegate", ExpectedRevision: f.project.Revision}, AgentID: f.delegate.ID, Revoke: true}); err != nil {
		t.Fatal(err)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	if replayed := screenAPIValue[domain.AssistanceHelp](t, envelope, status); !reflect.DeepEqual(replayed, help) {
		t.Fatal("current delegate change rewrote existing private receipt", replayed)
	}
	fresh := req
	fresh.OperationID = "target-asks-current-help"
	envelope, status = f.request(t, f.target, http.MethodPost, path, fresh)
	if current := screenAPIValue[domain.AssistanceHelp](t, envelope, status); len(current.Messages) != 1 || current.Messages[0].RecipientID != f.founder.ID {
		t.Fatal("new help routed to a departed delegate", current)
	}
	if _, err := f.store.Revoke(context.Background(), f.founder.ID, f.target.ID, domain.AgentRequest{Command: domain.Command{OperationID: "revoke-help-requester"}}); err != nil {
		t.Fatal(err)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, path, req)
	screenAPIFault(t, envelope, status, domain.Denied)
}

func TestScreenAwareAssistanceAPIBindsScopeAndRelay(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	screensPath := "/v1/agents/" + f.target.ID + "/screens"
	envelope, status := f.request(t, f.target, http.MethodPost, screensPath, domain.ScreensRequest{Command: domain.Command{OperationID: "publish-session-capability"}, Screens: apiScreens()})
	screenAPIValue[[]domain.Screen](t, envelope, status)
	grantReq := domain.AssistanceGrantRequest{Command: domain.Command{OperationID: "screen-bound-grant"}, RequesterID: f.delegate.ID, TargetID: f.target.ID, ProjectID: f.project.ID, Mode: "vnc", Purpose: "terminal", Reason: "Exact selected-screen access", ScreenID: "screen-1", ApplicationID: "browser"}
	envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/assistance", grantReq)
	screenAPIFault(t, envelope, status, domain.Denied)
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/assistance", grantReq)
	grant := screenAPIValue[domain.AssistanceGrant](t, envelope, status)
	if grant.ScreenID != grantReq.ScreenID || grant.ApplicationID != grantReq.ApplicationID || grant.DesktopFallback {
		t.Fatal("raw grant API discarded exact display scope", grant)
	}
	req := domain.SessionRequest{Command: domain.Command{OperationID: "screen-bound-session"}, GrantID: grant.ID, TargetID: f.target.ID, ProjectID: f.project.ID, Mode: "vnc", Purpose: "terminal", Reason: "Human selected display", ExpiresAt: time.Now().Unix() + 120, ScreenID: grant.ScreenID, ApplicationID: grant.ApplicationID}
	for _, change := range []func(*domain.SessionRequest){
		func(r *domain.SessionRequest) { r.ScreenID = "screen-2" },
		func(r *domain.SessionRequest) { r.ApplicationID = "terminal" },
		func(r *domain.SessionRequest) { r.ApplicationID = "" },
		func(r *domain.SessionRequest) { r.ScreenID, r.DesktopFallback = "legacy-desktop", true },
	} {
		changed := req
		changed.Command.OperationID = domain.NewID("changed-scope")
		change(&changed)
		envelope, status := f.request(t, f.delegate, http.MethodPost, "/v1/sessions", changed)
		screenAPIFault(t, envelope, status, domain.Denied)
	}
	envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/sessions", req)
	session := screenAPIValue[domain.Session](t, envelope, status)
	if session.ScreenID != grant.ScreenID || session.ApplicationID != grant.ApplicationID || session.OpenRequested {
		t.Fatal("session was opened implicitly or scope changed", session)
	}
	envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/sessions/"+session.ID, map[string]any{"operation_id": "cannot-change-session-screen", "expected_revision": session.Revision, "action": "open", "screen_id": "screen-2"})
	screenAPIFault(t, envelope, status, domain.Invalid)
	envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/sessions/"+session.ID, domain.SessionChange{Command: domain.Command{OperationID: "open-screen-session", ExpectedRevision: session.Revision}, Action: "open"})
	session = screenAPIValue[domain.Session](t, envelope, status)
	if _, err := f.store.SessionAccess(context.Background(), f.target.ID, session.ID, "target"); err != nil {
		t.Fatal("matching scoped raw API session did not use existing relay authority", err)
	}
	desktopReq := grantReq
	desktopReq.Command, desktopReq.ScreenID = domain.Command{OperationID: "unapproved-desktop-grant"}, "legacy-desktop"
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/assistance", desktopReq)
	screenAPIFault(t, envelope, status, domain.Denied)
	desktopReq.Command, desktopReq.DesktopFallback = domain.Command{OperationID: "explicit-desktop-grant"}, true
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/assistance", desktopReq)
	desktop := screenAPIValue[domain.AssistanceGrant](t, envelope, status)
	if desktop.ScreenID != "legacy-desktop" || !desktop.DesktopFallback {
		t.Fatal("explicit legacy desktop observation/fallback was not preserved", desktop)
	}
	envelope, status = f.request(t, f.target, http.MethodPost, screensPath, domain.ScreensRequest{Command: domain.Command{OperationID: "display-disappeared"}})
	screenAPIValue[[]domain.Screen](t, envelope, status)
	for _, side := range []struct {
		actor domain.Agent
		name  string
	}{{f.target, "target"}, {f.delegate, "requester"}} {
		envelope, status := f.request(t, side.actor, http.MethodGet, "/v1/sessions/"+session.ID+"/relay/"+side.name, nil)
		screenAPIFault(t, envelope, status, domain.Prerequisite)
	}
	grantReq.Command.OperationID = "headless-new-grant"
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/assistance", grantReq)
	screenAPIFault(t, envelope, status, domain.Prerequisite)
}
