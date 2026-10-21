package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

const authenticationKey = "authentication-requests"

type AuthenticationDelivery struct {
	RecipientID string `json:"recipient_id"`
	OperationID string `json:"operation_id"`
	MessageID   string `json:"message_id,omitempty"`
	State       string `json:"state"`
}

type AuthenticationRequest struct {
	ID             string                   `json:"id"`
	AgentID        string                   `json:"agent_id"`
	AgentName      string                   `json:"agent_name"`
	Provider       string                   `json:"provider"`
	ProjectID      string                   `json:"project_id,omitempty"`
	TaskID         string                   `json:"task_id,omitempty"`
	SpecGeneration int64                    `json:"spec_generation,omitempty"`
	ModeAtRequest  string                   `json:"mode_at_request"`
	State          string                   `json:"state"`
	CreatedAt      int64                    `json:"created_at"`
	ResolvedAt     int64                    `json:"resolved_at,omitempty"`
	Deliveries     []AuthenticationDelivery `json:"deliveries"`
	Error          *domain.Fault            `json:"error,omitempty"`
}

func (e *Engine) authenticationRequests(ctx context.Context) ([]AuthenticationRequest, error) {
	requests := []AuthenticationRequest{}
	_, err := e.State.Get(ctx, authenticationKey, &requests)
	return requests, err
}

func (e *Engine) authenticationRecipients(ctx context.Context, request *AuthenticationRequest) ([]string, error) {
	agents, err := api.Call[[]domain.Agent](ctx, e.Client, "GET", "/v1/agents", nil)
	if err != nil {
		return nil, err
	}
	delegates := []string{}
	if request.ProjectID != "" {
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", request.ProjectID), nil)
		if err != nil {
			return nil, err
		}
		if !project.Archived {
			delegates = project.Delegates
		}
	}
	recipients := []string{}
	for _, agent := range agents {
		if !agent.Active {
			continue
		}
		if agent.ID == request.AgentID && request.AgentName == "" {
			request.AgentName = agent.Name
		}
		if agent.Founder || slices.Contains(delegates, agent.ID) {
			recipients = append(recipients, agent.ID)
		}
	}
	slices.Sort(recipients)
	if len(recipients) == 0 {
		return nil, domain.Fail(domain.Prerequisite, "no active Founder or relevant project delegate can receive the sign-in request")
	}
	return recipients, nil
}

func authenticationMessage(request AuthenticationRequest) string {
	body := fmt.Sprintf("Provider sign-in required.\nRequest: %s\nTarget computer: %s (%s)\nProvider: %s\nDetected at: %s\nMode when detected: %s\n",
		request.ID, request.AgentName, request.AgentID, request.Provider, time.Unix(request.CreatedAt, 0).UTC().Format(time.RFC3339), request.ModeAtRequest)
	if request.ProjectID != "" {
		body += fmt.Sprintf("Triggering project: %s; task: %s; specification: %d. Re-read current task/authority before work; these references grant no approval.\n",
			request.ProjectID, request.TaskID, request.SpecGeneration)
	}
	return body + fmt.Sprintf(`
Authentication is blocking this computer before a provider invocation. No model was called to send this request.
An authorized human should use the target computer's own dedicated OS account and existing Foundation configuration:
1. Inspect: agent-cli mode show; agent-cli work status
2. Before sign-in can unlock automatic inference, arrange an authorized manual hold with: agent-cli mode set manual
3. In a visible human-only terminal, without --json or --non-interactive: agent-cli --timeout 300 provider login --provider %s
4. Check without inference: agent-cli provider status --provider %s
The commands use that account's existing configuration; use its established --config option if nondefault. Do not initialize a replacement configuration or copy credentials from Founder/another account.
Restore prior mode only after separate spending/work authorization. Login completion or credentials-present-unverified is not proof of live vendor usability.
Remote assistance, if chosen, needs a separately authorized session with current project scope, target per-session consent, explicit requester open, and its own 60-900 second expiry. This notice creates/opens/consents to no session.
No timed expiry for this notice. Successful authentication observation, a changed required provider/computer identity, or changed recipient authority invalidates its applicability; check current work/provider status before acting.
Delivery, acknowledgement, a text reply, or silence is not authentication, consent, task approval, or spending permission. Do not send tokens, passwords, device codes, authentication URLs, screenshots, or transcripts in messages or chat.
`, request.Provider, request.Provider)
}

