package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

const maxTextAttempts = 3

func (e *Engine) settleTextFailure(saveCtx, scheduling context.Context, c config.Worker, journal *local.Journal, launch Launch, cause error) error {
	hash := textActionHash(launch)
	var previous TextResponseBlock
	found, err := e.State.Get(saveCtx, "text-response:"+hash, &previous)
	if err != nil {
		return errors.Join(cause, err)
	}
	attempts := 1
	if found {
		if previous.Version != textResponseFormat || previous.ActionHash != hash || !domain.ValidOperation(previous.JournalID) {
			return errors.Join(cause, domain.Fail(domain.Prerequisite, "retained text retry does not match the exact source"))
		}
		if previous.State == "retry-wait" {
			attempts += previous.Attempts
		}
	}
	block := TextResponseBlock{Version: textResponseFormat, ActionHash: hash, JournalID: journal.ID,
		Phase: launch.Phase, State: "blocked", CreatedAt: e.Now().UTC().Unix(), Failure: "interrupted", Attempts: attempts}
	if launch.Task != nil {
		block.TaskID = launch.Task.ID
	}
	transient := !errors.Is(cause, errRuntimeReset) &&
		(errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || domain.AsFault(cause).Code == domain.Unavailable)
	if transient && attempts < maxTextAttempts {
		block.State = "retry-wait"
		block.RetryAt = e.Now().Add(time.Duration(5*attempts) * time.Second).Unix()
	}
	journal.State, journal.TextActionHash = "text-failed", hash
	journal.Problem = fmt.Sprintf("tool-disabled planning stopped before any reply was applied; attempt %d/%d; original private evidence retained", attempts, maxTextAttempts)
	if err := e.State.SaveTextResponse(saveCtx, *journal, hash, block); err != nil {
		return errors.Join(cause, err)
	}
	e.Log.Warn("tool-disabled planning did not complete", "journal", journal.ID, "attempt", attempts, "retry_at", block.RetryAt, "code", domain.AsFault(cause).Code)
	var helpErr error
	var prerequisiteErr error
	if block.State == "retry-wait" {
		deferWorkUntil(scheduling, time.Unix(block.RetryAt, 0))
	} else {
		_, helpErr = e.requestPermission(saveCtx, c, launch.Task, "text-response:"+journal.ID, "provider-response")
	}
	if !transient {
		prerequisiteErr = e.invocationBlocked(saveCtx, c, *journal, launch, cause)
	}
	return textResponseFenced{errors.Join(domain.Fail(domain.Prerequisite, "this tool-disabled source is retained for bounded retry or explicit recovery; unrelated work may continue"),
		cause, helpErr, prerequisiteErr)}
}

func (e *Engine) resolveTextRetry(ctx context.Context, launch Launch) error {
	hash := textActionHash(launch)
	var block TextResponseBlock
	found, err := e.State.Get(ctx, "text-response:"+hash, &block)
	if err != nil || !found {
		return err
	}
	if block.Failure != "interrupted" {
		return nil
	}
	if block.Version != textResponseFormat || block.ActionHash != hash || !domain.ValidOperation(block.JournalID) {
		return domain.Fail(domain.Prerequisite, "retained text retry does not match the completed source")
	}
	block.State, block.ResolvedAt, block.RetryAt = "resolved", e.Now().UTC().Unix(), 0
	return e.State.Put(ctx, "text-response:"+hash, block)
}
