package foundation

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

type assistanceHelpReceipt struct {
	AgentID    string   `json:"agent_id"`
	MessageIDs []string `json:"message_ids"`
	ExpiresAt  int64    `json:"expires_at"`
}

type storedResult struct {
	Kind       string          `json:"kind"`
	ID         string          `json:"id,omitempty"`
	TaskID     string          `json:"task_id,omitempty"`
	Accepted   bool            `json:"accepted,omitempty"`
	Stale      bool            `json:"stale,omitempty"`
	Generation int64           `json:"generation,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
}

func resultReceipt(value any) ([]byte, error) {
	var ref storedResult
	switch v := value.(type) {
	case domain.Agent:
		ref = storedResult{Kind: "agents", ID: v.ID}
	case domain.AgentIdentity:
		ref = storedResult{Kind: "identity-binding", ID: v.BindingID(), Generation: v.Generation}
	case agentStatusResult:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		ref = storedResult{Kind: "agent-status", ID: v.Receipt.AgentID, Value: data}
	case domain.Project:
		ref = storedResult{Kind: "projects", ID: v.ID}
	case domain.Note:
		ref = storedResult{Kind: "notes", ID: v.ID}
	case domain.SharedFile:
		ref = storedResult{Kind: "files", ID: v.ID}
	case domain.Publication:
		ref = storedResult{Kind: "publication-version", ID: v.VersionID}
	case domain.Task:
		ref = storedResult{Kind: "tasks", ID: v.ID}
	case domain.Improvement:
		ref = storedResult{Kind: "improvements", ID: v.ID}
	case domain.HostedRepository:
		ref = storedResult{Kind: "repositories", ID: v.ID}
	case domain.SoftwareProduct:
		ref = storedResult{Kind: "software_products", ID: v.ID}
	case domain.SoftwareBuild:
		ref = storedResult{Kind: "software_builds", ID: v.ID}
	case domain.SoftwareRelease:
		ref = storedResult{Kind: "software_releases", ID: v.ID}
	case domain.SoftwareDeployment:
		ref = storedResult{Kind: "software_deployments", ID: v.ID}
	case domain.SoftwarePreflight:
		ref = storedResult{Kind: "software_preflights", ID: v.ID}
	case domain.MaintenanceReservation:
		ref = storedResult{Kind: "maintenance_reservations", ID: v.ID}
	case domain.ImprovementResult:
		ref = storedResult{Kind: "improvement-result", ID: v.Request.ID}
	case domain.Run:
		ref = storedResult{Kind: "runs", ID: v.ID}
	case domain.Entry:
		ref = storedResult{Kind: "entries", ID: v.ID}
	case domain.Message:
		ref = storedResult{Kind: "messages", ID: v.ID}
	case domain.MessageRoom:
		ref = storedResult{Kind: "message_rooms", ID: v.ID}
	case domain.RoomMessage:
		ref = storedResult{Kind: "room_messages", ID: v.ID}
	case domain.AgentOperation:
		ref = storedResult{Kind: "agent-operation", ID: v.ID}
	case domain.UpgradePermit:
		ref = storedResult{Kind: "upgrade-permit", ID: domain.Hash(v.Signature)}
	case domain.UpgradePreflight:
		ref = storedResult{Kind: "upgrade-preflight", ID: v.ID}
	case domain.Session:
		ref = storedResult{Kind: "sessions", ID: v.ID}
	case domain.AssistanceGrant:
		ref = storedResult{Kind: "assistance_grants", ID: v.ID}
	case domain.AssistanceHelp:
		help := assistanceHelpReceipt{AgentID: v.AgentID, MessageIDs: make([]string, len(v.Messages)), ExpiresAt: v.ExpiresAt}
		for i, message := range v.Messages {
			help.MessageIDs[i] = message.ID
		}
		data, err := json.Marshal(help)
		if err != nil {
			return nil, err
		}
		ref = storedResult{Kind: "assistance-help", ID: v.ID, Value: data}
	case domain.ReportOutcome:
		ref = storedResult{Kind: "report", ID: v.Run.ID, TaskID: v.Task.ID, Accepted: v.Accepted, Stale: v.Stale}
	case domain.Attention, map[string]bool, []domain.Screen, domain.Sharing, domain.ComputerEnvironment, domain.ComputerModelOptions, domain.AnnouncementReceipt, domain.FileProgress, domain.FileDownloadReceipt, domain.UpgradeMaintenance, domain.ProviderPreferences, domain.UpgradeLease, domain.ImprovementPolicy, domain.SoftwareInventory, domain.SoftwareConsumer, domain.SoftwareMaintenance, domain.RoomDelivery, domain.RoomReadResult:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		ref = storedResult{Kind: "value", Value: data}
	default:
		return nil, domain.Fail(domain.Internal, "operation result has no bounded receipt contract")
	}
	return json.Marshal(ref)
}

func replayResult[T any](ctx context.Context, tx *sql.Tx, actor string, data []byte) (T, error) {
	var zero T
	var ref storedResult
	if err := json.Unmarshal(data, &ref); err != nil {
		return zero, err
	}
	var value any
	var err error
	switch ref.Kind {
	case "agents":
		value, err = get[domain.Agent](ctx, tx, ref.Kind, ref.ID)
	case "identity-binding":
		var identity domain.AgentIdentity
		identity, err = get[domain.AgentIdentity](ctx, tx, "agent_identities", ref.ID)
		if err == nil && (identity.AgentID != actor || identity.Generation != ref.Generation) {
			err = domain.Fail(domain.Conflict, "device operation belongs to a retired key generation")
		}
		value = identity
	case "agent-status":
		value, err = replayAgentStatus(ctx, tx, actor, ref)
	case "projects":
		value, err = get[domain.Project](ctx, tx, ref.Kind, ref.ID)
	case "notes":
		value, err = get[domain.Note](ctx, tx, ref.Kind, ref.ID)
	case "files":
		var file domain.SharedFile
		file, err = readFile(ctx, tx, ref.ID)
		if err == nil {
			err = requireFileReader(ctx, tx, actor, file)
		}
		value = file
	case "publication-version":
		value, err = get[domain.Publication](ctx, tx, "publication_versions", ref.ID)
	case "tasks":
		value, err = get[domain.Task](ctx, tx, ref.Kind, ref.ID)
	case "improvements":
		value, err = get[domain.Improvement](ctx, tx, ref.Kind, ref.ID)
	case "improvement-result":
		request, readErr := get[domain.Improvement](ctx, tx, "improvements", ref.ID)
		err = readErr
		result := domain.ImprovementResult{Request: request}
		if err == nil && request.TaskID != "" {
			task, taskErr := get[domain.Task](ctx, tx, "tasks", request.TaskID)
			err, result.Task = taskErr, &task
		}
		value = result
	case "repositories":
		repo, readErr := get[domain.HostedRepository](ctx, tx, ref.Kind, ref.ID)
		err = readErr
		if err == nil {
			err = requireRepositoryReader(ctx, tx, actor, repo)
		}
		value = repo
	case "software_products":
		value, err = get[domain.SoftwareProduct](ctx, tx, ref.Kind, ref.ID)
	case "software_builds":
		value, err = get[domain.SoftwareBuild](ctx, tx, ref.Kind, ref.ID)
	case "software_releases":
		value, err = get[domain.SoftwareRelease](ctx, tx, ref.Kind, ref.ID)
	case "software_deployments":
		deployment, readErr := get[domain.SoftwareDeployment](ctx, tx, ref.Kind, ref.ID)
		err = readErr
		if err == nil && deployment.Permit.ActorID != actor && deployment.Permit.Proposal.Challenge.TargetID != actor {
			err = domain.Fail(domain.Denied, "software deployment receipt belongs to another party")
		}
		value = deployment
	case "software_preflights":
		control, readErr := get[domain.SoftwarePreflight](ctx, tx, ref.Kind, ref.ID)
		err = readErr
		if err == nil && actor != control.FounderID && actor != control.Request.TargetID && actor != "native-maintenance" {
			err = domain.Fail(domain.Denied, "software preflight receipt belongs to another party")
		}
		value = control
	case "maintenance_reservations":
		reservation, readErr := get[domain.MaintenanceReservation](ctx, tx, ref.Kind, ref.ID)
		err = readErr
		if err == nil && reservation.AgentID != actor {
			err = domain.Fail(domain.Denied, "maintenance reservation belongs to another actor")
		}
		value = reservation
	case "runs":
		value, err = get[domain.Run](ctx, tx, ref.Kind, ref.ID)
	case "entries":
		value, err = get[domain.Entry](ctx, tx, ref.Kind, ref.ID)
	case "messages":
		value, err = get[domain.Message](ctx, tx, ref.Kind, ref.ID)
	case "message_rooms":
		var room domain.MessageRoom
		room, err = get[domain.MessageRoom](ctx, tx, ref.Kind, ref.ID)
		if err == nil {
			_, err = hydrateRoom(ctx, tx, actor, &room)
		}
		value = room
	case "room_messages":
		value, err = get[domain.RoomMessage](ctx, tx, ref.Kind, ref.ID)
	case "agent-operation":
		value, err = getAgentOperation(ctx, tx, ref.ID)
	case "upgrade-permit":
		var data string
		err = tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "upgrade-permit:"+ref.ID).Scan(&data)
		if err == nil {
			var permit domain.UpgradePermit
			err = json.Unmarshal([]byte(data), &permit)
			if err == nil && (permit.ActorID != actor || domain.Hash(permit.Signature) != ref.ID) {
				err = domain.Fail(domain.Denied, "upgrade receipt does not belong to this exact actor and signed proposal")
			}
			value = permit
		}
	case "upgrade-preflight":
		var record domain.UpgradePreflight
		record, err = readUpgradePreflight(ctx, tx, ref.ID)
		if err == nil && actor != record.FounderID &&
			!(record.Request.Role == "agent" && actor == record.Request.TargetID) &&
			!(record.Request.Role == "foundation" && actor == "native-maintenance") {
			err = domain.Fail(domain.Denied, "native preflight receipt belongs to another exact party")
		}
		value = record
	case "sessions":
		value, err = get[domain.Session](ctx, tx, ref.Kind, ref.ID)
	case "assistance_grants":
		value, err = get[domain.AssistanceGrant](ctx, tx, ref.Kind, ref.ID)
	case "assistance-help":
		var receipt assistanceHelpReceipt
		if err := json.Unmarshal(ref.Value, &receipt); err != nil {
			return zero, err
		}
		help := domain.AssistanceHelp{ID: ref.ID, AgentID: receipt.AgentID, Messages: []domain.Message{}, ExpiresAt: receipt.ExpiresAt}
		for _, id := range receipt.MessageIDs {
			message, err := get[domain.Message](ctx, tx, "messages", id)
			if err != nil {
				return zero, err
			}
			if message.SenderID != receipt.AgentID || !assistanceHelpMessage(message) || message.ExpiresAt != receipt.ExpiresAt {
				return zero, domain.Fail(domain.Internal, "assistance help receipt does not match its immutable messages")
			}
			// Disposition/expiry projections never rewrite the original help receipt.
			message.State, message.Revision = "pending", 1
			help.Messages = append(help.Messages, message)
		}
		value = help
	case "value":
		err = json.Unmarshal(ref.Value, &zero)
		return zero, err
	case "report":
		task, taskErr := get[domain.Task](ctx, tx, "tasks", ref.TaskID)
		if taskErr != nil {
			return zero, taskErr
		}
		run, runErr := get[domain.Run](ctx, tx, "runs", ref.ID)
		if runErr != nil {
			return zero, runErr
		}
		reportedSpec := run.SpecGeneration
		if run.Kind == domain.VerificationRun && run.Decision == "rework" && !ref.Stale && run.State == "completed" {
			reportedSpec++
		}
		stale := ref.Stale || task.SpecGeneration != reportedSpec
		if run.Kind == domain.VerificationRun {
			stale = stale || task.ResultGeneration != run.ResultGeneration
		}
		value = domain.ReportOutcome{Accepted: ref.Accepted && !stale, Stale: stale, Task: task, Run: run}
	default:
		return zero, domain.Fail(domain.Internal, "unknown operation receipt")
	}
	if err != nil {
		return zero, err
	}
	result, ok := value.(T)
	if !ok {
		return zero, domain.Fail(domain.Internal, "operation receipt has an incompatible response type")
	}
	return result, nil
}
