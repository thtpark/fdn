package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFounderRuntimeResetAuthoritySelfTargetAndExactReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, actor := range []domain.Agent{f.worker, f.other} {
		for _, target := range []domain.Agent{actor, f.worker, f.founder} {
			_, err := f.s.RequestAgentReset(ctx, actor.ID, target.ID, domain.AgentResetRequest{Command: op(), Reason: "Unauthorized reset"})
			assertCode(t, err, domain.Denied)
		}
	}
	for _, target := range []domain.Agent{f.worker, f.founder} {
		before, err := f.s.Agent(ctx, f.founder.ID, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		req := domain.AgentResetRequest{Command: op(), Reason: "Founder requests an identity-preserving runtime reset"}
		queued, err := f.s.RequestAgentReset(ctx, f.founder.ID, target.ID, req)
		if err != nil || queued.Kind != "runtime-reset" || queued.State != "queued" || queued.Result != nil || queued.TargetGeneration != 1 || queued.RequesterGeneration != 1 || queued.ExpiresAt-queued.CreatedAt != 900 {
			t.Fatal("reset was not an exact generation-bound protected request", queued, err)
		}
		_, err = f.s.Message(ctx, target.ID, queued.ID)
		assertCode(t, err, domain.Denied)
		checking, _ := checkingAgentOperation(t, f, queued)
		report := domain.AgentOperationReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: checking.Revision}, State: "reset",
			Result: &domain.AgentOperationResult{Code: "runtime-reset", ModeAtCheck: before.Mode, CapturedAt: f.now.Unix(),
				AutomationMayResume: before.Mode == "ai", Observation: &domain.AgentInspection{ObservedAt: f.now.Unix(), WorkSlot: "free"}}}
		done, err := f.s.ReportAgentOperation(ctx, target.ID, checking.ID, report)
		if err != nil || done.State != "reset" || done.Result.ModeAtCheck != before.Mode {
			t.Fatal("target could not report its actual runtime reset", done, err)
		}
		after, err := f.s.Agent(ctx, f.founder.ID, target.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("runtime reset changed the agent's identity, role or mode", before, after, err)
		}
		replayed, err := f.s.RequestAgentReset(ctx, f.founder.ID, target.ID, req)
		if err != nil || !reflect.DeepEqual(done, replayed) {
			t.Fatal("reset request replay created another cancellation", done, replayed, err)
		}
		req.Reason = "Changed reset must not reuse authority"
		_, err = f.s.RequestAgentReset(ctx, f.founder.ID, target.ID, req)
		assertCode(t, err, domain.Conflict)
	}
}

func TestRuntimeResetCannotMasqueradeAsAnotherControl(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	queued, err := f.s.RequestAgentReset(ctx, f.founder.ID, f.worker.ID, domain.AgentResetRequest{Command: op(), Reason: "Explicit runtime reset"})
	if err != nil {
		t.Fatal(err)
	}
	checking, _ := checkingAgentOperation(t, f, queued)
	report := diagnosticOperationReport(f, checking)
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, checking.ID, report)
	assertCode(t, err, domain.Invalid)
	for _, reason := range []string{"", " \n"} {
		_, err := f.s.RequestAgentReset(ctx, f.founder.ID, f.worker.ID, domain.AgentResetRequest{Command: op(), Reason: reason})
		assertCode(t, err, domain.Invalid)
	}
}