func (e *Engine) requestAuthentication(ctx context.Context, c config.Worker, provider string, task *domain.Task) (AuthenticationRequest, error) {
	requests, err := e.authenticationRequests(ctx)
	if err != nil {
		return AuthenticationRequest{}, err
	}
	projectID := ""
	if task != nil {
		projectID = task.ProjectID
	}
	index := slices.IndexFunc(requests, func(request AuthenticationRequest) bool {
		return request.Provider == provider && request.ProjectID == projectID
	})
	request := AuthenticationRequest{}
	if index >= 0 {
		request = requests[index]
	}
	if request.ID == "" || request.State == "resolved" {
		request = AuthenticationRequest{ID: domain.NewID("auth_request"), AgentID: e.Client.Credential.AgentID, Provider: provider,
			ProjectID: projectID, ModeAtRequest: c.Mode, State: "delivery-pending", CreatedAt: e.Now().UTC().Unix(), Deliveries: []AuthenticationDelivery{}}
		if task != nil {
			request.TaskID, request.SpecGeneration = task.ID, task.SpecGeneration
		}
		if index < 0 {
			index = len(requests)
			requests = append(requests, request)
		}
	}
	save := func() error {
		requests[index] = request
		return e.State.Put(ctx, authenticationKey, requests)
	}
	failed := func(cause error) (AuthenticationRequest, error) {
		request.State = "delivery-pending"
		fault := domain.AsFault(cause)
		request.Error = &domain.Fault{Code: fault.Code, Retryable: fault.Retryable,
			Message: fmt.Sprintf("provider sign-in request %s delivery remains pending: %s", request.ID, fault.Message)}
		return request, errors.Join(request.Error, cause, save())
	}
	if err := save(); err != nil {
		return request, err
	}
	recipients, err := e.authenticationRecipients(ctx, &request)
	if err != nil {
		return failed(err)
	}
	for index := range request.Deliveries {
		delivery := &request.Deliveries[index]
		if !slices.Contains(recipients, delivery.RecipientID) {
			delivery.State = "no-longer-authorized"
		} else if delivery.State == "no-longer-authorized" {
			delivery.State = "pending"
			if delivery.MessageID != "" {
				delivery.State = "delivered"
			}
		}
	}
	for _, recipient := range recipients {
		if !slices.ContainsFunc(request.Deliveries, func(delivery AuthenticationDelivery) bool { return delivery.RecipientID == recipient }) {
			request.Deliveries = append(request.Deliveries, AuthenticationDelivery{RecipientID: recipient,
				OperationID: request.ID + "." + domain.Hash(recipient), State: "pending"})
		}
	}
	if slices.ContainsFunc(request.Deliveries, func(delivery AuthenticationDelivery) bool { return delivery.State == "pending" }) {
		request.State = "delivery-pending"
	}
	if err := save(); err != nil {
		return request, err
	}
	for index := range request.Deliveries {
		delivery := &request.Deliveries[index]
		if delivery.State != "pending" {
			continue
		}
		message, err := commit[domain.Message](ctx, e, "POST", "/v1/messages", domain.MessageRequest{
			Command: domain.Command{OperationID: delivery.OperationID}, RecipientID: delivery.RecipientID,
			Body: authenticationMessage(request), Origin: "service", Kind: "authentication", ResponseRequest: false})
		if err != nil {
			return failed(err)
		}
		delivery.MessageID, delivery.State = message.ID, "delivered"
		if err := save(); err != nil {
			return request, err
		}
	}
	request.State, request.Error = "requested", nil
	return request, save()
}

func (e *Engine) resolveAuthentication(ctx context.Context, provider string) error {
	requests, err := e.authenticationRequests(ctx)
	if err != nil {
		return err
	}
	changed := false
	for index := range requests {
		if requests[index].Provider == provider && requests[index].State != "resolved" {
			requests[index].State, requests[index].Error = "resolved", nil
			requests[index].ResolvedAt = e.Now().UTC().Unix()
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return e.State.Put(ctx, authenticationKey, requests)
}
