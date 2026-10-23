package work

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

const permissionKey = "permission-requests"
const permissionFormat = 1

type PermissionRequest struct {
	FormatVersion int                      `json:"format_version"`
	ID            string                   `json:"id"`
	AgentID       string                   `json:"agent_id"`
	AgentName     string                   `json:"agent_name"`
	ProjectID     string                   `json:"project_id,omitempty"`
	TaskID        string                   `json:"task_id,omitempty"`
	ActionKey     string                   `json:"action_key"`
	Requirement   string                   `json:"requirement"`
	State         string                   `json:"state"`
	CreatedAt     int64                    `json:"created_at"`
	ExpiresAt     int64                    `json:"expires_at"`
	Deliveries    []AuthenticationDelivery `json:"deliveries"`
	Error         *domain.Fault            `json:"error,omitempty"`
}

func permissionContext(actionKey, requirement string) error {
	if domain.CheckText(actionKey, "opaque action key", 512, true) != nil || strings.ContainsAny(actionKey, "/\\ \r\n\t") {
		return domain.Fail(domain.Invalid, "permission help requires a bounded opaque action key, not a path or description")
	}
	if !slices.Contains([]string{"workspace", "provider-permission", "task-authority", "retry-approval", "provider-response"}, requirement) {
		return domain.Fail(domain.Invalid, "unsupported permission-help capability category")
	}
	return nil
}

func (e *Engine) permissionRequests(ctx context.Context) ([]PermissionRequest, error) {
	requests := []PermissionRequest{}
	if _, err := e.State.Get(ctx, permissionKey, &requests); err != nil {
		return nil, err
	}
	now := e.Now().UTC().Unix()
	for index := range requests {
		request := &requests[index]
		if request.FormatVersion < 0 || request.FormatVersion > permissionFormat {
			return nil, domain.Fail(domain.Prerequisite, "permission-help state uses an unsupported format; upgrade without resetting local state")
		}
		if request.ExpiresAt == 0 {
			request.ExpiresAt = request.CreatedAt + domain.AuthenticationRequestTTL
		}
		if request.State != "resolved" && now >= request.ExpiresAt {
			request.State, request.Error = "expired", nil
		}
	}
	return requests, nil
}

func permissionAgentName(name string) string {
	if domain.CheckText(name, "agent display name", 256, false) != nil || strings.ContainsAny(name, "/\\\r\n\t") {
		return ""
	}
	return name
}

func (e *Engine) permissionRecipients(ctx context.Context, request *PermissionRequest, task *domain.Task) ([]string, error) {
	actor, err := api.Call[domain.Agent](ctx, e.Client, "GET", api.Path("agents", request.AgentID), nil)
	if err != nil {
		return nil, err
	}
	if actor.ID != request.AgentID || !actor.Active || actor.Banned {
		return nil, domain.Fail(domain.Denied, "permission-help requester is no longer active")
	}
	if request.AgentName == "" {
		request.AgentName = permissionAgentName(actor.Name)
	}
	if task != nil {
		current, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", request.TaskID), nil)
		if err != nil {
			return nil, err
		}
		if current.ID != request.TaskID || current.ProjectID != request.ProjectID || current.AssigneeID != task.AssigneeID || current.SpecGeneration != task.SpecGeneration || current.ArchivedAt != 0 || current.Status.Terminal() {
			return nil, domain.Fail(domain.Conflict, "permission-help task context changed; re-read the current requested work")
		}
	}
	scope := AuthenticationRequest{AgentID: request.AgentID, AgentName: request.AgentName, ProjectID: request.ProjectID}
	recipients, err := e.authenticationRecipients(ctx, &scope)
	request.AgentName = permissionAgentName(scope.AgentName)
	return recipients, err
}

