package work

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

func (e *Engine) flushReports(ctx context.Context) error {
	items, err := e.State.Outbox(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := e.phaseHealth(ctx, "reporting", nil, "", 120, false); err != nil {
			return err
		}
		var outcome domain.ReportOutcome
		if err := e.Client.Request(ctx, item.Method, item.Path, item.Body, &outcome); err != nil {
			return errors.Join(err, e.blockHealth(ctx, "report-pending"))
		}
		receipt := ReportReceipt{TaskID: outcome.Task.ID, RunID: outcome.Run.ID, Accepted: outcome.Accepted, Stale: outcome.Stale, State: outcome.Run.State}
		if err := e.State.Put(ctx, "last-report", receipt); err != nil {
			return err
		}
		journals, err := e.State.Journals(ctx, true)
		if err != nil {
			return err
		}
		for _, j := range journals {
			if j.Report != nil && j.Report.OperationID == item.OperationID {
				if outcome.Stale {
					j.Problem = "stale run evidence retained; newer task generation was not completed"
				}
				j.State = "done"
				if err := e.State.SaveJournal(ctx, j); err != nil {
					return err
				}
			}
		}
		if outcome.Stale {
			e.Log.Warn("stale report reconciled without completing task", "task", outcome.Task.ID, "run", outcome.Run.ID)
		}
		if err := e.State.Acknowledge(ctx, item.OperationID); err != nil {
			return err
		}
		if err := e.completedHealth(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) reconcile(ctx context.Context, manual bool) (bool, error) {
	journals, err := e.State.Journals(ctx, true)
	if err != nil {
		return false, err
	}
	for _, j := range journals {
		if j.State == "report-prepared" {
			if err := e.queueReport(ctx, &j); err != nil {
				return false, err
			}
		}
	}
	items, err := e.State.Outbox(ctx)
	if err != nil {
		return false, err
	}
	if len(items) > 0 {
		if err := e.phaseHealth(ctx, "reporting", nil, "", 120, false); err != nil {
			return false, err
		}
		return true, e.flushReports(ctx)
	}
	for _, j := range journals {
		switch j.State {
		case "invoking", "uncertain":
			healthErr := e.journalHealth(ctx, j, "blocked")
			return false, errors.Join(domain.Fail(domain.Prerequisite, "interrupted invocation "+j.ID+" is fenced; confirm all of its processes stopped and use work recover"), healthErr, e.blockHealth(ctx, "invocation-uncertain"))
		case "claim-prepared":
			if j.Claim == nil {
				return false, domain.Fail(domain.Internal, "prepared claim is missing its exact request")
			}
			run, err := api.Call[domain.Run](ctx, e.Client, "POST", api.Path("tasks", j.TaskID, "claim"), j.Claim)
			if err != nil {
				if domain.AsFault(err).Code == domain.Conflict || domain.AsFault(err).Code == domain.Prerequisite || domain.AsFault(err).Code == domain.Denied {
					j.State, j.Problem = "stale", "prepared claim was not accepted or can no longer be reconciled"
					return true, errors.Join(err, e.State.SaveJournal(ctx, j))
				}
				return false, err
			}
			j.RunID, j.State = run.ID, "claimed"
			if err := e.State.SaveJournal(ctx, j); err != nil {
				return false, err
			}
			return true, e.notStarted(ctx, &j, "recovered a claim without an invocation fence")
		case "claimed":
			return true, e.notStarted(ctx, &j, "recovered a claim without an invocation fence")
		case "reply-prepared", "applying":
			if j.Kind != domain.ExecutionRun && j.Kind != domain.VerificationRun {
				c, err := e.currentConfig()
				if err != nil {
					return false, err
				}
				if !manual && c.Mode == "manual" {
					continue
				}
			}
			var launch Launch
			var reply provider.Reply
			if err := json.Unmarshal(j.Context, &launch); err != nil {
				return false, err
			}
			if err := json.Unmarshal(j.Reply, &reply); err != nil {
				return false, err
			}
			if j.Kind == domain.ExecutionRun || j.Kind == domain.VerificationRun {
				return true, e.finishExecution(ctx, &j, launch, reply)
			}
			return true, e.applyText(ctx, &j, launch, reply)
		case "prepared":
			j.State, j.Problem = "abandoned", "provider process was not started; next selection may prepare a fresh review"
			return true, e.State.SaveJournal(ctx, j)
		default:
			return false, domain.Fail(domain.Internal, "unknown durable invocation state")
		}
	}
	return false, nil
}

func (e *Engine) Recover(ctx context.Context, id string, acknowledgeStopped bool) error {
	if !acknowledgeStopped {
		return domain.Fail(domain.Prerequisite, "explicit confirmation that all processes for this invocation stopped is required")
	}
	slot, err := secure.Acquire(filepath.Join(e.Config.StateDir, "work.lock"))
	if err != nil {
		return err
	}
	defer slot.Close()
	j, err := e.State.Journal(ctx, id)
	if err != nil {
		return err
	}
	if j.State == "reply-rejected" {
		return e.recoverTextResponse(ctx, &j)
	}
	if j.State != "invoking" && j.State != "uncertain" && j.State != "reply-prepared" && j.State != "applying" {
		return domain.Fail(domain.Prerequisite, "journal is not awaiting explicit recovery")
	}
	if (j.Kind == domain.ExecutionRun || j.Kind == domain.VerificationRun) && j.RunID != "" {
		j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".recovery-report"}, RunID: j.RunID, Token: j.Token, SpecGeneration: j.SpecGeneration, ResultGeneration: verificationResult(j),
			Outcome: "interrupted", Summary: "operator explicitly acknowledged the interrupted process tree stopped; side effects remain uncertain", Checkpoint: j.ID}
		if err := e.queueReport(ctx, &j); err != nil {
			return err
		}
		return e.flushReports(ctx)
	}
	j.State, j.Problem = "abandoned", "operator acknowledged stopped processes and explicitly authorized a fresh text review"
	return e.State.SaveJournal(ctx, j)
}
