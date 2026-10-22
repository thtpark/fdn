//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func TestScreenScopedHelpAndSessionsKeepAgentIdentityAndGrantBoundaries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := requestedHelpProject(t, h)
	screens := []domain.Screen{
		{ID: "left", Name: "Left application screen", Scope: "screen", Width: 1280, Height: 800, Primary: true, Applications: []string{"browser"}},
		{ID: "right", Name: "Right application screen", Scope: "screen", Width: 1280, Height: 800, Applications: []string{"terminal"}},
	}
	publish := domain.ScreensRequest{Command: command(), Screens: screens}
	if _, err := api.Call[[]domain.Screen](ctx, h.worker, "POST", api.Path("agents", h.otherAgent.ID, "screens"), publish); err == nil {
		t.Fatal("an agent published another agent's display inventory")
	}
	if _, err := api.Call[[]domain.Screen](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "screens"), publish); err != nil {
		t.Fatal(err)
	}
	helpInput := domain.AssistanceHelpRequest{Command: command(), Mode: "vnc", Purpose: "authentication", Provider: "codex", ProjectID: project.ID,
		ScreenID: "left", ApplicationID: "browser", Reason: "Founder help on the selected browser screen"}
	help, err := api.Call[domain.AssistanceHelp](ctx, h.worker, "POST", "/v1/assistance/requests", helpInput)
	if err != nil || len(help.Messages) != 2 || help.AgentID != h.workerAgent.ID {
		t.Fatal("target's display help did not reach Founder/current delegate", help, err)
	}
	replay, err := api.Call[domain.AssistanceHelp](ctx, h.worker, "POST", "/v1/assistance/requests", helpInput)
	if err != nil || replay.ID != help.ID || len(replay.Messages) != len(help.Messages) {
		t.Fatal("help replay created a new human-help request", replay, err)
	}
	for _, message := range help.Messages {
		if message.SenderID != h.workerAgent.ID || message.Kind != "session" || message.ResponseRequest || message.ExpiresAt-message.CreatedAt != 900 {
			t.Fatal("VNC help was not private/protected and bounded", message)
		}
	}
	sessions, err := api.Call[[]domain.Session](ctx, h.worker, "GET", "/v1/sessions", nil)
	if err != nil || len(sessions) != 0 {
		t.Fatal("a help notice implicitly opened desktop access", sessions, err)
	}
	grant, err := api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", "/v1/assistance", domain.AssistanceGrantRequest{Command: command(), RequesterID: h.founderAgent.ID,
		TargetID: h.workerAgent.ID, ProjectID: project.ID, Mode: "vnc", Purpose: "authentication", Provider: "codex", ScreenID: "left", ApplicationID: "browser", Reason: "Founder authorizes selected-screen assistance"})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SessionRequest{Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, ProjectID: project.ID, Mode: "vnc", Purpose: "authentication",
		Provider: "codex", ScreenID: "right", ApplicationID: "terminal", Reason: "Attempt to substitute a different desktop", ExpiresAt: time.Now().Unix() + 120}
	if _, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", request); err == nil {
		t.Fatal("selected-screen authorization widened to another screen/application")
	}
	request.Command, request.ScreenID, request.ApplicationID = command(), "left", "browser"
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", request)
	if err != nil || session.ScreenID != "left" || session.DesktopFallback || session.ApplicationID != "browser" || session.State != "consented" {
		t.Fatal("VNC session did not preserve the authorized screen/application", session, err)
	}
	open := domain.SessionChange{Command: command(), Action: "open"}
	open.ExpectedRevision = session.Revision
	if _, err := api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), open); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[[]domain.Screen](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "screens"), domain.ScreensRequest{Command: command(), Screens: []domain.Screen{}}); err != nil {
		t.Fatal(err)
	}
	request.Command = command()
	if _, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", request); err == nil {
		t.Fatal("headless/unavailable display retained live VNC admission")
	}
}

func TestDesktopFallbackIsNeverImplicitlyExpandedFromAScreenGrant(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	screen := domain.Screen{ID: "desktop", Name: "Existing host desktop", Scope: "desktop", Width: 1920, Height: 1080, Primary: true, Applications: []string{"browser"}}
	if _, err := api.Call[[]domain.Screen](ctx, h.worker, "POST", api.Path("agents", h.workerAgent.ID, "screens"), domain.ScreensRequest{Command: command(), Screens: []domain.Screen{screen}}); err != nil {
		t.Fatal(err)
	}
	input := domain.AssistanceHelpRequest{Command: command(), Mode: "vnc", Purpose: "terminal", ScreenID: screen.ID, Reason: "Need human help with an existing desktop"}
	if _, err := api.Call[domain.AssistanceHelp](ctx, h.worker, "POST", "/v1/assistance/requests", input); err == nil {
		t.Fatal("desktop scope was silently labelled as an isolated screen")
	}
	input.Command, input.DesktopFallback = command(), true
	if _, err := api.Call[domain.AssistanceHelp](ctx, h.worker, "POST", "/v1/assistance/requests", input); err != nil {
		t.Fatal("explicit whole-desktop help was unavailable", err)
	}
}
