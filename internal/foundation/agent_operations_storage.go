package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func agentOperationFromMessage(message domain.Message) (domain.AgentOperation, error) {
	if message.Kind != "agent-operation" || message.AgentOperation == nil {
		return domain.AgentOperation{}, domain.Fail(domain.NotFound, "agent operation not found")
	}
	operation := *message.AgentOperation
	if message.ID != operation.ID || message.SenderID != operation.RequesterID || message.RecipientID != operation.TargetID ||
		message.CreatedAt != operation.CreatedAt || message.ExpiresAt != operation.ExpiresAt || message.Revision != operation.Revision ||
		message.Origin != "service" || !message.Urgent || message.ResponseRequest || message.AuthenticationRefresh != nil ||
		(message.State != "pending" && message.State != "handled") || (domain.AgentOperationTerminal(operation.State) != (message.State == "handled")) {
		return domain.AgentOperation{}, domain.Fail(domain.Internal, "agent operation does not match its protected storage reference")
	}
	if err := domain.ValidateAgentOperation(operation); err != nil {
		return domain.AgentOperation{}, domain.Fail(domain.Internal, "stored agent operation is invalid")
	}
	return operation, nil
}

func getAgentOperation(ctx context.Context, q queryer, id string) (domain.AgentOperation, error) {
	if !domain.ValidOperation(id) {
		return domain.AgentOperation{}, domain.Fail(domain.Invalid, "a valid agent operation ID is required")
	}
	message, err := get[domain.Message](ctx, q, "messages", id)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	return agentOperationFromMessage(message)
}

func agentOperationIdentity(ctx context.Context, q queryer, a domain.Agent) (int64, bool, error) {
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", a.ID)
	if err != nil {
		return 0, false, err
	}
	return identity.Generation, identity.AgentID == a.ID && identity.Generation > 0 && identity.State == "active" && a.Active && !a.Banned, nil
}

func agentOperationParties(ctx context.Context, q queryer, operation domain.AgentOperation) (domain.Agent, bool, error) {
	requester, err := get[domain.Agent](ctx, q, "agents", operation.RequesterID)
	if err != nil {
		return domain.Agent{}, false, err
	}
	target, err := get[domain.Agent](ctx, q, "agents", operation.TargetID)
	if err != nil {
		return target, false, err
	}
	requesterGeneration, requesterActive, err := agentOperationIdentity(ctx, q, requester)
	if err != nil {
		return target, false, err
	}
	targetGeneration, targetActive, err := agentOperationIdentity(ctx, q, target)
	return target, err == nil && requesterActive && requester.Founder && targetActive && requesterGeneration == operation.RequesterGeneration && targetGeneration == operation.TargetGeneration, err
}

// Administrative recovery reads only the exact descriptor, not the private session API.
func agentOperationSession(ctx context.Context, q queryer, target, id string, now int64) (domain.Session, error) {
	session, err := get[domain.Session](ctx, q, "sessions", id)
	if err != nil {
		return session, err
	}
	if session.TargetID != target {
		return domain.Session{}, domain.Fail(domain.Denied, "session does not belong to the exact operation target")
	}
	if session.ExpiresAt <= now && slices.Contains([]string{"requested", "consented", "active"}, session.State) {
		session.State = "expired"
	}
	if !domain.TerminalSessionState(session.State) {
		return session, domain.Fail(domain.Prerequisite, "runtime recovery requires an already terminal session")
	}
	return session, nil
}

