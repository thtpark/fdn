package foundation

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func configureImprovementFixture(t *testing.T, f *fixture, enabled bool) domain.ImprovementPolicy {
	t.Helper()
	policy, err := f.s.ImprovementPolicy(context.Background(), f.founder.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ImprovementPolicyRequest{Command: op(), Enabled: enabled, WindowSeconds: 3600,
		SliceSeconds: 10, PerAgentSeconds: 60, FleetSeconds: 120, Reason: "Authorize bounded internal maintenance without installing software"}
	req.ExpectedRevision = policy.Revision
	policy, err = f.s.ConfigureImprovements(context.Background(), f.founder.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestImprovementFoundationProjectAndFeatureIntake(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ordinary, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Foundation", Description: "An existing ordinary project"})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ImprovementPolicyRequest{Command: op(), WindowSeconds: 3600, SliceSeconds: 10, PerAgentSeconds: 60, FleetSeconds: 120, Reason: "Initialize"}
	_, err = f.s.ConfigureImprovements(ctx, f.worker.ID, req)
	assertCode(t, err, domain.Denied)
	policy := configureImprovementFixture(t, f, true)
	project, err := f.s.Project(ctx, f.worker.ID, domain.FoundationProjectID)
	if err != nil || project.ID == ordinary.ID || project.OwnerID != f.founder.ID || len(project.Delegates) != 0 {
		t.Fatal("built-in identity adopted a name or granted delegate authority", project, err)
	}
	if policy.Revision != 1 {
		t.Fatal("initialization did not use a separate explicit revision", policy)
	}
	primary := f.ready(t)
	request := domain.ImprovementRequest{Command: op(), ProjectID: project.ID, Kind: "feature_request",
		Title: "Add a reusable internal tool", Description: "Provide a checked native tool for an ordinary task.",
		AcceptanceCriteria: "An independent consumer verifies the requested capability.", RelatedTaskID: primary.ID}
	improvement, err := f.s.CreateImprovement(ctx, f.worker.ID, request)
	if err != nil || improvement.IncidentID != "" || improvement.State != "proposed" {
		t.Fatal("feature intake required an incident or admitted execution", improvement, err)
	}
	replayed, err := f.s.CreateImprovement(ctx, f.worker.ID, request)
	if err != nil || replayed.ID != improvement.ID {
		t.Fatal("request replay duplicated intake", err)
	}
	decision := domain.ImprovementDecision{Command: op(), Decision: "accept", AssigneeID: f.worker.ID, Reason: "Checked scope"}
	decision.ExpectedRevision = improvement.Revision
	_, err = f.s.DecideImprovement(ctx, f.worker.ID, improvement.ID, decision)
	assertCode(t, err, domain.Denied)
	grant := domain.GrantRequest{Command: op(), AgentID: f.other.ID}
	grant.ExpectedRevision = project.Revision
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, grant)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.DecideImprovement(ctx, f.other.ID, improvement.ID, decision)
	if err != nil || result.Task == nil || result.Task.Readiness != nil || result.Task.ProjectID != project.ID ||
		!result.Task.Policy.IndependentReadiness || !result.Task.Policy.IndependentVerification || result.Task.ParentID != "" {
		t.Fatal("delegated intake created authority or cross-project parenting", result, err)
	}
	resultAgain, err := f.s.DecideImprovement(ctx, f.other.ID, improvement.ID, decision)
	if err != nil || resultAgain.Task == nil || resultAgain.Task.ID != result.Task.ID {
		t.Fatal("decision replay created a second task", err)
	}
	current, err := f.s.Task(ctx, f.worker.ID, primary.ID)
	if err != nil || current.ProjectID != primary.ProjectID || current.Status != primary.Status || current.SpecGeneration != primary.SpecGeneration {
		t.Fatal("platform request changed the original project/task", current, err)
	}
	task := f.change(t, f.worker.ID, result.Task.ID, "workspace", func(q *domain.TaskChange) { q.WorkspaceRef = "workspace_platform" })
	self := domain.TaskChange{Command: op(), Kind: "approve", SpecGeneration: task.SpecGeneration, Reason: "Self cannot approve"}
	self.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.worker.ID, task.ID, self)
	assertCode(t, err, domain.Denied)
	weaken := domain.TaskChange{Command: op(), Kind: "policy", Policy: &domain.ApprovalPolicy{}, Reason: "Remove independent checks"}
	weaken.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.other.ID, task.ID, weaken)
	assertCode(t, err, domain.Denied)
}

func TestImprovementMaintenanceBudgetReplayExpiryAndNoReset(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	configureImprovementFixture(t, f, true)
	request, err := f.s.CreateImprovement(ctx, f.worker.ID, domain.ImprovementRequest{Command: op(), ProjectID: domain.FoundationProjectID,
		Kind: "enhancement", Title: "Improve a feature", Description: "A scoped improvement.", AcceptanceCriteria: "Verify the new behavior."})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.s.DecideImprovement(ctx, f.founder.ID, request.ID, domain.ImprovementDecision{
		Command:  domain.Command{OperationID: op().OperationID, ExpectedRevision: request.Revision},
		Decision: "accept", AssigneeID: f.worker.ID, Reason: "Prepare normally"})
	if err != nil {
		t.Fatal(err)
	}
	reserve := domain.MaintenanceRequest{Command: op(), ProjectID: domain.FoundationProjectID,
		SourceID: accepted.Task.ID, Kind: "preparation", Seconds: 10}
	first, err := f.s.ReserveMaintenance(ctx, f.worker.ID, reserve)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := f.s.MaintenanceUsage(ctx, f.worker.ID)
	if err != nil || usage.AgentSeconds != 10 || usage.FleetSeconds != 10 {
		t.Fatal("native admission was not charged exactly", usage, err)
	}
	f.now = f.now.Add(time.Second)
	replay, err := f.s.ReserveMaintenance(ctx, f.worker.ID, reserve)
	if err != nil || replay.ID != first.ID || replay.ExpiresAt != first.ExpiresAt {
		t.Fatal("lost acknowledgement renewed/recharged admission", replay, err)
	}
	_, err = f.s.SettleMaintenance(ctx, f.other.ID, first.ID, domain.MaintenanceSettlement{Command: op()})
	assertCode(t, err, domain.Denied)
	if _, err := f.s.SettleMaintenance(ctx, f.worker.ID, first.ID, domain.MaintenanceSettlement{Command: op()}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		reserve.Command = op()
		current, err := f.s.ReserveMaintenance(ctx, f.worker.ID, reserve)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.SettleMaintenance(ctx, f.worker.ID, current.ID, domain.MaintenanceSettlement{Command: op()}); err != nil {
			t.Fatal(err)
		}
	}
	usage, err = f.s.MaintenanceUsage(ctx, f.worker.ID)
	if err != nil || usage.AgentSeconds != 60 {
		t.Fatal("settlement refunded or reset the reserved bound", usage, err)
	}
	reserve.Command = op()
	_, err = f.s.ReserveMaintenance(ctx, f.worker.ID, reserve)
	assertCode(t, err, domain.Prerequisite)
	configureImprovementFixture(t, f, true)
	_, err = f.s.ReserveMaintenance(ctx, f.worker.ID, reserve)
	assertCode(t, err, domain.Prerequisite)
	f.now = f.now.Add(time.Hour)
	reserve.Command = op()
	if _, err := f.s.ReserveMaintenance(ctx, f.worker.ID, reserve); err != nil {
		t.Fatal("a new real server window did not permit bounded admission", err)
	}
}
