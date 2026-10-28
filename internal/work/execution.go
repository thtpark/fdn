package work

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

type ReportReceipt struct {
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id"`
	Accepted bool   `json:"accepted"`
	Stale    bool   `json:"stale"`
	State    string `json:"state"`
}

func (e *Engine) execute(ctx context.Context, c config.Worker, task domain.Task, launch Launch, manual bool) error {
	launch.Phase = domain.ExecutionRun
	return e.coding(ctx, c, task, launch, manual)
}

func (e *Engine) coding(ctx context.Context, c config.Worker, task domain.Task, launch Launch, manual bool) error {
	ctx, launch = actionContext(ctx, launch)
	if err := e.phaseHealth(ctx, "preparing", &launch, "", 120, false); err != nil {
		return err
	}
	verification := launch.Phase == domain.VerificationRun
	var binding local.Binding
	var err error
	if verification {
		binding, err = e.projectWorkspace(ctx, c, task.ProjectID)
	} else {
		binding, err = e.State.Workspace(ctx, task.WorkspaceRef)
	}
	if err != nil {
		return e.permissionBlocked(ctx, c, launch, "workspace", err)
	}
	if err := e.resolvePermission(ctx, launch.ActionKey, "workspace"); err != nil {
		return err
	}
	health, model, err := e.selected(ctx, c, launch)
	if err != nil {
		return err
	}
	if err := e.refreshNotes(ctx, &launch); err != nil {
		return err
	}
	j, err := e.newJournal(launch, health, model)
	if err != nil {
		return err
	}
	prompt, err := invocationPrompt(j.Context)
	if err != nil {
		return err
	}
	j.State, j.WorkspaceRef, j.Token = "claim-prepared", binding.Ref, domain.NewToken()
	j.Claim = &domain.ClaimRequest{Command: domain.Command{OperationID: j.ID + ".claim", ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(j.Token), Provider: health.Provider, Model: model}
	if verification {
		j.Claim.Kind, j.Claim.ResultGeneration, j.Claim.ProjectEpoch = domain.VerificationRun, task.ResultGeneration, launch.Project.Epoch
	}
	if err := e.State.SaveJournal(ctx, j); err != nil {
		return err
	}
	run, err := api.Call[domain.Run](ctx, e.Client, "POST", api.Path("tasks", task.ID, "claim"), j.Claim)
	if err != nil {
		if domain.AsFault(err).Code != domain.Unavailable {
			j.State = "stale"
			j.Problem = "claim was not accepted"
			err = errors.Join(err, e.State.SaveJournal(context.WithoutCancel(ctx), j))
		}
		return err
	}
	j.RunID, j.State = run.ID, "claimed"
	if err := e.State.SaveJournal(ctx, j); err != nil {
		return err
	}
	if _, err := e.State.Workspace(ctx, binding.Ref); err != nil {
		return e.notStarted(ctx, &j, "workspace authorization changed before provider launch")
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if verification {
		err = e.guardVerification(runCtx, cancel, launch, run)
	} else {
		err = e.guardTask(runCtx, cancel, task, run.ID, manual)
	}
	if err != nil {
		return errors.Join(err, e.notStarted(ctx, &j, "current authority could not be validated before provider launch"))
	}
	if err := e.phaseHealth(ctx, "invoking", &launch, j.ID, min(binding.TimeoutSeconds, c.InvocationTimeoutSeconds), true); err != nil {
		return err
	}
	j.State = "invoking"
	if err := e.State.SaveJournal(ctx, j); err != nil {
		return err
	}
	go e.guardMode(runCtx, cancel, manual, binding.Ref)
	reply, invokeErr := e.Executor.Invoke(runCtx, provider.Invocation{ID: j.ID, Provider: j.Provider, Path: health.Path, Model: model,
		Prompt: prompt, Workspace: binding.Path, Coding: true,
		Timeout: time.Duration(min(binding.TimeoutSeconds, c.InvocationTimeoutSeconds)) * time.Second,
		Started: func(pid int) error { j.PID = pid; return e.State.SaveJournal(runCtx, j) }})
	if invokeErr == nil {
		invokeErr = reply.Validate()
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	if checkCleanup(invokeErr) {
		j.State, j.Problem = "uncertain", "provider process-tree cleanup is unproven; confirm it stopped before recovery"
		saveErr := e.State.SaveJournal(saveCtx, j)
		return errors.Join(invokeErr, saveErr, e.invocationBlocked(saveCtx, c, j, launch, invokeErr))
	}
	if invokeErr != nil {
		outcome := "failed"
		summary := "provider failed; private evidence retained; review possible side effects before retry"
		if runCtx.Err() != nil {
			outcome = "stopped"
			summary = "provider stopped by cancellation or a current mode/authority/workspace guard; partial private evidence retained"
		}
		j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".report"}, RunID: j.RunID, Token: j.Token,
			SpecGeneration: j.SpecGeneration, ResultGeneration: verificationResult(j), Outcome: outcome, Summary: summary, Checkpoint: j.ID, SafeToRetry: !verification && j.PID == 0}
		helpErr := e.invocationBlocked(saveCtx, c, j, launch, invokeErr)
		if err := e.queueReport(saveCtx, &j); err != nil {
			return errors.Join(invokeErr, err, helpErr)
		}
		if err := e.flushReports(saveCtx); err != nil {
			return errors.Join(err, helpErr)
		}
		return errors.Join(domain.Fail(domain.Provider, "provider failed or stopped; a fenced failure report was recorded, not completion"), helpErr)
	}
	validReply := reply.Decision == "" && reply.RequestDecision == "" && !reply.MessageRead && validExecutionOutcome(reply.Outcome)
	if verification {
		decision := reply.Outcome == "reviewed" && (reply.Decision == "accept" || reply.Decision == "rework") && len(reply.Evidence) > 0
		stopped := (reply.Outcome == "paused" || reply.Outcome == "failed") && reply.Decision == ""
		validReply = (decision || stopped) && len(reply.Questions) == 0 && len(reply.AnswerIDs) == 0 && !reply.SafeToRetry && !reply.MessageRead && reply.RequestDecision == ""
	}
	if reply.Description != nil || reply.AcceptanceCriteria != nil || !validReply {
		j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".report"}, RunID: j.RunID, Token: j.Token,
			SpecGeneration: j.SpecGeneration, ResultGeneration: verificationResult(j), Outcome: "failed", Summary: "provider returned an invalid coding contract; private evidence retained", Checkpoint: j.ID}
		if err := e.queueReport(saveCtx, &j); err != nil {
			return err
		}
		return errors.Join(domain.Fail(domain.Provider, "invalid coding result was recorded as failure, not acceptance"), e.flushReports(saveCtx))
	}
	j.Reply, err = json.Marshal(reply)
	if err != nil {
		return err
	}
	j.State = "reply-prepared"
	if err := e.State.SaveJournal(saveCtx, j); err != nil {
		return err
	}
	if err := e.resolvePermission(saveCtx, launch.ActionKey, "provider-permission"); err != nil {
		return err
	}
	return e.finishExecution(saveCtx, &j, launch, reply)
}

