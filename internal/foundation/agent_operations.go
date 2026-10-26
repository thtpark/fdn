package foundation

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) RequestDiagnosticRefresh(ctx context.Context, actor, target string, req domain.DiagnosticRefreshRequest) (domain.AgentOperation, error) {
	operation, err := mutate(ctx, s, actor, "agent-operation.diagnostics:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AgentOperation, error) {
		if !a.Founder {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "only Founder may request fresh remote diagnostics")
		}
		if err := domain.ValidateDiagnosticRefreshRequest(req); err != nil {
			return domain.AgentOperation{}, err
		}
		return s.createAgentOperation(ctx, tx, a, target, "diagnostics", req.Reason, "", "")
	})
	return s.agentOperationResponse(ctx, actor, operation, err, false)
}

func (s *Store) RequestSessionRecovery(ctx context.Context, actor, target string, req domain.SessionRecoveryRequest) (domain.AgentOperation, error) {
	operation, err := mutate(ctx, s, actor, "agent-operation.session-recovery:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AgentOperation, error) {
		if !a.Founder {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "only Founder may request exact closed-session runtime recovery")
		}
		if err := domain.ValidateSessionRecoveryRequest(req); err != nil {
			return domain.AgentOperation{}, err
		}
		if _, err := agentOperationSession(ctx, tx, target, req.SessionID, s.timestamp()); err != nil {
			return domain.AgentOperation{}, err
		}
		return s.createAgentOperation(ctx, tx, a, target, "session-recovery", req.Reason, req.SessionID, req.ExpectedFence)
	})
	return s.agentOperationResponse(ctx, actor, operation, err, false)
}

func (s *Store) createAgentOperation(ctx context.Context, tx *sql.Tx, requester domain.Agent, targetID, kind, reason, sessionID, fence string) (domain.AgentOperation, error) {
	target, err := activeIdentitySubject(ctx, tx, targetID)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	requesterGeneration, requesterActive, err := agentOperationIdentity(ctx, tx, requester)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	targetGeneration, targetActive, err := agentOperationIdentity(ctx, tx, target)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	if !requesterActive || !targetActive {
		return domain.AgentOperation{}, domain.Fail(domain.Denied, "agent operation requires active issuer and target identities")
	}
	now := s.timestamp()
	operation := domain.AgentOperation{Version: domain.AgentOperationVersion, ID: domain.NewID("agent_operation"), Revision: 1,
		Kind: kind, RequesterID: requester.ID, TargetID: target.ID, RequesterGeneration: requesterGeneration, TargetGeneration: targetGeneration,
		State: "queued", Reason: reason, SessionID: sessionID, ExpectedFence: fence, CreatedAt: now, ExpiresAt: now + domain.AgentOperationTTL}
	if err := domain.ValidateAgentOperation(operation); err != nil {
		return domain.AgentOperation{}, err
	}
	message := domain.Message{ID: operation.ID, SenderID: requester.ID, RecipientID: target.ID,
		Body:   "Founder requested a protected runtime inspection or exact closed-session fence check. Only the agent-operation protocol may process this control; it is not model context or work, sign-in, retry, mode, or spending permission.",
		Origin: "service", Kind: "agent-operation", Urgent: true, State: "pending", CreatedAt: now, ExpiresAt: operation.ExpiresAt, Revision: operation.Revision, AgentOperation: &operation}
	if _, err := insertMessage(ctx, tx, message); err != nil {
		return domain.AgentOperation{}, err
	}
	if err := s.audit(ctx, tx, requester.ID, "agent-operation.queued:"+operation.ID, "accepted"); err != nil {
		return domain.AgentOperation{}, err
	}
	return operation, nil
}

func (s *Store) GetAgentOperation(ctx context.Context, actor, id string) (domain.AgentOperation, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	defer tx.Close()
	viewer, err := s.actor(ctx, tx, actor)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	operation, err := getAgentOperation(ctx, tx, id)
	if err != nil {
		return domain.AgentOperation{}, err
	}
	if viewer.ID != operation.TargetID && (viewer.ID != operation.RequesterID || !viewer.Founder) {
		return domain.AgentOperation{}, domain.Fail(domain.Denied, "agent operation is private to its target and issuing Founder")
	}
	return projectAgentOperation(ctx, tx, operation, s.timestamp())
}

