package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

type agentRestartRecord struct {
	Permit    domain.AgentRestartPermit `json:"permit"`
	PriorMode string                    `json:"prior_mode"`
	Lease     *domain.UpgradeLease      `json:"lease,omitempty"`
}

func readAgentRestart(ctx context.Context, q queryer, id string) (agentRestartRecord, error) {
	var record agentRestartRecord
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", "agent-restart:"+id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return record, domain.Fail(domain.NotFound, "native restart authority is not recorded")
		}
		return record, err
	}
	err := json.Unmarshal([]byte(raw), &record)
	return record, err
}

func saveAgentRestart(ctx context.Context, tx *sql.Tx, id string, record agentRestartRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		"agent-restart:"+id, string(raw))
	return err
}

func (s *Store) RequestAgentRestart(ctx context.Context, actor, targetID string, request domain.AgentRestartRequest, sign func(domain.AgentRestartPermit) (string, error)) (domain.AgentOperation, error) {
	operation, err := mutate(ctx, s, actor, "agent-operation.native-restart:"+targetID, request.Command, request, func(tx *sql.Tx, founder domain.Agent) (domain.AgentOperation, error) {
		if !founder.Founder {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "only Founder may restart native agent services")
		}
		if err := domain.ValidateDiagnosticRefreshRequest(request); err != nil {
			return domain.AgentOperation{}, err
		}
		target, err := activeIdentitySubject(ctx, tx, targetID)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", target.ID)
		if err != nil || identity.State != "active" {
			return domain.AgentOperation{}, errors.Join(err, domain.Fail(domain.Denied, "native restart requires the original active computer binding"))
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE recipient_id=? AND state='pending'
AND json_extract(data,'$.agent_operation.kind')='native-restart'
AND json_extract(data,'$.agent_operation.expires_at')>?`, target.ID, s.timestamp()).Scan(&pending); err != nil {
			return domain.AgentOperation{}, err
		}
		if pending != 0 {
			return domain.AgentOperation{}, domain.Fail(domain.Conflict, "the original native restart is still pending; inspect its exact operation")
		}
		binding := founder.ID
		if certificate, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok {
			binding = certificate.BindingID()
		}
		issuer, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", binding)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		if issuer.AgentID != founder.ID || issuer.State != "active" {
			return domain.AgentOperation{}, domain.Fail(domain.Denied, "native restart requires the exact current Founder device")
		}
		prior := target.Mode
		if prior == "ai" {
			target, err = s.setAgentMode(ctx, tx, target, founder.ID, "manual", "Temporary pause for the exact Founder native restart")
			if err != nil {
				return domain.AgentOperation{}, err
			}
		}
		operation, err := s.createAgentOperation(ctx, tx, founder, target.ID, "native-restart", request.Reason, "", "")
		if err != nil {
			return domain.AgentOperation{}, err
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		permit := domain.AgentRestartPermit{ID: operation.ID, InstanceID: instance, RequesterID: founder.ID,
			RequesterBindingID: binding, RequesterGeneration: issuer.Generation, TargetID: target.ID,
			TargetGeneration: identity.Generation, ModeRevision: target.Revision, Reason: request.Reason,
			CreatedAt: operation.CreatedAt, ExpiresAt: operation.ExpiresAt}
		if err := upgrade.ValidateRestartPermit(permit); err != nil {
			return domain.AgentOperation{}, err
		}
		permit.Signature, err = sign(permit)
		if err != nil {
			return domain.AgentOperation{}, err
		}
		return operation, saveAgentRestart(ctx, tx, operation.ID, agentRestartRecord{Permit: permit, PriorMode: prior})
	})
	return s.agentOperationResponse(ctx, actor, operation, err, false)
}

func agentRestartCurrent(ctx context.Context, q queryer, record agentRestartRecord) (domain.Agent, error) {
	permit := record.Permit
	founder, err := activeIdentitySubject(ctx, q, permit.RequesterID)
	if err != nil {
		return domain.Agent{}, err
	}
	issuer, err := get[domain.AgentIdentity](ctx, q, "agent_identities", permit.RequesterBindingID)
	if err != nil {
		return domain.Agent{}, err
	}
	target, err := activeIdentitySubject(ctx, q, permit.TargetID)
	if err != nil {
		return target, err
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", permit.TargetID)
	if err != nil {
		return target, err
	}
	if !founder.Founder || issuer.AgentID != founder.ID || issuer.State != "active" ||
		issuer.Generation != permit.RequesterGeneration || identity.State != "active" || identity.Generation != permit.TargetGeneration {
		return target, domain.Fail(domain.Denied, "native restart issuer or computer generation changed")
	}
	return target, nil
}

func (s *Store) NativeAgentRestarts(ctx context.Context) ([]domain.AgentRestartPermit, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.QueryContext(ctx, `SELECT data FROM messages WHERE state='pending'
AND json_extract(data,'$.agent_operation.kind')='native-restart'
AND json_extract(data,'$.agent_operation.state') IN ('queued','checking')
ORDER BY id LIMIT 32`)
	if err != nil {
		return nil, err
	}
	messages, err := scanJSON[domain.Message](rows)
	if err != nil {
		return nil, err
	}
	permits := []domain.AgentRestartPermit{}
	for _, message := range messages {
		record, err := readAgentRestart(ctx, tx, message.ID)
		if err != nil {
			return nil, err
		}
		permits = append(permits, record.Permit)
	}
	return permits, nil
}

func (s *Store) LeaseAgentRestart(ctx context.Context, request domain.AgentRestartLeaseRequest, sign func(domain.UpgradeLease) (string, error)) (domain.UpgradeLease, error) {
	return transact(ctx, s, "native-maintenance", "agent-restart.lease:"+request.ID, request.OperationID, request, func(tx *sql.Tx) (domain.UpgradeLease, error) {
		operation, err := getAgentOperation(ctx, tx, request.ID)
		if err != nil {
			return domain.UpgradeLease{}, err
		}
		record, err := readAgentRestart(ctx, tx, request.ID)
		if err != nil {
			return domain.UpgradeLease{}, err
		}
		target, err := agentRestartCurrent(ctx, tx, record)
		if err != nil {
			return domain.UpgradeLease{}, err
		}
		now := s.timestamp()
		if operation.Kind != "native-restart" || operation.State != "checking" || now+30 >= operation.ExpiresAt ||
			target.Mode != "manual" || target.Revision != record.Permit.ModeRevision || record.Lease != nil ||
			request.ExpectedRevision != 0 || len(request.Nonce) != 43 || request.PermitSHA256 != domain.Hash(record.Permit.Signature) {
			return domain.UpgradeLease{}, domain.Fail(domain.Denied, "native restart start requires exact current paused authority and an unused nonce")
		}
		lease := domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce, IssuedAt: now, ExpiresAt: now + 30}
		lease.Signature, err = sign(lease)
		if err != nil {
			return lease, err
		}
		record.Lease = &lease
		return lease, saveAgentRestart(ctx, tx, request.ID, record)
	})
}

func (s *Store) ReportNativeAgentRestart(ctx context.Context, request domain.AgentRestartReport) (domain.AgentOperation, error) {
	return transact(ctx, s, "native-maintenance", "agent-restart.result:"+request.ID, request.OperationID, request, func(tx *sql.Tx) (domain.AgentOperation, error) {
		operation, err := getAgentOperation(ctx, tx, request.ID)
		if err != nil {
			return operation, err
		}
		record, err := readAgentRestart(ctx, tx, request.ID)
		if err != nil {
			return operation, err
		}
		if operation.Kind != "native-restart" || domain.AgentOperationTerminal(operation.State) {
			return operation, domain.Fail(domain.Conflict, "native restart result requires its exact unfinished operation")
		}
		if err := expect(operation.Revision, request.ExpectedRevision); err != nil {
			return operation, err
		}
		report := domain.AgentOperationReport{Command: request.Command, State: request.State, Result: request.Result}
		if err := domain.ValidateAgentOperationOutcome(operation, report); err != nil {
			return operation, err
		}
		target, currentErr := agentRestartCurrent(ctx, tx, record)
		now := s.timestamp()
		if request.State == "checking" {
			if currentErr != nil || operation.State != "queued" || now >= operation.ExpiresAt ||
				target.Mode != "manual" || target.Revision != record.Permit.ModeRevision {
				return operation, errors.Join(currentErr, domain.Fail(domain.Denied, "native restart admission authority changed or expired"))
			}
			operation.State, operation.StartedAt = "checking", now
		} else {
			if request.State == "restarted" && (currentErr != nil || record.Lease == nil ||
				operation.StartedAt == 0 || now >= operation.ExpiresAt || request.Result.NativeRuntime.IdentityGeneration != operation.TargetGeneration) {
				return operation, errors.Join(currentErr, domain.Fail(domain.Denied, "native restart success requires current admitted service evidence before expiry"))
			}
			result := *request.Result
			result.CapturedAt, result.ModeAtCheck, result.AutomationMayResume = now, target.Mode, false
			if currentErr != nil {
				result.ModeAtCheck = "manual"
			}
			canRestore := request.State == "restarted" && result.NativeRuntime != nil &&
				result.NativeRuntime.PendingInvocations == 0 && result.NativeRuntime.PendingReports == 0 && !result.NativeRuntime.SessionFenced
			noNativeEffect := request.State == "rejected" && record.Lease == nil
			if canRestore || noNativeEffect {
				if err := upgradeQuiescent(ctx, tx, domain.UpgradeChallenge{Role: "agent", TargetID: operation.TargetID}, now); err != nil {
					if domain.AsFault(err).Code != domain.Prerequisite {
						return operation, err
					}
					canRestore, noNativeEffect = false, false
				}
			}
			if currentErr == nil && record.PriorMode == "ai" && (canRestore || noNativeEffect) &&
				target.Mode == "manual" && target.Revision == record.Permit.ModeRevision && target.ModeRequestedBy == operation.RequesterID {
				target, err = s.setAgentMode(ctx, tx, target, operation.RequesterID, "ai", "Restore only the unchanged verified native-restart pause")
				if err != nil {
					return operation, err
				}
				result.ModeAtCheck = target.Mode
				result.AutomationMayResume = canRestore
			}
			operation.State, operation.Result, operation.FinishedAt = request.State, &result, now
		}
		operation.Revision++
		if err := domain.ValidateAgentOperation(operation); err != nil {
			return operation, err
		}
		if err := saveAgentOperation(ctx, tx, operation); err != nil {
			return operation, err
		}
		return operation, s.audit(ctx, tx, "native-maintenance", "agent-operation."+operation.State+":"+operation.ID, "accepted")
	})
}
