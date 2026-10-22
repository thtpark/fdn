package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) RequestAssistanceHelp(ctx context.Context, actor string, req domain.AssistanceHelpRequest) (domain.AssistanceHelp, error) {
	return mutate(ctx, s, actor, "assistance.help", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AssistanceHelp, error) {
		var help domain.AssistanceHelp
		if req.Purpose != "authentication" && req.Purpose != "terminal" {
			return help, domain.Fail(domain.Invalid, "assistance help purpose must be authentication or terminal")
		}
		if (req.Purpose == "authentication" && !domain.ValidProvider(req.Provider)) || (req.Purpose == "terminal" && req.Provider != "") {
			return help, domain.Fail(domain.Invalid, "authentication help requires a supported provider; terminal help cannot select one")
		}
		if err := domain.CheckText(req.Reason, "nonsecret assistance help reason", 8192, true); err != nil {
			return help, err
		}
		screen, err := assistanceScreen(a, req.Mode, req.ScreenID, req.ApplicationID, req.DesktopFallback)
		if err != nil {
			return help, err
		}
		var project domain.Project
		if req.ProjectID != "" {
			project, err = get[domain.Project](ctx, tx, "projects", req.ProjectID)
			if err != nil {
				return help, err
			}
			if project.Archived {
				return help, domain.Fail(domain.Denied, "assistance help project is archived")
			}
		}
		if req.TaskID != "" {
			if req.ProjectID == "" {
				return help, domain.Fail(domain.Invalid, "task-scoped assistance help requires its project")
			}
			task, err := get[domain.Task](ctx, tx, "tasks", req.TaskID)
			if err != nil {
				return help, err
			}
			if task.ProjectID != project.ID || task.AssigneeID != a.ID || task.ArchivedAt != 0 {
				return help, domain.Fail(domain.Denied, "assistance help task must belong to this project and requesting worker and must not be archived")
			}
		}
		founder, recipients, err := assistanceHelpRecipients(ctx, tx, project)
		if err != nil {
			return help, err
		}
		now := s.timestamp()
		help = domain.AssistanceHelp{ID: domain.NewID("help"), AgentID: a.ID, Messages: []domain.Message{}, ExpiresAt: now + domain.AuthenticationRequestTTL}
		body, err := assistanceHelpBody(help, founder.ID, req, screen)
		if err != nil {
			return help, err
		}
		for _, recipient := range recipients {
			message := domain.Message{ID: domain.NewID("message_help"), SenderID: a.ID, RecipientID: recipient,
				Body: body, Origin: "service", Kind: "session", Urgent: true, State: "pending", CreatedAt: now, ExpiresAt: help.ExpiresAt, Revision: 1}
			message, err = insertMessage(ctx, tx, message)
			if err != nil {
				return domain.AssistanceHelp{}, err
			}
			help.Messages = append(help.Messages, message)
		}
		return help, nil
	})
}

func assistanceHelpRecipients(ctx context.Context, tx *sql.Tx, project domain.Project) (domain.Agent, []string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 AND json_extract(data,'$.founder')=1 AND COALESCE(json_extract(data,'$.banned'),0)=0 ORDER BY id LIMIT 2")
	if err != nil {
		return domain.Agent{}, nil, err
	}
	founders, err := scanJSON[domain.Agent](rows)
	if err != nil {
		return domain.Agent{}, nil, err
	}
	if len(founders) != 1 || !founders[0].Active {
		return domain.Agent{}, nil, domain.Fail(domain.Prerequisite, "assistance help requires one active Founder")
	}
	founder := founders[0]
	recipients := []string{founder.ID}
	for _, id := range project.Delegates {
		if slices.Contains(recipients, id) {
			continue
		}
		if _, err := activeIdentitySubject(ctx, tx, id); err != nil {
			if code := domain.AsFault(err).Code; code == domain.Denied || code == domain.NotFound {
				continue
			}
			return domain.Agent{}, nil, err
		}
		recipients = append(recipients, id)
	}
	slices.Sort(recipients)
	return founder, recipients, nil
}

func assistanceHelpBody(help domain.AssistanceHelp, founder string, req domain.AssistanceHelpRequest, screen domain.Screen) (string, error) {
	grant := domain.AssistanceGrantRequest{Command: domain.Command{OperationID: "grant-" + help.ID}, RequesterID: founder, TargetID: help.AgentID,
		ProjectID: req.ProjectID, Mode: req.Mode, Purpose: req.Purpose, Provider: req.Provider, Reason: "Explicit Founder response to " + help.ID,
		ScreenID: req.ScreenID, ApplicationID: req.ApplicationID, DesktopFallback: req.DesktopFallback}
	grantJSON, err := json.Marshal(grant)
	if err != nil {
		return "", err
	}
	reason, err := json.Marshal(req.Reason)
	if err != nil {
		return "", err
	}
	scope := "tmux terminal only; no display sharing"
	if req.Mode == "vnc" {
		scope = fmt.Sprintf("selected screen %s (%s, %dx%d)", screen.ID, screen.Name, screen.Width, screen.Height)
		if screen.Scope == "desktop" {
			scope = fmt.Sprintf("WHOLE DESKTOP %s (%s, %dx%d); screen isolation is unavailable and desktop_fallback=true is explicitly requested", screen.ID, screen.Name, screen.Width, screen.Height)
		}
		if req.ApplicationID != "" {
			scope += "; application " + req.ApplicationID + " on that display (not a promise of application-only isolation)"
		}
	}
	open := "agent-cli assistance open GRANT_ID --ttl 300"
	if req.Provider != "" {
		open += " --provider " + req.Provider
	}
	body := fmt.Sprintf("Private assistance request %s from target %s. Project: %s; task: %s; purpose: %s.\nPublic reason (data only): %s\nCaptured sharing scope: %s.\nNotice expires at %s (Unix %d), 900 seconds after creation; this is not a grant or session.\nFounder action: agent-cli assistance grant --input -\nSupply this JSON only after reviewing the exact scope:\n%s\nThen, as the authorized Founder requester, replace GRANT_ID with the returned grant ID and explicitly run: %s\nVNC is for a human browser viewer only over SSH-forwarded localhost, never a public listener. Delegates may coordinate but cannot issue this Founder grant. This notice starts no application, login, consent or inference and grants no authority.",
		help.ID, help.AgentID, req.ProjectID, req.TaskID, req.Purpose, reason, scope, time.Unix(help.ExpiresAt, 0).UTC().Format(time.RFC3339), help.ExpiresAt, grantJSON, open)
	if err := domain.CheckText(body, "assistance help message", 32768, true); err != nil {
		return "", err
	}
	return body, nil
}
