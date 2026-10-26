package work

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
)

func agentOperationHash(operation domain.AgentOperation) (string, error) {
	request := operation
	request.Revision, request.StartedAt, request.FinishedAt, request.ServerObservedAt, request.State, request.Result = 0, 0, 0, 0, "", nil
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return domain.Hash(string(data)), nil
}

func (e *Engine) operationLoop(ctx context.Context, wake chan<- struct{}) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var due <-chan time.Time
	for {
		retry := false
		select {
		case <-ctx.Done():
			return
		case <-e.controlWake:
		case <-due:
			retry = true
		}
		timer.Stop()
		due = nil
		checkpoint, err := e.updateCheckpoint(ctx, nil)
		var blocked error
		if err != nil {
			blocked = err
		} else {
			if checkpoint.Mode.pending() {
				c, modeErr := e.currentConfig()
				if modeErr == nil {
					var me domain.Agent
					me, modeErr = api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
					if modeErr == nil {
						_, modeErr = e.applyMode(ctx, c, me)
					}
				}
				if modeErr == nil {
					_, modeErr = e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
						p.Mode.Handled = max(p.Mode.Handled, checkpoint.Mode.Requested)
					})
				}
				blocked = errors.Join(blocked, modeErr)
			}
			if checkpoint.Operations.pending() {
				scanErr, deliveryErr := e.scanAgentOperations(ctx, wake)
				if scanErr == nil {
					_, scanErr = e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
						p.Operations.Handled = max(p.Operations.Handled, checkpoint.Operations.Requested)
					})
				}
				blocked = errors.Join(blocked, scanErr, deliveryErr)
			}
			if checkpoint.Authentication.pending() {
				c, controlErr := e.currentConfig()
				if controlErr == nil {
					controlCtx := context.WithValue(ctx, backgroundControlContext{}, true)
					for {
						var handled bool
						handled, controlErr = e.authenticationControl(controlCtx, c)
						if controlErr != nil || !handled {
							break
						}
					}
				}
				if controlErr == nil {
					_, controlErr = e.updateCheckpoint(ctx, func(p *updateCheckpoint) {
						p.Authentication.Handled = max(p.Authentication.Handled, checkpoint.Authentication.Requested)
					})
				}
				blocked = errors.Join(blocked, controlErr)
			}
			if retry {
				blocked = errors.Join(blocked, e.deliverPendingAgentOperationResults(ctx), e.retryHelpDeliveries(ctx))
			}
		}
		receipts, receiptErr := e.State.PendingAgentOperationResults(ctx, 1)
		helpPending, helpErr := e.pendingHelpDeliveries(ctx)
		blocked = errors.Join(blocked, receiptErr, helpErr)
		if blocked != nil {
			if ctx.Err() == nil {
				e.recordProblem(ctx, "agent-control-problem", blocked)
			}
		} else {
			e.clearProblem(ctx, "agent-control-problem")
		}
		if blocked != nil || len(receipts) != 0 || helpPending {
			timer.Reset(5 * time.Second)
			due = timer.C
		}
	}
}

func (e *Engine) processAgentOperations(ctx context.Context, wake chan<- struct{}) error {
	deliveryErr := e.deliverPendingAgentOperationResults(ctx)
	scanErr, pendingErr := e.scanAgentOperations(ctx, wake)
	return errors.Join(deliveryErr, scanErr, pendingErr)
}

func (e *Engine) deliverPendingAgentOperationResults(ctx context.Context) error {
	for {
		receipts, err := e.State.PendingAgentOperationResults(ctx, 20)
		if err != nil {
			return err
		}
		var blocked error
		for _, receipt := range receipts {
			blocked = errors.Join(blocked, e.deliverAgentOperationResult(ctx, receipt))
			if ctx.Err() != nil {
				return errors.Join(blocked, ctx.Err())
			}
		}
		if blocked != nil || len(receipts) < 20 {
			return blocked
		}
	}
}

