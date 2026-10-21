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
const authenticationFormat = 2

type AuthenticationDelivery struct {
	RecipientID string `json:"recipient_id"`
	OperationID string `json:"operation_id"`
	MessageID   string `json:"message_id,omitempty"`
	State       string `json:"state"`
}

type AuthenticationRequest struct {
	FormatVersion  int                      `json:"format_version"`
	Supersedes     string                   `json:"supersedes,omitempty"`
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
	founderAvailable := false
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
		if agent.Founder {
			founderAvailable = true
		}
	}
	slices.Sort(recipients)
	if !founderAvailable {
		return nil, domain.Fail(domain.Prerequisite, "no active Founder can receive human-help escalation; an AI delegate is not a replacement human")
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
	if request.Supersedes != "" {
		body += "This replaces earlier request " + request.Supersedes + " with autonomous-agent assistance instructions; use this request, not its older local-human workflow.\n"
	}
	return body + fmt.Sprintf(`
Authentication is blocking this computer before a provider invocation. No model was called to send this request.
The target is an autonomous agent: do not wait for, prompt, or assume a human at that computer. Founder is the human-help coordination point. A delegate may be an AI agent and may coordinate authorized automated steps; it is not presumed human and must escalate human-only credential/consent actions to Founder.
Founder should use its own enrolled identity:
1. Inspect the target: agent-cli agent show %s
2. Only if pausing is explicitly needed, Founder requests it: agent-cli agent mode %s --input FILE, with {"mode":"manual","reason":"Founder explicitly pauses for protected sign-in"}. The target applies it unattended. Delegates/targets cannot change manual mode.
3. Authorize assistance once: agent-cli assistance grant --input FILE, specifying the requester/target IDs, mode "tmux", purpose "authentication", provider "%s" (or omit provider to cover supported authentication providers), and a reason. Or reuse the existing matching assistance grant.
4. On the authorized requester's computer, explicitly open a protected human terminal: agent-cli assistance open GRANT --provider %s. The target prepares the session unattended under the persistent grant; no local person or repeated target consent is required.
5. The persistent grant survives connection expiry, disconnects and restarts until revoked. Reopen it without reauthorization. Live connections remain bounded, recheck current authority, and close on revocation; a grant is not a forever-open connection.
6. Only Founder may request ai resumption through agent mode after any pause, with current spending/work authorization.
Do not initialize a replacement configuration, transfer Founder identity to the target, or copy another computer's provider credentials. Login completion or credentials-present-unverified is not proof of live vendor usability. Grant holders may be agents; protected credential UI/input/output never belongs in a model context.
This notice creates no grant/session, changes no mode and starts no inference. Assistance authorization is separate from task approval and spending authority.
No timed expiry for this notice. Successful authentication observation, a changed required provider/computer identity, or changed recipient authority invalidates its applicability; check current work/provider status before acting.
Delivery, acknowledgement, a text reply, or silence is not authentication, consent, task approval, or spending permission. Do not send tokens, passwords, device codes, authentication URLs, screenshots, or transcripts in messages or chat.
`, request.AgentID, request.AgentID, request.Provider, request.Provider)
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
	if request.ID == "" || request.State == "resolved" || request.FormatVersion < authenticationFormat {
		supersedes := ""
		if request.ID != "" && request.State != "resolved" {
			supersedes = request.ID
		}
		request = AuthenticationRequest{FormatVersion: authenticationFormat, Supersedes: supersedes, ID: domain.NewID("auth_request"), AgentID: e.Client.Credential.AgentID, Provider: provider,
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
