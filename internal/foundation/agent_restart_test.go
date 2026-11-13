package foundation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func restartRequest(t *testing.T, f *fixture) (domain.AgentOperation, domain.AgentRestartPermit, domain.AgentRestartRequest) {
	t.Helper()
	request := domain.AgentRestartRequest{Command: op(), Reason: "Restore fixed native services without erasing state"}
	operation, err := f.s.RequestAgentRestart(context.Background(), f.founder.ID, f.worker.ID, request,
		func(domain.AgentRestartPermit) (string, error) { return "fixture-signed-permit", nil })
	if err != nil {
		t.Fatal(err)
	}
	permits, err := f.s.NativeAgentRestarts(context.Background())
	if err != nil || len(permits) != 1 {
		t.Fatal(permits, err)
	}
	return operation, permits[0], request
}

func admitRestart(t *testing.T, f *fixture, operation domain.AgentOperation, permit domain.AgentRestartPermit) domain.AgentOperation {
	t.Helper()
	ctx := context.Background()
	checking, err := f.s.ReportNativeAgentRestart(ctx, domain.AgentRestartReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: operation.Revision}, ID: operation.ID, State: "checking"})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.AgentRestartLeaseRequest{Command: op(), ID: operation.ID, Nonce: domain.NewToken(), PermitSHA256: domain.Hash(permit.Signature)}
	lease, err := f.s.LeaseAgentRestart(ctx, request, func(domain.UpgradeLease) (string, error) { return "fixture-signed-lease", nil })
	if err != nil || lease.ExpiresAt-lease.IssuedAt != 30 {
		t.Fatal(lease, err)
	}
	replayed, err := f.s.LeaseAgentRestart(ctx, request, func(domain.UpgradeLease) (string, error) { t.Fatal("lease replay resigned authority"); return "", nil })
	if err != nil || replayed != lease {
		t.Fatal("lease replay changed exact authority", replayed, err)
	}
	request.OperationID = op().OperationID
	if _, err := f.s.LeaseAgentRestart(ctx, request, func(domain.UpgradeLease) (string, error) { return "new-lease", nil }); err == nil {
		t.Fatal("native restart renewed an already admitted lease")
	}
	return checking
}

func TestNativeRestartIsFounderOnlyAndCannotBeClaimedByTheTarget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, actor := range []string{f.worker.ID, f.other.ID} {
		_, err := f.s.RequestAgentRestart(ctx, actor, f.worker.ID, domain.AgentRestartRequest{Command: op(), Reason: "Unauthorized"},
			func(domain.AgentRestartPermit) (string, error) { t.Fatal("non-Founder reached signer"); return "", nil })
		assertCode(t, err, domain.Denied)
	}
	operation, permit, request := restartRequest(t, f)
	if operation.State != "queued" || operation.Result != nil || permit.TargetGeneration != operation.TargetGeneration {
		t.Fatal("queued native restart claimed execution", operation, permit)
	}
	paused, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || paused.Mode != "manual" || paused.Revision != permit.ModeRevision {
		t.Fatal(paused, err)
	}
	replayed, err := f.s.RequestAgentRestart(ctx, f.founder.ID, f.worker.ID, request,
		func(domain.AgentRestartPermit) (string, error) {
			t.Fatal("restart replay issued a new permit")
			return "", nil
		})
	if err != nil || replayed.ID != operation.ID {
		t.Fatal(replayed, err)
	}
	request.OperationID = op().OperationID
	if _, err := f.s.RequestAgentRestart(ctx, f.founder.ID, f.worker.ID, request, func(domain.AgentRestartPermit) (string, error) { return "new", nil }); err == nil {
		t.Fatal("a pending restart was replaced with a new request")
	}
	page, err := f.s.ListAgentOperations(ctx, f.worker.ID, "", 20)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("ordinary worker runtime received native restart authority", page, err)
	}
	_, err = f.s.ReportAgentOperation(ctx, f.worker.ID, operation.ID, domain.AgentOperationReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: 1}, State: "checking"})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.GetAgentOperation(ctx, f.other.ID, operation.ID); err == nil {
		t.Fatal("unrelated actor read a restart")
	}
}