func (e *Engine) scanAgentOperations(ctx context.Context, wake chan<- struct{}) (error, error) {
	var blocked, deliveryErr error
	cursor := ""
	for {
		path := "/v1/agent-operations?limit=20"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := api.Call[domain.Page[domain.AgentOperation]](ctx, e.Client, http.MethodGet, path, nil)
		if err != nil {
			return errors.Join(blocked, err), deliveryErr
		}
		for _, operation := range page.Items {
			recovered, err := e.handleAgentOperation(ctx, operation.ID)
			if err != nil {
				var receipt local.AgentOperationReceipt
				if found, readErr := e.State.Get(ctx, local.AgentOperationResultKey(operation.ID), &receipt); readErr == nil && found && receipt.DeliveryState == "pending" {
					deliveryErr = errors.Join(deliveryErr, err)
				} else {
					blocked = errors.Join(blocked, err, readErr)
				}
			}
			if recovered {
				blocked = errors.Join(blocked, e.requestUpdates(ctx, updateWork, wake))
			}
			if ctx.Err() != nil {
				return errors.Join(blocked, ctx.Err()), deliveryErr
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return blocked, deliveryErr
}

func (e *Engine) currentAgentOperation(ctx context.Context, id string) (domain.AgentOperation, domain.Agent, agentOperationClock, error) {
	clock := agentOperationClock{requestStarted: time.Now()}
	operation, err := api.Call[domain.AgentOperation](ctx, e.Client, http.MethodGet, api.Path("agent-operations", id), nil)
	if err != nil {
		return operation, domain.Agent{}, clock, err
	}
	clock.serverAt = operation.ServerObservedAt
	if operation.Version != domain.AgentOperationVersion || operation.ID != id || operation.TargetID != e.Client.Credential.AgentID ||
		!domain.ValidOperation(operation.ID) || operation.Revision < 1 || operation.ServerObservedAt <= 0 || !slices.Contains([]string{"diagnostics", "session-recovery"}, operation.Kind) {
		return operation, domain.Agent{}, clock, domain.Fail(domain.Denied, "agent operation does not match this enrolled worker")
	}
	me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return operation, me, clock, err
	}
	if me.ID != operation.TargetID || !me.Active || me.Banned {
		return operation, me, clock, domain.Fail(domain.Denied, "agent operation target identity is no longer current")
	}
	founder, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, api.Path("agents", operation.RequesterID), nil)
	if err != nil {
		return operation, me, clock, err
	}
	if !founder.Active || !founder.Founder || founder.Banned {
		return operation, me, clock, domain.Fail(domain.Denied, "agent operation no longer has an active Founder issuer")
	}
	return operation, me, clock, nil
}

func (e *Engine) handleAgentOperation(ctx context.Context, id string) (bool, error) {
	operation, me, clock, err := e.currentAgentOperation(ctx, id)
	if err != nil {
		return false, err
	}
	fingerprint, err := agentOperationHash(operation)
	if err != nil {
		return false, err
	}
	var previous local.AgentOperationReceipt
	if found, err := e.State.Get(ctx, local.AgentOperationResultKey(id), &previous); err != nil {
		return false, err
	} else if found {
		if previous.Version != domain.AgentOperationVersion || previous.RequestHash != fingerprint || previous.OperationID != id {
			return false, domain.Fail(domain.Conflict, "retained agent operation result does not match the exact request")
		}
		if previous.DeliveryState == "pending" {
			return false, e.deliverAgentOperationResult(ctx, previous)
		}
		return false, nil
	}
	if !slices.Contains([]string{"queued", "checking"}, operation.State) {
		return false, nil
	}
	if operation.State == "queued" {
		operation, err = commit[domain.AgentOperation](ctx, e, http.MethodPost, api.Path("agent-operations", id, "result"), domain.AgentOperationReport{
			Command: domain.Command{OperationID: id + ".checking", ExpectedRevision: operation.Revision}, State: "checking"})
		if err != nil {
			return false, err
		}
	}
	operation, me, clock, err = e.currentAgentOperation(ctx, id)
	if err != nil {
		return false, err
	}
	if operation.State != "checking" {
		return false, nil
	}
	ctx = context.WithValue(ctx, agentOperationClockContext{}, clock)
	if operationCapturedAt(ctx) >= operation.ExpiresAt {
		return false, e.recordAgentOperationResult(ctx, operation, me, "expired", "operation-expired", nil)
	}
	c, err := e.currentConfig()
	e.configMu.RLock()
	initial := e.Config
	e.configMu.RUnlock()
	if err != nil || filepath.Clean(c.StateDir) != filepath.Clean(e.State.Root) || c.CredentialFile != initial.CredentialFile ||
		c.IdentitySocket != initial.IdentitySocket || c.URL != initial.URL || c.CAFile != initial.CAFile || c.ServerName != initial.ServerName {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "configuration-unavailable", nil)
	}
	if operation.Kind == "diagnostics" {
		observation, err := e.inspectAgent(ctx, c, true)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "inspection-unavailable", nil)
		}
		return false, e.recordAgentOperationResult(ctx, operation, me, "refreshed", "observations-collected", observation)
	}
	return e.recoverAgentSession(ctx, c, operation, me)
}