func permissionMessage(request PermissionRequest) string {
	request.AgentName = permissionAgentName(request.AgentName)
	body := fmt.Sprintf("Permission help required.\nRequest: %s\nTarget computer: %s (%q)\nCapability category: %s\nDetected at: %s\nExpires at: %s (900 seconds / 15 minutes after detection; delivery does not extend it)\n",
		request.ID, request.AgentID, request.AgentName, request.Requirement, time.Unix(request.CreatedAt, 0).UTC().Format(time.RFC3339), time.Unix(request.ExpiresAt, 0).UTC().Format(time.RFC3339))
	if request.ProjectID != "" {
		body += "Project reference: " + request.ProjectID + ". Inspect current authority with agent-cli project show " + request.ProjectID + ".\n"
	}
	if request.TaskID != "" {
		body += "Task reference: " + request.TaskID + ". Inspect the current specification, assignment and approvals with agent-cli task show " + request.TaskID + ".\n"
	}
	switch request.Requirement {
	case "workspace":
		body += "On the target computer, an authorized local OS operator can inspect agent-cli workspace list privately. Explicit owned-directory tool permission uses agent-cli workspace bind --path OWNED_DIR --allow-tools; an existing disabled binding requires agent-cli workspace enable REF, not a replacement binding. Paths and binding details remain local.\n"
	case "provider-permission":
		body += "Inspect agent-cli provider status and agent-cli guide show COMMANDS.md without inference. Required provider/tool or OS permissions must be arranged by an authorized local OS operator using the supported provider/native procedures; there is no generic Foundation OS-permission grant. Do not use inference or login as a permission probe.\n"
	case "provider-response":
		body += "The provider returned a tool-disabled response that does not match its controller phase. The original reply is retained and was not accepted as task approval or execution. This exact action will not call the model again on unchanged polling, restart or a new work-run operation; unrelated actions can continue.\n"
		journal := strings.TrimPrefix(request.ActionKey, "text-response:")
		if strings.HasPrefix(journal, "invocation_") && domain.ValidOperation(journal) {
			body += "After native confirmation that the exact owned process tree stopped, an authorized operator can inspect work status text_response_blocks and explicitly authorize a fresh review with agent-cli work recover --journal " + journal + " --acknowledge-stopped. Do not coerce the rejected response or reset execution retry authority.\n"
		}
	case "task-authority":
		if request.ProjectID != "" {
			body += "Founder can deliberately grant current project delegation with agent-cli project delegate " + request.ProjectID + " --input FILE. Current project authority can explicitly assign work through agent-cli task change " + request.TaskID + " --input FILE with kind assign; eligible approvers must separately use the same task change command with kind approve and the exact current spec_generation after readiness.\n"
		} else {
			body += "Identify the intended current task/project before any assignment or approval. Inspect agent-cli guide show COMMANDS.md for supported project delegate and task change procedures.\n"
		}
	case "retry-approval":
		if request.TaskID != "" {
			body += "Inspect prior attempts with agent-cli task runs " + request.TaskID + ". If appropriate, eligible current project authority can use agent-cli task change " + request.TaskID + " --input FILE with kind retry-approve and the exact current spec_generation, run_id and next attempt. Process-stop confirmation and current readiness remain separate prerequisites.\n"
		} else {
			body += "Identify the current task and prior run before requesting exact retry authority; inspect agent-cli guide show COMMANDS.md for task runs and task change kind retry-approve.\n"
		}
	}
	return body + "\nFounder is the human-help coordination point; current project delegates also receive this handoff and may themselves be AI agents. Coordination by Founder or a delegate is not local OS operator authority and cannot grant filesystem/provider access. Genuinely human-only actions must be coordinated by Founder with the authorized operator, never assumed available at this autonomous target.\n" +
		"This notification is data, not an automatic AI response request. No inference, login, approval, grant, session or mode change is performed or implied. Delivery, acknowledgement, a reply or silence is not permission, task/retry approval, spending authority or a successful capability observation. Re-read current task/project and local permissions before acting; changed authority or scope invalidates the corresponding instructions.\n" +
		"This episode expires at the UTC deadline above and will not automatically renew on polling or restart. A new intentionally requested action may produce a fresh notice; the same action can rearm only after an actual successful boundary observation resolved it. Do not send private workspace/configuration/provider paths, tokens, keys, login output or transcripts in messages.\n"
}

