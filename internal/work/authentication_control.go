package work

import (
	"context"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func (e *Engine) authenticationControl(ctx context.Context, c config.Worker) (bool, error) {
	path := "/v1/messages?urgent=true&limit=100"
	for {
		page, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			return false, err
		}
		for _, message := range page.Items {
			if message.Kind != "authentication-refresh" || message.State != "pending" || message.ExpiresAt <= e.Now().Unix() {
				continue
			}
			return true, e.applyAuthenticationControl(ctx, c, message.ID)
		}
		if page.NextCursor == "" {
			return false, nil
		}
		path = "/v1/messages?urgent=true&limit=100&cursor=" + page.NextCursor
	}
}

func (e *Engine) applyAuthenticationControl(ctx context.Context, c config.Worker, id string) error {
	e.authControlMu.Lock()
	defer e.authControlMu.Unlock()
	message, err := api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", id), nil)
	if err != nil {
		return err
	}
	if message.Kind != "authentication-refresh" || message.AuthenticationRefresh == nil || message.RecipientID != e.Client.Credential.AgentID || message.ResponseRequest {
		return domain.Fail(domain.Denied, "sign-in refresh requires a server-issued control for this identity")
	}
	if message.State == "handled" {
		return nil
	}
	if message.State != "pending" || message.ExpiresAt <= e.Now().UTC().Unix() {
		return domain.Fail(domain.Prerequisite, "sign-in refresh is no longer pending; Founder must request again")
	}
	founder, err := api.Call[domain.Agent](ctx, e.Client, "GET", api.Path("agents", message.SenderID), nil)
	if err != nil {
		return err
	}
	if !founder.Active || !founder.Founder {
		return domain.Fail(domain.Denied, "sign-in refresh no longer has an active Founder issuer")
	}
	control := message.AuthenticationRefresh
	if control.ProjectID != "" {
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", control.ProjectID), nil)
		if err != nil {
			return err
		}
		if project.Archived {
			return domain.Fail(domain.Prerequisite, "sign-in refresh project is archived")
		}
	}
	requests, err := e.authenticationRequests(ctx)
	if err != nil {
		return err
	}
	prepared := false
	for _, request := range requests {
		if request.RefreshID == message.ID {
			if request.Provider != control.Provider || request.ProjectID != control.ProjectID {
				return domain.Fail(domain.Conflict, "prepared sign-in refresh scope changed")
			}
			prepared = true
		}
	}
	if !prepared {
		health, err := e.Executor.Check(ctx, control.Provider)
		if background, _ := ctx.Value(backgroundControlContext{}).(bool); !background {
			if observedErr := e.providerHealth(ctx, control.Provider, "", health, err); observedErr != nil {
				return observedErr
			}
		}
		if err != nil {
			return err
		}
		switch health.Authentication {
		case "authenticated", "credentials-present-unverified":
			if err := e.resolveAuthentication(ctx, control.Provider); err != nil {
				return err
			}
			return e.receiveAuthenticationControl(ctx, message)
		case "login-required", "credentials-rejected":
		default:
			return domain.Fail(domain.Prerequisite, "provider authentication is unknown; no fresh sign-in request was issued")
		}
	}
	if e.Now().UTC().Unix() >= message.ExpiresAt {
		return domain.Fail(domain.Prerequisite, "sign-in refresh expired during the readiness check; Founder must request again")
	}
	var task *domain.Task
	if control.ProjectID != "" {
		task = &domain.Task{ProjectID: control.ProjectID}
	}
	if _, err := e.refreshAuthentication(ctx, c, control.Provider, task, message.ID); err != nil {
		return err
	}
	return e.receiveAuthenticationControl(ctx, message)
}

func (e *Engine) receiveAuthenticationControl(ctx context.Context, message domain.Message) error {
	_, err := commit[domain.Message](ctx, e, "POST", api.Path("messages", message.ID, "receipt"), domain.MessageReceipt{
		Command: domain.Command{OperationID: message.ID + ".authentication-receipt", ExpectedRevision: message.Revision}, State: "handled"})
	return err
}