func validExecutionOutcome(outcome string) bool {
	return outcome == "completed" || outcome == "paused" || outcome == "failed"
}

func (e *Engine) guardTask(ctx context.Context, cancel context.CancelCauseFunc, approved domain.Task, runID string, manual bool) error {
	return e.guardAuthority(ctx, cancel, &authoritySubscription{taskID: approved.ID, projectID: approved.ProjectID}, func(ctx context.Context) error {
		task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", approved.ID), nil)
		if err != nil {
			return err
		}
		if task.ActiveRun == nil || task.ActiveRun.ID != runID || task.ActiveRun.StopRequested || task.SpecGeneration != approved.SpecGeneration || task.AssigneeID != approved.AssigneeID || task.Readiness == nil {
			return domain.Fail(domain.Conflict, "current task ownership or approval changed")
		}
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		if project.Archived || project.Epoch != task.Readiness.ProjectEpoch {
			return domain.Fail(domain.Conflict, "current project authority changed")
		}
		return e.validateRunMode(ctx, manual)
	})
}

func (e *Engine) finishExecution(ctx context.Context, j *local.Journal, launch Launch, reply provider.Reply) error {
	if err := e.phaseHealth(ctx, "applying", &launch, j.ID, 30, true); err != nil {
		return err
	}
	if launch.Task == nil {
		return domain.Fail(domain.Internal, "coding invocation omitted its task context")
	}
	if j.Kind == domain.VerificationRun {
		outcome := reply.Outcome
		if outcome == "reviewed" {
			outcome = "completed"
		}
		j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".report"}, RunID: j.RunID, Token: j.Token,
			SpecGeneration: j.SpecGeneration, ResultGeneration: verificationResult(*j), Outcome: outcome, Decision: reply.Decision,
			ApproveRework: reply.Decision == "rework", Summary: reply.Summary, Evidence: reply.Evidence, Checkpoint: j.ID}
		if err := e.queueReport(ctx, j); err != nil {
			return err
		}
		return e.flushReports(ctx)
	}
	waiting := (*domain.Waiting)(nil)
	outcome := reply.Outcome
	if len(reply.Questions) != 0 {
		var err error
		waiting, err = e.publishQuestions(ctx, j, *launch.Task, reply.Questions)
		if err != nil {
			return err
		}
		outcome = "paused"
	}
	if outcome == "paused" && waiting == nil && reply.Checkpoint == "" {
		return domain.Fail(domain.Provider, "intentional pause needs a checkpoint or published blocking questions")
	}
	if err := e.State.Put(ctx, "checkpoint:"+j.TaskID, reply); err != nil {
		return err
	}
	j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".report"}, RunID: j.RunID, Token: j.Token,
		SpecGeneration: j.SpecGeneration, Outcome: outcome, Summary: reply.Summary, Evidence: reply.Evidence, Checkpoint: j.ID, Waiting: waiting}
	if err := e.queueReport(ctx, j); err != nil {
		return err
	}
	return e.flushReports(ctx)
}

