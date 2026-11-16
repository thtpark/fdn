package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestImprovementNativeAuthorityShapesAndExactReplay(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	worker := messageFixtureClient(t, f.server, f.target.ID)
	policy := domain.ImprovementPolicyRequest{Command: domain.Command{OperationID: "native-improvement-policy"},
		Enabled: true, WindowSeconds: 3600, SliceSeconds: 10, PerAgentSeconds: 60, FleetSeconds: 120, Reason: "Configure bounded improvement"}
	if _, err := Call[domain.ImprovementPolicy](ctx, worker, "POST", "/v1/improvement-policy", policy); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary identity configured global maintenance policy", err)
	}
	configured, err := Call[domain.ImprovementPolicy](ctx, founder, "POST", "/v1/improvement-policy", policy)
	if err != nil || configured.Revision != 1 {
		t.Fatal("typed native policy did not preserve explicit bounds", configured, err)
	}
	project, err := Call[domain.Project](ctx, founder, "GET", Path("projects", domain.FoundationProjectID), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.ImprovementRequest{Command: domain.Command{OperationID: "native-feature-request"}, ProjectID: project.ID,
		Kind: "feature_request", Title: "Create a new internal tool", Description: "Implement an explicitly requested reusable tool.",
		AcceptanceCriteria: "Actual consumer behavior and failure shapes are checked independently."}
	feature, err := Call[domain.Improvement](ctx, worker, "POST", "/v1/improvements", request)
	if err != nil || feature.IncidentID != "" || feature.TaskID != "" {
		t.Fatal("feature request required an incident or approved itself", feature, err)
	}
	replayed, err := Call[domain.Improvement](ctx, worker, "POST", "/v1/improvements", request)
	if err != nil || replayed.ID != feature.ID {
		t.Fatal("native exact replay created new intake", err)
	}
	decision := domain.ImprovementDecision{Command: domain.Command{OperationID: "native-feature-accept", ExpectedRevision: feature.Revision},
		Decision: "accept", AssigneeID: f.target.ID, Reason: "Exact actionable draft for normal independent review"}
	if _, err := Call[domain.ImprovementResult](ctx, worker, "POST", Path("improvements", feature.ID, "decision"), decision); err == nil {
		t.Fatal("ordinary requester granted task authority")
	}
	result, err := Call[domain.ImprovementResult](ctx, founder, "POST", Path("improvements", feature.ID, "decision"), decision)
	if err != nil || result.Task == nil || result.Task.Readiness != nil || !result.Task.Policy.IndependentReadiness || !result.Task.Policy.IndependentVerification {
		t.Fatal("native task conversion returned the wrong authority/output shape", result, err)
	}
	for _, path := range []string{"/v1/improvement-policy?force=true", "/v1/improvements?owner=other", "/v1/maintenance-usage?actor=other"} {
		if err := worker.Request(ctx, http.MethodGet, path, nil, nil); err == nil {
			t.Fatal("native query admitted caller authority fields", path)
		}
	}
	for _, field := range []string{"owner_id", "origin", "requester_id", "task_id", "incident_id", "state", "revision"} {
		input := map[string]any{"operation_id": domain.NewID("op"), "project_id": project.ID, "kind": "feature_request",
			"title": "Invalid protected input", "description": "No forged provenance", "acceptance_criteria": "Must reject", field: "forged"}
		envelope, status := f.request(t, f.target, http.MethodPost, "/v1/improvements", input)
		if status != http.StatusBadRequest || envelope.OK {
			t.Fatal("raw protected improvement input was accepted", field, status, envelope)
		}
	}
}
