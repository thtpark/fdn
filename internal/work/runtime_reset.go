package work

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
)

var errRuntimeReset = domain.Fail(domain.Cancelled, "Founder requested a runtime reset")

func (e *Engine) beginRuntimeWork(ctx context.Context) (context.Context, func(), error) {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	if e.resetting {
		return ctx, nil, domain.Fail(domain.Prerequisite, "Founder runtime reset is joining owned work")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	if e.runtimeWork == nil {
		e.runtimeWork = make(map[chan struct{}]context.CancelCauseFunc)
	}
	e.runtimeWork[done] = cancel
	return ctx, func() {
		cancel(nil)
		e.runtimeMu.Lock()
		delete(e.runtimeWork, done)
		close(done)
		e.runtimeMu.Unlock()
	}, nil
}

func (e *Engine) resetAgentRuntime(ctx context.Context, c config.Worker, operation domain.AgentOperation, me domain.Agent) (reset bool, err error) {
	lock, err := secure.Acquire(filepath.Join(c.StateDir, "runtime-reset.lock"))
	if err != nil {
		if domain.AsFault(err).Code == domain.Conflict {
			return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "slot-occupied", nil)
		}
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	fingerprint, err := agentOperationHash(operation)
	if err != nil {
		return false, err
	}
	startKey := "agent-reset-start:" + operation.ID
	var previous string
	if found, err := e.State.Get(ctx, startKey, &previous); err != nil {
		return false, err
	} else if found {
		if previous != fingerprint {
			return false, domain.Fail(domain.Conflict, "retained runtime reset belongs to a different exact request")
		}
		// A crash after cancellation is not authority to cancel newer work again.
		return false, e.recordAgentOperationResult(ctx, operation, me, "operator-required", "reset-interrupted", nil)
	}
	current, me, clock, err := e.currentAgentOperation(ctx, operation.ID)
	if err != nil {
		return false, err
	}
	currentHash, err := agentOperationHash(current)
	if err != nil {
		return false, err
	}
	if currentHash != fingerprint {
		return false, domain.Fail(domain.Conflict, "runtime reset request changed before cancellation")
	}
	ctx = context.WithValue(ctx, agentOperationClockContext{}, clock)
	if current.State != "checking" {
		return false, nil
	}
	remaining := current.ExpiresAt - operationCapturedAt(ctx)
	if remaining <= 1 {
		return false, e.recordAgentOperationResult(ctx, current, me, "expired", "operation-expired", nil)
	}
	e.runtimeMu.Lock()
	if e.resetting {
		e.runtimeMu.Unlock()
		return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "slot-occupied", nil)
	}
	e.resetting = true
	work := make(map[chan struct{}]context.CancelCauseFunc, len(e.runtimeWork))
	for done, cancel := range e.runtimeWork {
		work[done] = cancel
	}
	sessions := e.sessions
	e.runtimeMu.Unlock()
	paused := false
	defer func() {
		if paused {
			err = errors.Join(err, sessions.resume())
		}
		e.runtimeMu.Lock()
		e.resetting = false
		e.runtimeMu.Unlock()
	}()
	if err := e.State.Put(ctx, startKey, fingerprint); err != nil {
		return false, err
	}
	current, me, clock, err = e.currentAgentOperation(ctx, operation.ID)
	if err != nil {
		return false, err
	}
	ctx = context.WithValue(ctx, agentOperationClockContext{}, clock)
	currentHash, err = agentOperationHash(current)
	if err != nil || currentHash != fingerprint {
		return false, errors.Join(err, domain.Fail(domain.Conflict, "runtime reset authority changed before cancellation"))
	}
	if current.State != "checking" {
		return false, nil
	}
	remaining = current.ExpiresAt - operationCapturedAt(ctx)
	if remaining <= 1 {
		return false, e.recordAgentOperationResult(ctx, current, me, "expired", "operation-expired", nil)
	}
	resetCtx, cancel := context.WithTimeout(ctx, min(30*time.Second, time.Duration(remaining-1)*time.Second))
	defer cancel()
	for _, cancel := range work {
		cancel(errRuntimeReset)
	}
	if sessions != nil {
		paused = true
		if err := sessions.pause(resetCtx); err != nil {
			e.recordProblem(ctx, "runtime-reset-problem", err)
			return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "runtime-unavailable", nil)
		}
	}
	for done := range work {
		select {
		case <-resetCtx.Done():
			return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "slot-occupied", nil)
		case <-done:
		}
	}
	slot, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		if domain.AsFault(err).Code == domain.Conflict {
			return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "slot-occupied", nil)
		}
		return false, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	snapshot, err := e.State.WorkSnapshot(resetCtx, []string{"maintenance", "session-fence"})
	if err != nil {
		return false, err
	}
	if len(snapshot.Journals) != 0 {
		code := "work-pending"
		for _, journal := range snapshot.Journals {
			if journal.State == "invoking" || journal.State == "uncertain" {
				code = "invocation-uncertain"
				break
			}
		}
		return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", code, nil)
	}
	if snapshot.PendingReports != 0 {
		return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "report-pending", nil)
	}
	var maintenance struct {
		Active bool `json:"active"`
	}
	if data := snapshot.Values["maintenance"]; len(data) != 0 {
		if err := json.Unmarshal(data, &maintenance); err != nil {
			return false, err
		}
	}
	if maintenance.Active {
		return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "maintenance-active", nil)
	}
	fence, err := remote.ReadSessionFence(resetCtx, e.State)
	if err != nil {
		return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "cleanup-unproven", nil)
	}
	if fence.Fence.Active {
		session, err := api.Call[domain.Session](resetCtx, e.Client, http.MethodGet, api.Path("sessions", fence.Fence.ID), nil)
		if err != nil || session.TargetID != me.ID || !domain.TerminalSessionState(session.State) {
			return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "session-unavailable", nil)
		}
		if !fence.Fence.CleanupProven {
			authorized, _, _, err := e.currentAgentOperation(resetCtx, operation.ID)
			if err != nil {
				return false, err
			}
			hash, err := agentOperationHash(authorized)
			if err != nil || hash != fingerprint || authorized.State != "checking" {
				return false, errors.Join(err, domain.Fail(domain.Denied, "runtime reset authority changed before native cleanup"))
			}
			cleanup := func() error { return (remote.Native{}).ResetSession(resetCtx, session, c, fence) }
			if sessions != nil {
				resetter, ok := sessions.runner.(interface {
					PrepareReset(context.Context, domain.Session, remote.SessionFenceSnapshot) error
				})
				if !ok {
					return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "cleanup-unproven", nil)
				}
				cleanup = func() error { return resetter.PrepareReset(resetCtx, session, fence) }
			}
			if err := cleanup(); err != nil {
				e.recordProblem(ctx, "runtime-reset-problem", err)
				return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "cleanup-unproven", nil)
			}
			if err := e.State.ProveSessionCleanup(resetCtx, session.ID, fence.Fingerprint); err != nil {
				return false, err
			}
			fence, err = remote.ReadSessionFence(resetCtx, e.State)
			if err != nil {
				return false, err
			}
		}
	} else if sessions != nil {
		if cleanup, ok := sessions.runner.(interface{ Cleanup(context.Context) error }); ok {
			if err := cleanup.Cleanup(resetCtx); err != nil {
				e.recordProblem(ctx, "runtime-reset-problem", err)
				return false, e.recordAgentOperationResult(ctx, current, me, "operator-required", "cleanup-unproven", nil)
			}
		}
	}
	current, me, clock, err = e.currentAgentOperation(resetCtx, operation.ID)
	if err != nil {
		return false, err
	}
	ctx = context.WithValue(ctx, agentOperationClockContext{}, clock)
	if current.State != "checking" {
		return false, nil
	}
	currentHash, err = agentOperationHash(current)
	if err != nil || currentHash != fingerprint {
		return false, errors.Join(err, domain.Fail(domain.Conflict, "runtime reset authority changed before completion"))
	}
	observation := &domain.AgentInspection{ObservedAt: e.Now().UTC().Unix(), WorkSlot: "free"}
	receipt, err := e.operationReceipt(ctx, current, me, "reset", "runtime-reset", observation)
	if err != nil {
		return false, err
	}
	if receipt.Report.State != "reset" {
		return false, e.recordAgentOperationResult(ctx, current, me, "expired", "operation-expired", nil)
	}
	if fence.Fence.Active {
		err = e.State.CommitSessionRecovery(resetCtx, fence.Fence.ID, fence.Fingerprint, local.AgentOperationResultKey(current.ID), receipt)
	} else {
		err = e.State.Put(resetCtx, local.AgentOperationResultKey(current.ID), receipt)
	}
	if err != nil {
		return false, err
	}
	e.clearProblem(ctx, "runtime-reset-problem")
	return true, e.deliverAgentOperationResult(ctx, receipt)
}