func projectAgentOperation(ctx context.Context, q queryer, operation domain.AgentOperation, now int64) (domain.AgentOperation, error) {
	operation.ServerObservedAt = now
	target, allowed, err := agentOperationParties(ctx, q, operation)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	mode := target.Mode
	if mode != "ai" && mode != "manual" {
		mode = "manual"
	}
	project := func(state, code string, finished int64) domain.AgentOperation {
		operation.State, operation.FinishedAt = state, finished
		operation.Result = &domain.AgentOperationResult{Code: code, ModeAtCheck: mode}
		return operation
	}
	if !allowed {
		return project("rejected", "authority-changed", max(operation.CreatedAt, now)), nil
	}
	if !domain.AgentOperationTerminal(operation.State) {
		if now >= operation.ExpiresAt {
			return project("expired", "operation-expired", operation.ExpiresAt), nil
		}
		if operation.Kind == "session-recovery" {
			if _, err := agentOperationSession(ctx, q, operation.TargetID, operation.SessionID, now); err != nil {
				switch domain.AsFault(err).Code {
				case domain.NotFound, domain.Denied:
					return project("rejected", "session-unavailable", max(operation.CreatedAt, now)), nil
				case domain.Prerequisite:
					return project("rejected", "session-live", max(operation.CreatedAt, now)), nil
				default:
					return domain.AgentOperation{}, err
				}
			}
		}
	}
	if operation.Result != nil {
		result := *operation.Result
		result.AutomationMayResume = result.AutomationMayResume && target.Mode == "ai" && now < operation.ExpiresAt && operation.FinishedAt < operation.ExpiresAt
		operation.Result = &result
	}
	return operation, nil
}

func saveAgentOperation(ctx context.Context, tx *sql.Tx, operation domain.AgentOperation) error {
	operation.ServerObservedAt = 0
	message, err := get[domain.Message](ctx, tx, "messages", operation.ID)
	if err != nil {
		return err
	}
	message.AgentOperation, message.Revision = &operation, operation.Revision
	if domain.AgentOperationTerminal(operation.State) {
		message.State = "handled"
	}
	data, err := encode(message)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE messages SET data=?,state=? WHERE id=?", data, message.State, message.ID)
	return err
}

func safeAgentInspection(ctx context.Context, q queryer, operation domain.AgentOperation, target domain.Agent, inspection *domain.AgentInspection) (*domain.AgentInspection, error) {
	if inspection == nil {
		return nil, nil
	}
	copy := *inspection
	if inspection.Health != nil {
		h := *inspection.Health
		h.Problems = append([]domain.HealthProblem{}, h.Problems...)
		if err := sanitizeHealthReferences(ctx, q, target, &h); err != nil {
			return nil, err
		}
		h.JournalID = ""
		for _, action := range []struct {
			kind *string
			id   *string
		}{{&h.ActionKind, &h.ActionID}, {&h.LastActionKind, &h.LastActionID}} {
			if *action.kind == "message" && *action.id != "" && operation.RequesterID != target.ID {
				message, err := get[domain.Message](ctx, q, "messages", *action.id)
				if err != nil {
					return nil, err
				}
				if message.SenderID != operation.RequesterID && message.RecipientID != operation.RequesterID {
					*action.id = ""
				}
			} else if *action.kind == "onboarding" || *action.kind == "research" {
				*action.id = ""
			}
			if *action.id == "" && slices.Contains([]string{"message", "attention", "task"}, *action.kind) {
				*action.kind = ""
			}
		}
		copy.Health = &h
	}
	if inspection.Provider != nil {
		provider := *inspection.Provider
		copy.Provider = &provider
	}
	if inspection.SessionFence != nil {
		fence := *inspection.SessionFence
		session, err := get[domain.Session](ctx, q, "sessions", fence.SessionID)
		if err != nil {
			if domain.AsFault(err).Code != domain.NotFound {
				return nil, err
			}
			if fence.SessionState != "unknown" || fence.RecoveryState != "session-unavailable" {
				return nil, domain.Fail(domain.Invalid, "missing session must remain an unavailable observation")
			}
		} else if session.TargetID != target.ID {
			return nil, domain.Fail(domain.Denied, "inspection fence must belong to the operation target")
		}
		copy.SessionFence = &fence
	}
	if err := domain.ValidateAgentInspection(copy); err != nil {
		return nil, err
	}
	return &copy, nil
}
