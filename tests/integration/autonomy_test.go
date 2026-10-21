//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func grantFor(t *testing.T, h *harness, requester, target, mode, purpose, project string) domain.AssistanceGrant {
	t.Helper()
	grant, err := api.Call[domain.AssistanceGrant](context.Background(), h.founder, "POST", "/v1/assistance", domain.AssistanceGrantRequest{
		Command: command(), RequesterID: requester, TargetID: target, Mode: mode, Purpose: purpose, ProjectID: project, Reason: "Founder authorizes persistent scoped fixture assistance"})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestFounderOnlyModeControlAndScopedDelegateAssistance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, client := range []*api.Client{h.worker, h.other} {
		req := domain.ModeRequest{Command: command(), Mode: "manual", Reason: "untrusted local pause"}
		req.ExpectedRevision = h.workerAgent.Revision
		if _, err := api.Call[domain.Agent](ctx, client, "POST", api.Path("agents", h.workerAgent.ID, "mode"), req); err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("ordinary agent changed automation authority", err)
		}
	}
	pause := domain.ModeRequest{Command: command(), Mode: "manual", Reason: "explicit Founder pause"}
	pause.ExpectedRevision = h.workerAgent.Revision
	paused, err := api.Call[domain.Agent](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "mode"), pause)
	if err != nil || paused.Mode != "manual" || paused.ModeRequestedBy != h.founderAgent.ID {
		t.Fatal(paused, err)
	}
	grant := grantFor(t, h, h.otherAgent.ID, h.workerAgent.ID, "tmux", "authentication", "")
	if _, err := api.Call[domain.AssistanceGrant](ctx, h.worker, "POST", "/v1/assistance", domain.AssistanceGrantRequest{
		Command: command(), RequesterID: h.workerAgent.ID, TargetID: h.otherAgent.ID, Mode: "tmux", Purpose: "authentication", Reason: "self authorization"}); err == nil {
		t.Fatal("non-Founder issued persistent access")
	}
	request := domain.SessionRequest{Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, Mode: "tmux", Purpose: "authentication", Provider: "codex", Reason: "AI delegate coordinates protected assistance", ExpiresAt: time.Now().Unix() + 90}
	session, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", request)
	if err != nil || session.State != "consented" || session.OpenRequested {
		t.Fatal("autonomous target was made to wait for a local human", session, err)
	}
	open := domain.SessionChange{Command: command(), Action: "open"}
	open.ExpectedRevision = session.Revision
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", api.Path("sessions", session.ID), open); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RetireSessions(ctx); err != nil {
		t.Fatal(err)
	}
	retained, err := api.Call[domain.AssistanceGrant](ctx, h.other, "GET", api.Path("assistance", grant.ID), nil)
	if err != nil || !retained.Active || retained.Revision != 1 {
		t.Fatal("relay restart consumed permanent authorization", retained, err)
	}
	request.Command = command()
	reconnected, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", request)
	if err != nil || reconnected.State != "consented" || reconnected.ID == session.ID {
		t.Fatal("reconnect required repeated approval", reconnected, err)
	}
	request.Command = command()
	request.Provider = "copilot"
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", request); err != nil {
		t.Fatal("provider-unrestricted authentication grant did not cover supported sign-in", err)
	}
	mode := domain.ModeRequest{Command: command(), Mode: "ai", Reason: "delegate tries to bypass Founder pause"}
	mode.ExpectedRevision = paused.Revision
	if _, err := api.Call[domain.Agent](ctx, h.other, "POST", api.Path("agents", h.workerAgent.ID, "mode"), mode); err == nil {
		t.Fatal("assistance grant conferred automation authority")
	}
	revoke := domain.AssistanceRevoke{Command: command(), Reason: "Founder withdraws retained access"}
	revoke.ExpectedRevision = retained.Revision
	if _, err := api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", api.Path("assistance", grant.ID, "revoke"), revoke); err != nil {
		t.Fatal(err)
	}
	request.Command = command()
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", request); err == nil {
		t.Fatal("revoked reusable grant reopened access")
	}
}

func TestAssistanceScopeAndCurrentProjectRoleAreRechecked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.ready(t)
	project, err := api.Call[domain.Project](ctx, h.founder, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		t.Fatal(err)
	}
	delegate := domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID}
	delegate.ExpectedRevision = project.Revision
	project, err = api.Call[domain.Project](ctx, h.founder, "POST", api.Path("projects", project.ID, "delegates"), delegate)
	if err != nil {
		t.Fatal(err)
	}
	grant := grantFor(t, h, h.otherAgent.ID, h.workerAgent.ID, "tmux", "authentication", project.ID)
	invalid := domain.SessionRequest{Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, ProjectID: project.ID, Mode: "tmux", Purpose: "terminal", Reason: "attempt broader terminal access", ExpiresAt: time.Now().Unix() + 90}
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", invalid); err == nil {
		t.Fatal("authentication grant enabled arbitrary terminal access")
	}
	invalid.Command = command()
	invalid.Purpose = "authentication"
	invalid.Provider = "codex"
	invalid.TargetID = h.founderAgent.ID
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", invalid); err == nil {
		t.Fatal("grant transferred to a different target")
	}
	delegate = domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID, Revoke: true}
	delegate.ExpectedRevision = project.Revision
	if _, err := api.Call[domain.Project](ctx, h.founder, "POST", api.Path("projects", project.ID, "delegates"), delegate); err != nil {
		t.Fatal(err)
	}
	invalid.Command = command()
	invalid.TargetID = h.workerAgent.ID
	if _, err := api.Call[domain.Session](ctx, h.other, "POST", "/v1/sessions", invalid); err == nil {
		t.Fatal("revoked project role retained scoped assistance")
	}
}