func TestNativeRestartRestoresOnlyVerifiedUnchangedPriorMode(t *testing.T) {
	for _, change := range []string{"none", "newer-pause", "pending-work", "preexisting-manual"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			if change == "preexisting-manual" {
				_, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{
					OperationID: op().OperationID, ExpectedRevision: f.worker.Revision}, Mode: "manual", Reason: "Existing human intent"})
				if err != nil {
					t.Fatal(err)
				}
			}
			operation, permit, _ := restartRequest(t, f)
			checking := admitRestart(t, f, operation, permit)
			if change == "newer-pause" {
				_, err := f.s.SetMode(ctx, f.founder.ID, f.worker.ID, domain.ModeRequest{Command: domain.Command{
					OperationID: op().OperationID, ExpectedRevision: permit.ModeRevision}, Mode: "manual", Reason: "Newer Founder intent"})
				if err != nil {
					t.Fatal(err)
				}
			}
			runtime := &domain.NativeRuntimeObservation{Version: "0.3.41", Commit: strings.Repeat("a", 40),
				IdentityGeneration: permit.TargetGeneration, ServicesRunning: true}
			if change == "pending-work" {
				runtime.PendingInvocations = 1
			}
			report := domain.AgentRestartReport{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: checking.Revision},
				ID: operation.ID, State: "restarted", Result: &domain.AgentOperationResult{Code: "native-services-restarted", ModeAtCheck: "manual", NativeRuntime: runtime}}
			result, err := f.s.ReportNativeAgentRestart(ctx, report)
			if err != nil || result.State != "restarted" {
				t.Fatal(result, err)
			}
			agent, err := f.s.Me(ctx, f.worker.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := "manual"
			if change == "none" {
				expected = "ai"
			}
			if agent.Mode != expected {
				t.Fatal("restart overwrote prior/newer intent or ignored retained work", agent.Mode, change)
			}
			replayed, err := f.s.ReportNativeAgentRestart(ctx, report)
			if err != nil || replayed.Revision != result.Revision {
				t.Fatal("completed restart report replay changed effects", replayed, err)
			}
			current, err := f.s.Me(ctx, f.worker.ID)
			if err != nil || current.Revision != agent.Revision {
				t.Fatal("replay restored mode twice", current, err)
			}
		})
	}
}

func TestNativeRestartExpiredOrChangedAuthorityCannotStartServices(t *testing.T) {
	f := newFixture(t)
	operation, permit, _ := restartRequest(t, f)
	_, err := f.s.ReportNativeAgentRestart(context.Background(), domain.AgentRestartReport{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: operation.Revision}, ID: operation.ID, State: "checking"})
	if err != nil {
		t.Fatal(err)
	}

	f.now = f.now.Add(901 * time.Second)
	_, err = f.s.LeaseAgentRestart(context.Background(), domain.AgentRestartLeaseRequest{
		Command: op(), ID: operation.ID, Nonce: domain.NewToken(), PermitSHA256: domain.Hash(permit.Signature)},
		func(domain.UpgradeLease) (string, error) { t.Fatal("expired restart reached signer"); return "", nil })
	assertCode(t, err, domain.Denied)
	current, err := f.s.GetAgentOperation(context.Background(), f.founder.ID, operation.ID)
	if err != nil || current.State != "expired" {
		t.Fatal(current, err)
	}
}

func TestNativeRestartPreservesAnInterruptedClaimAndDoesNotResumeItsWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: task.Revision}, Provider: "codex",
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken())})
	if err != nil {
		t.Fatal(err)
	}
	operation, permit, _ := restartRequest(t, f)
	checking := admitRestart(t, f, operation, permit)
	result, err := f.s.ReportNativeAgentRestart(ctx, domain.AgentRestartReport{Command: domain.Command{
		OperationID: op().OperationID, ExpectedRevision: checking.Revision}, ID: operation.ID, State: "restarted",
		Result: &domain.AgentOperationResult{Code: "native-services-restarted", ModeAtCheck: "manual",
			NativeRuntime: &domain.NativeRuntimeObservation{Version: "0.3.41", Commit: strings.Repeat("a", 40),
				IdentityGeneration: permit.TargetGeneration, ServicesRunning: true}}})
	if err != nil || result.State != "restarted" || result.Result.AutomationMayResume {
		t.Fatal(result, err)
	}
	agent, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || agent.Mode != "manual" {
		t.Fatal("an interrupted claim resumed after native restart", agent, err)
	}
	current, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil || current.ActiveRun == nil || current.ActiveRun.ID != run.ID ||
		current.Attempts != run.Attempt || current.SpecGeneration != task.SpecGeneration {
		t.Fatal("restart erased ownership or reset the task budget", current, err)
	}
}