func verificationResult(j local.Journal) int64 {
	if j.Kind == domain.VerificationRun && j.Claim != nil {
		return j.Claim.ResultGeneration
	}
	return 0
}

func (e *Engine) notStarted(ctx context.Context, j *local.Journal, reason string) error {
	j.Report = &domain.ReportRequest{Command: domain.Command{OperationID: j.ID + ".report"}, RunID: j.RunID, Token: j.Token,
		SpecGeneration: j.SpecGeneration, ResultGeneration: verificationResult(*j), Outcome: "failed", Summary: reason + "; provider process was never started", SafeToRetry: j.Kind != domain.VerificationRun}
	if err := e.queueReport(ctx, j); err != nil {
		return err
	}
	return e.flushReports(ctx)
}

func (e *Engine) queueReport(ctx context.Context, j *local.Journal) error {
	if j.Report == nil {
		return domain.Fail(domain.Internal, "report proposal is missing")
	}
	if err := e.journalHealth(ctx, *j, "reporting"); err != nil {
		return err
	}
	j.State = "report-prepared"
	if err := e.State.SaveJournal(ctx, *j); err != nil {
		return err
	}
	data, err := json.Marshal(j.Report)
	if err != nil {
		return err
	}
	return e.State.Enqueue(ctx, local.Outbox{OperationID: j.Report.OperationID, Method: http.MethodPost, Path: api.Path("tasks", j.TaskID, "report"), Body: data})
}