func (s *Store) ListAgentOperations(ctx context.Context, actor, cursor string, limit int) (domain.Page[domain.AgentOperation], error) {
	page := domain.Page[domain.AgentOperation]{Items: []domain.AgentOperation{}}
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Close()
	target, err := s.actor(ctx, tx, actor)
	if err != nil {
		return page, err
	}
	generation, active, err := agentOperationIdentity(ctx, tx, target)
	if err != nil {
		return page, err
	}
	if !active {
		return page, domain.Fail(domain.Denied, "only a current target identity may list its pending controls")
	}
	limit, err = limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "agent-operations:" + strconv.FormatInt(generation, 10)
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	now := s.timestamp()
	for len(page.Items) <= limit {
		rows, err := tx.QueryContext(ctx, `SELECT data FROM messages WHERE recipient_id=? AND state='pending' AND id>?
AND json_extract(data,'$.kind')='agent-operation' AND json_extract(data,'$.origin')='service'
AND json_extract(data,'$.agent_operation.state') IN ('queued','checking')
AND json_extract(data,'$.agent_operation.target_generation')=? AND json_extract(data,'$.agent_operation.expires_at')>?
ORDER BY id LIMIT ?`, actor, last, generation, now, limit+1)
		if err != nil {
			return page, err
		}
		messages, err := scanJSON[domain.Message](rows)
		if err != nil {
			return page, err
		}
		for _, message := range messages {
			last = message.ID
			operation, err := agentOperationFromMessage(message)
			if err != nil {
				return page, err
			}
			operation, err = projectAgentOperation(ctx, tx, operation, now)
			if err != nil {
				return page, err
			}
			if operation.State == "queued" || operation.State == "checking" {
				page.Items = append(page.Items, operation)
				if len(page.Items) > limit {
					break
				}
			}
		}
		if len(messages) < limit+1 {
			break
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].ID)
	}
	return page, nil
}

func (s *Store) ReportAgentOperation(ctx context.Context, actor, id string, req domain.AgentOperationReport) (domain.AgentOperation, error) {
	operation, err := mutate(ctx, s, actor, "agent-operation.result:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.AgentOperation, error) {
		operation, err := getAgentOperation(ctx, tx, id)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		if operation.TargetID != a.ID {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "only the exact target may report this agent operation")
		}
		target, allowed, err := agentOperationParties(ctx, tx, operation)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		if !allowed {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "agent operation issuer or target authority changed")
		}
		if err := domain.ValidateAgentOperationOutcome(operation, req); err != nil {
			return domain.AgentOperation{}, err
		}
		if err := expect(operation.Revision, req.ExpectedRevision); err != nil {
			return domain.AgentOperation{}, err
		}
		if domain.AgentOperationTerminal(operation.State) {
			return domain.AgentOperation{}, domain.Fail(domain.Conflict, "a final agent operation cannot be changed")
		}
		now := s.timestamp()
		if req.State == "checking" {
			if operation.State != "queued" || now >= operation.ExpiresAt {
				return domain.AgentOperation{}, domain.Fail(domain.Conflict, "only a nonexpired queued operation may begin checking")
			}
			if operation.Kind == "session-recovery" {
				if _, err := agentOperationSession(ctx, tx, operation.TargetID, operation.SessionID, now); err != nil {
					return domain.AgentOperation{}, err
				}
			}
			operation.State, operation.StartedAt = "checking", now
		} else {
			if operation.State != "checking" || operation.StartedAt == 0 || operation.StartedAt >= operation.ExpiresAt {
				return domain.AgentOperation{}, domain.Fail(domain.Conflict, "a final result requires its durable pre-expiry checking receipt")
			}
			result := *req.Result
			if req.State == "expired" && now < operation.ExpiresAt {
				return domain.AgentOperation{}, domain.Fail(domain.Conflict, "the fixed server operation deadline has not expired")
			}
			// On-time server receipt bounds completion independently of target UTC.
			// Late receipts instead need the retained server-clock capture bound.
			if now >= operation.ExpiresAt && req.State != "expired" && (result.CapturedAt < operation.StartedAt || result.CapturedAt >= operation.ExpiresAt) {
				return domain.AgentOperation{}, domain.Fail(domain.Conflict, "late evidence requires a pre-expiry server-clock capture bound and prior checking receipt")
			}
			if req.State == "recovered" {
				if _, err := agentOperationSession(ctx, tx, operation.TargetID, operation.SessionID, now); err != nil {
					return domain.AgentOperation{}, err
				}
			}
			result.Observation, err = safeAgentInspection(ctx, tx, operation, target, result.Observation)
			if err != nil {
				return domain.AgentOperation{}, err
			}
			operation.State, operation.Result, operation.FinishedAt = req.State, &result, now
		}
		operation.Revision++
		if err := domain.ValidateAgentOperation(operation); err != nil {
			return domain.AgentOperation{}, err
		}
		if err := saveAgentOperation(ctx, tx, operation); err != nil {
			return domain.AgentOperation{}, err
		}
		if err := s.audit(ctx, tx, a.ID, "agent-operation."+operation.State+":"+operation.ID, "accepted"); err != nil {
			return domain.AgentOperation{}, err
		}
		return operation, nil
	})
	return s.agentOperationResponse(ctx, actor, operation, err, true)
}

func (s *Store) agentOperationResponse(ctx context.Context, actor string, operation domain.AgentOperation, err error, report bool) (domain.AgentOperation, error) {
	if err != nil {
		return domain.AgentOperation{}, err
	}
	current, err := s.GetAgentOperation(ctx, actor, operation.ID)
	if err == nil && report && current.State == "rejected" && current.Result != nil && current.Result.Code == "authority-changed" {
		return domain.AgentOperation{}, domain.Fail(domain.Denied, "agent operation issuer or target authority changed")
	}
	return current, err
}
