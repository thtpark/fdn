package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestScreenAndHelpObservationCurrentIdentityFencesAndRecovery(t *testing.T) {
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "display target", 4101)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	targetCtx := securityOwnerContext(t, f.s, target)
	req := domain.ScreensRequest{Command: op(), Screens: assistanceScreens()}
	published, err := f.s.PublishScreens(targetCtx, target.AgentID, req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.PublishScreens(founderCtx, target.AgentID, req)
	assertCode(t, err, domain.Denied)
	helpReq := domain.AssistanceHelpRequest{Command: op(), Mode: "vnc", Purpose: "authentication", Provider: "codex", ScreenID: "screen-1", ApplicationID: "browser", Reason: "Human help under target identity only"}
	help, err := f.s.RequestAssistanceHelp(targetCtx, target.AgentID, helpReq)
	if err != nil || help.AgentID != target.AgentID || len(help.Messages) != 1 || help.Messages[0].RecipientID != f.founder.AgentID {
		t.Fatal("help subjects were treated as callers or caller impersonated another target", help, err)
	}
	_, err = f.s.RequestAssistanceHelp(founderCtx, target.AgentID, helpReq)
	assertCode(t, err, domain.Denied)
	currentCtx := securityOwnerContext(t, f.s, target)
	_, err = f.s.PublishScreens(targetCtx, target.AgentID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Screens(targetCtx, target.AgentID, target.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RequestAssistanceHelp(targetCtx, target.AgentID, helpReq)
	assertCode(t, err, domain.Denied)
	if replayed, err := f.s.PublishScreens(currentCtx, target.AgentID, req); err != nil || !reflect.DeepEqual(replayed, published) {
		t.Fatal("current owner could not reconcile an earlier exact observation", replayed, err)
	}
	if replayed, err := f.s.RequestAssistanceHelp(currentCtx, target.AgentID, helpReq); err != nil || !reflect.DeepEqual(replayed, help) {
		t.Fatal("current owner could not reconcile an earlier private help receipt", replayed, err)
	}
	fresh := securityPublic(t, "recovered display target")
	recovered, err := f.s.RecoverIdentity(founderCtx, f.founder.AgentID, target.AgentID, domain.RecoverIdentityRequest{Command: op(), Identity: fresh.public, Reason: "Explicit fixture fresh-key recovery"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, target.UID)
	recoveredCtx := securityOwnerContext(t, f.s, recovered)
	_, err = f.s.PublishScreens(currentCtx, target.AgentID, req)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RequestAssistanceHelp(currentCtx, target.AgentID, helpReq)
	assertCode(t, err, domain.Denied)
	if screens, err := f.s.Screens(recoveredCtx, target.AgentID, target.AgentID); err != nil || len(screens) != 0 {
		t.Fatal("fresh-key recovery inherited unverified old-computer display capabilities", screens, err)
	}
	newHelp := helpReq
	newHelp.Command = op()
	_, err = f.s.RequestAssistanceHelp(recoveredCtx, target.AgentID, newHelp)
	assertCode(t, err, domain.Prerequisite)
	if _, err := f.s.PublishScreens(recoveredCtx, target.AgentID, req); err != nil {
		t.Fatal("current identity could not reconcile old observation receipt", err)
	}
	if screens, err := f.s.Screens(recoveredCtx, target.AgentID, target.AgentID); err != nil || len(screens) != 0 {
		t.Fatal("old operation replay resurrected recovered capability", screens, err)
	}
	req.Command = op()
	if _, err := f.s.PublishScreens(recoveredCtx, target.AgentID, req); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if screens, err := reopened.Screens(recoveredCtx, target.AgentID, target.AgentID); err != nil || !reflect.DeepEqual(screens, published) {
		t.Fatal("committed public observations did not survive store reopen", screens, err)
	}
	if err := f.s.QuarantineRestoredIdentities(context.Background()); err != nil {
		t.Fatal(err)
	}
	agent, err := get[domain.Agent](context.Background(), f.s.db, "agents", target.AgentID)
	if err != nil || len(agent.Screens) != 0 {
		t.Fatal("restore retained stale display observations", agent, err)
	}
}

func TestTmuxAssistanceRejectsDisplayScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	grant, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Unchanged headless tmux authorization"})
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []struct {
		screen, application string
		fallback            bool
	}{{screen: "screen-1"}, {application: "browser"}, {fallback: true}} {
		_, err := f.s.CreateAssistance(ctx, f.founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: f.founder.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Invalid tmux display scope", ScreenID: selection.screen, ApplicationID: selection.application, DesktopFallback: selection.fallback})
		assertCode(t, err, domain.Invalid)
		_, err = f.s.RequestSession(ctx, f.founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: f.worker.ID, Mode: "tmux", Purpose: "terminal", Reason: "Invalid tmux display scope", ExpiresAt: f.now.Unix() + 120, ScreenID: selection.screen, ApplicationID: selection.application, DesktopFallback: selection.fallback})
		assertCode(t, err, domain.Invalid)
		_, err = f.s.RequestAssistanceHelp(ctx, f.worker.ID, domain.AssistanceHelpRequest{Command: op(), Mode: "tmux", Purpose: "terminal", Reason: "Invalid tmux display scope", ScreenID: selection.screen, ApplicationID: selection.application, DesktopFallback: selection.fallback})
		assertCode(t, err, domain.Invalid)
	}
}