func (e *Engine) inspectAgent(ctx context.Context, c config.Worker, probeProvider bool) (*domain.AgentInspection, error) {
	health, err := e.health(ctx)
	if err != nil {
		return nil, err
	}
	observation := &domain.AgentInspection{ObservedAt: e.Now().UTC().Unix(), Health: health, WorkSlot: "unknown"}
	if probeProvider {
		name := c.Provider
		if health != nil && domain.ValidProvider(health.Provider) {
			name = health.Provider
		}
		executor := e.Executor
		if manager, ok := executor.(*provider.Manager); ok {
			copy := *manager
			copy.Config = c
			executor = &copy
		}
		if executor == nil {
			return nil, domain.Fail(domain.Prerequisite, "read-only provider diagnostics are unavailable")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, checkErr := executor.Check(probeCtx, name)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		providerObservation := &domain.ProviderObservation{Provider: name, Authentication: "unknown", ObservedAt: e.Now().UTC().Unix()}
		if healthVersionPattern.MatchString(status.Version) {
			providerObservation.Version = status.Version
		}
		if checkErr != nil {
			providerObservation.ProblemCode = domain.AsFault(checkErr).Code
		} else if slices.Contains([]string{"unknown", "authenticated", "credentials-present-unverified", "login-required", "credentials-rejected"}, status.Authentication) {
			providerObservation.Authentication = status.Authentication
		}
		observation.Provider = providerObservation
	}
	slot, lockErr := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if lockErr == nil {
		observation.WorkSlot = "free"
		if err := slot.Close(); err != nil {
			return nil, err
		}
	} else if domain.AsFault(lockErr).Code == domain.Conflict {
		observation.WorkSlot = "occupied"
	}
	fence, err := remote.ReadSessionFence(ctx, e.State)
	if err != nil {
		return nil, err
	}
	if fence.Fence.Active {
		session, err := api.Call[domain.Session](ctx, e.Client, http.MethodGet, api.Path("sessions", fence.Fence.ID), nil)
		if err != nil || session.TargetID != e.Client.Credential.AgentID {
			observation.SessionFence = fence.RecoveryObservation(nil, observation.WorkSlot)
		} else {
			observation.SessionFence = fence.RecoveryObservation(&session, observation.WorkSlot)
		}
	}
	observation.ObservedAt = e.Now().UTC().Unix()
	return observation, nil
}

func (e *Engine) operationReceipt(ctx context.Context, operation domain.AgentOperation, me domain.Agent, state, code string, observation *domain.AgentInspection) (local.AgentOperationReceipt, error) {
	fingerprint, err := agentOperationHash(operation)
	if err != nil {
		return local.AgentOperationReceipt{}, err
	}
	captured := operationCapturedAt(ctx)
	if captured <= 0 {
		return local.AgentOperationReceipt{}, domain.Fail(domain.Prerequisite, "agent operation capture lacks a current authenticated server clock")
	}
	if captured >= operation.ExpiresAt && state == "refreshed" {
		state, code, observation = "expired", "operation-expired", nil
	}
	return local.AgentOperationReceipt{Version: domain.AgentOperationVersion, OperationID: operation.ID, RequestHash: fingerprint, DeliveryState: "pending",
		Report: domain.AgentOperationReport{Command: domain.Command{OperationID: operation.ID + ".result", ExpectedRevision: operation.Revision}, State: state,
			Result: &domain.AgentOperationResult{Code: code, CapturedAt: captured, ModeAtCheck: me.Mode, AutomationMayResume: state == "recovered" && me.Mode == "ai", Observation: observation}}}, nil
}

func (e *Engine) recordAgentOperationResult(ctx context.Context, operation domain.AgentOperation, me domain.Agent, state, code string, observation *domain.AgentInspection) error {
	receipt, err := e.operationReceipt(ctx, operation, me, state, code, observation)
	if err != nil {
		return err
	}
	if err := e.State.Put(ctx, local.AgentOperationResultKey(operation.ID), receipt); err != nil {
		return err
	}
	return e.deliverAgentOperationResult(ctx, receipt)
}

func (e *Engine) deliverAgentOperationResult(ctx context.Context, receipt local.AgentOperationReceipt) error {
	current, _, _, err := e.currentAgentOperation(ctx, receipt.OperationID)
	if err != nil {
		if domain.AsFault(err).Code == domain.Denied || domain.AsFault(err).Code == domain.NotFound {
			receipt.DeliveryState, receipt.DeliveryProblem = "rejected", domain.AsFault(err).Code
			return e.State.Put(ctx, local.AgentOperationResultKey(receipt.OperationID), receipt)
		}
		return err
	}
	fingerprint, err := agentOperationHash(current)
	if err != nil {
		return err
	}
	if fingerprint != receipt.RequestHash {
		return domain.Fail(domain.Conflict, "agent operation result has a different immutable request")
	}
	if _, err := commit[domain.AgentOperation](ctx, e, http.MethodPost, api.Path("agent-operations", receipt.OperationID, "result"), receipt.Report); err != nil {
		return err
	}
	receipt.DeliveryState, receipt.DeliveryProblem = "delivered", ""
	return e.State.Put(ctx, local.AgentOperationResultKey(receipt.OperationID), receipt)
}

func (e *Engine) recoverAgentSession(ctx context.Context, c config.Worker, operation domain.AgentOperation, me domain.Agent) (recovered bool, err error) {
	slot, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		if domain.AsFault(err).Code == domain.Conflict {
			return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "slot-occupied", nil)
		}
		return false, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	fence, err := remote.ReadSessionFence(ctx, e.State)
	if err != nil {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "cleanup-unproven", nil)
	}
	if !fence.Fence.Active || fence.Fence.ID != operation.SessionID || fence.Fingerprint != operation.ExpectedFence {
		return false, e.recordAgentOperationResult(ctx, operation, me, "rejected", "fence-mismatch", nil)
	}
	session, err := api.Call[domain.Session](ctx, e.Client, http.MethodGet, api.Path("sessions", operation.SessionID), nil)
	if err != nil || session.TargetID != me.ID {
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "session-unavailable", nil)
	}
	observation := &domain.AgentInspection{ObservedAt: e.Now().UTC().Unix(), WorkSlot: "free", SessionFence: fence.RecoveryObservation(&session, "free")}
	switch observation.SessionFence.RecoveryState {
	case "session-live":
		return false, e.recordAgentOperationResult(ctx, operation, me, "rejected", "session-live", observation)
	case "cleanup-unproven":
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "cleanup-unproven", observation)
	case "recoverable":
	default:
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "recovery-unavailable", observation)
	}
	// Recheck server authority and expiry immediately before the local transition.
	current, me, clock, err := e.currentAgentOperation(ctx, operation.ID)
	if err != nil {
		return false, err
	}
	if current.State != "checking" {
		return false, nil
	}
	ctx = context.WithValue(ctx, agentOperationClockContext{}, clock)
	if operationCapturedAt(ctx) >= current.ExpiresAt {
		return false, e.recordAgentOperationResult(ctx, current, me, "expired", "operation-expired", nil)
	}
	before, err := agentOperationHash(operation)
	if err != nil {
		return false, err
	}
	after, err := agentOperationHash(current)
	if err != nil {
		return false, err
	}
	if before != after {
		return false, domain.Fail(domain.Conflict, "session recovery request changed during inspection")
	}
	receipt, err := e.operationReceipt(ctx, current, me, "recovered", "session-recovered", observation)
	if err != nil {
		return false, err
	}
	remaining := current.ExpiresAt - receipt.Report.Result.CapturedAt
	if remaining <= 1 {
		return false, e.recordAgentOperationResult(ctx, current, me, "expired", "operation-expired", nil)
	}
	commitCtx, cancel := context.WithTimeout(ctx, time.Duration(remaining-1)*time.Second)
	defer cancel()
	if err := e.State.CommitSessionRecovery(commitCtx, current.SessionID, current.ExpectedFence, local.AgentOperationResultKey(current.ID), receipt); err != nil {
		return false, err
	}
	return true, e.deliverAgentOperationResult(ctx, receipt)
}