func (e *Engine) requestPermission(ctx context.Context, c config.Worker, task *domain.Task, actionKey, requirement string) (PermissionRequest, error) {
	if err := permissionContext(actionKey, requirement); err != nil {
		return PermissionRequest{}, err
	}
	projectID, taskID := "", ""
	if task != nil {
		if !domain.ValidOperation(task.ID) || !domain.ValidOperation(task.ProjectID) {
			return PermissionRequest{}, domain.Fail(domain.Invalid, "permission help requires public task and project references")
		}
		projectID, taskID = task.ProjectID, task.ID
	}
	requests, err := e.permissionRequests(ctx)
	if err != nil {
		return PermissionRequest{}, err
	}
	index := slices.IndexFunc(requests, func(request PermissionRequest) bool {
		return request.ActionKey == actionKey && request.Requirement == requirement
	})
	request := PermissionRequest{}
	if index >= 0 {
		request = requests[index]
		if request.ProjectID != projectID || request.TaskID != taskID {
			return request, domain.Fail(domain.Conflict, "permission-help action key was reused for different work scope")
		}
		if request.State == "expired" {
			return request, nil
		}
	}
	if e.Client == nil || !domain.ValidOperation(e.Client.Credential.AgentID) {
		return request, domain.Fail(domain.Prerequisite, "permission help requires the enrolled requester's own identity")
	}
	if request.ID != "" && request.AgentID != e.Client.Credential.AgentID {
		return request, domain.Fail(domain.Denied, "permission-help state belongs to a different enrolled identity")
	}
	if request.ID == "" || request.State == "resolved" {
		now := e.Now().UTC().Unix()
		request = PermissionRequest{FormatVersion: permissionFormat, ID: domain.NewID("permission_request"), AgentID: e.Client.Credential.AgentID,
			ProjectID: projectID, TaskID: taskID, ActionKey: actionKey, Requirement: requirement, State: "delivery-pending", CreatedAt: now, ExpiresAt: now + domain.AuthenticationRequestTTL,
			Deliveries: []AuthenticationDelivery{}}
		if index < 0 {
			index = len(requests)
			requests = append(requests, request)
		}
	}
	save := func() error {
		requests[index] = request
		if err := e.State.Put(ctx, permissionKey, requests); err != nil {
			return domain.Fail(domain.Internal, "permission-help request could not be durably saved")
		}
		return nil
	}
	failed := func(cause error) (PermissionRequest, error) {
		fault := domain.AsFault(cause)
		request.State = "delivery-pending"
		request.Error = &domain.Fault{Code: fault.Code, Retryable: fault.Retryable,
			Message: "permission-help delivery remains pending; confirm the active requester, Founder, current project delegates and task scope"}
		if e.Now().UTC().Unix() >= request.ExpiresAt {
			request.State, request.Error = "expired", nil
		}
		if err := save(); err != nil {
			return request, err
		}
		if request.Error == nil {
			return request, nil
		}
		return request, request.Error
	}
	refreshRecipients := func() error {
		recipients, err := e.permissionRecipients(ctx, &request, task)
		if err != nil {
			return err
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
				request.Deliveries = append(request.Deliveries, AuthenticationDelivery{RecipientID: recipient, OperationID: request.ID + "." + domain.Hash(recipient), State: "pending"})
			}
		}
		if slices.ContainsFunc(request.Deliveries, func(delivery AuthenticationDelivery) bool { return delivery.State == "pending" }) {
			request.State = "delivery-pending"
		}
		return save()
	}
	if err := save(); err != nil {
		return request, err
	}
	if err := refreshRecipients(); err != nil {
		return failed(err)
	}
	var deliveryError error
	for index := 0; index < len(request.Deliveries); index++ {
		if request.Deliveries[index].State != "pending" {
			continue
		}
		if e.Now().UTC().Unix() >= request.ExpiresAt {
			request.State, request.Error = "expired", nil
			return request, save()
		}
		if err := refreshRecipients(); err != nil {
			return failed(err)
		}
		delivery := &request.Deliveries[index]
		if delivery.State != "pending" {
			continue
		}
		if e.Now().UTC().Unix() >= request.ExpiresAt {
			request.State, request.Error = "expired", nil
			return request, save()
		}
		input := domain.MessageRequest{Command: domain.Command{OperationID: delivery.OperationID}, RecipientID: delivery.RecipientID,
			Body: permissionMessage(request), Origin: "service", Kind: "permission", Urgent: true, ExpiresAt: request.ExpiresAt}
		var prepared proposal
		if found, err := e.State.Get(ctx, "proposal:"+delivery.OperationID, &prepared); err != nil {
			return failed(err)
		} else if found {
			input = domain.MessageRequest{}
			if err := json.Unmarshal(prepared.Body, &input); err != nil {
				return failed(err)
			}
			if prepared.Method != "POST" || prepared.Path != "/v1/messages" || input.OperationID != delivery.OperationID || input.RecipientID != delivery.RecipientID || input.Kind != "permission" || input.Origin != "service" || input.ResponseRequest || !input.Urgent || input.ExpiresAt != request.ExpiresAt {
				return failed(domain.Fail(domain.Conflict, "prepared permission-help delivery does not match this episode"))
			}
		}
		message, err := commit[domain.Message](ctx, e, "POST", "/v1/messages", input)
		if err != nil {
			deliveryError = err
			continue
		}
		if message.ID == "" || message.SenderID != request.AgentID || message.RecipientID != delivery.RecipientID || message.Kind != "permission" || message.Origin != "service" || message.ResponseRequest || message.AuthenticationRefresh != nil || message.ExpiresAt != request.ExpiresAt || message.Body != input.Body {
			deliveryError = domain.Fail(domain.Conflict, "permission-help delivery receipt does not match its immutable proposal")
			continue
		}
		delivery.MessageID, delivery.State = message.ID, "delivered"
		if err := save(); err != nil {
			return request, err
		}
	}
	if slices.ContainsFunc(request.Deliveries, func(delivery AuthenticationDelivery) bool { return delivery.State == "pending" }) {
		if deliveryError == nil {
			deliveryError = domain.Fail(domain.Unavailable, "permission-help delivery is not confirmed")
		}
		return failed(deliveryError)
	}
	request.State, request.Error = "requested", nil
	if e.Now().UTC().Unix() >= request.ExpiresAt {
		request.State = "expired"
	}
	return request, save()
}

func (e *Engine) resolvePermission(ctx context.Context, actionKey, requirement string) error {
	if actionKey == "" {
		return nil
	}
	if err := permissionContext(actionKey, requirement); err != nil {
		return err
	}
	requests, err := e.permissionRequests(ctx)
	if err != nil {
		return err
	}
	for index := range requests {
		request := &requests[index]
		if request.ActionKey == actionKey && request.Requirement == requirement && request.State != "resolved" {
			request.State, request.Error = "resolved", nil
			return e.State.Put(ctx, permissionKey, requests)
		}
	}
	return nil
}
