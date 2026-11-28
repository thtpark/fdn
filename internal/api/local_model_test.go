package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFounderLocalModelInitNativeAPIAuthorityAndSettlement(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()

	route := Path("agents", f.target.ID, "local-models")
	req := domain.LocalModelInitRequest{
		Command:       domain.Command{OperationID: "op-local-quant"},
		Model:         "qwen3.8:27b",
		Quantized:     true,
		SetPreference: true,
		Reason:        "Initialize quantized Qwen 3.8 27B to fit 60 GiB GPU VRAM",
	}

	for _, actor := range []domain.Agent{f.delegate, f.target} {
		envelope, status := f.request(t, actor, http.MethodPost, route, req)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("non-Founder requested remote local model initialization", actor.ID, status, envelope)
		}
	}

	founderClient := messageFixtureClient(t, f.server, f.founder.ID)
	targetClient := messageFixtureClient(t, f.server, f.target.ID)

	queued, err := Call[domain.AgentOperation](ctx, founderClient, http.MethodPost, route, req)
	if err != nil || queued.State != "queued" || queued.Kind != "local-model-init" || queued.ExpiresAt-queued.CreatedAt != domain.LocalModelInitTTL || queued.LocalModel == nil || !queued.LocalModel.Quantized || queued.LocalModel.Quantization != "auto" {
		t.Fatal("failed to queue quantized local-model-init", queued, err)
	}
	replayed, err := Call[domain.AgentOperation](ctx, founderClient, http.MethodPost, route, req)
	if err != nil || !sameAPIAgentOperation(queued, replayed) {
		t.Fatal("replay altered local-model-init operation", replayed, err)
	}

	pendingPage, err := Call[domain.Page[domain.AgentOperation]](ctx, targetClient, http.MethodGet, "/v1/agent-operations?limit=10", nil)
	if err != nil || len(pendingPage.Items) != 1 || pendingPage.Items[0].ID != queued.ID {
		t.Fatal("target pending operations omitted local-model-init", pendingPage, err)
	}

	checking, err := Call[domain.AgentOperation](ctx, targetClient, http.MethodPost, Path("agent-operations", queued.ID, "result"), domain.AgentOperationReport{
		Command: domain.Command{OperationID: "op-local-checking", ExpectedRevision: queued.Revision},
		State:   "checking",
	})
	if err != nil || checking.State != "checking" {
		t.Fatal("target checking transition failed", checking, err)
	}

	final, err := Call[domain.AgentOperation](ctx, targetClient, http.MethodPost, Path("agent-operations", queued.ID, "result"), domain.AgentOperationReport{
		Command: domain.Command{OperationID: "op-local-done", ExpectedRevision: checking.Revision},
		State:   "initialized",
		Result: &domain.AgentOperationResult{
			Code:                "local-model-initialized",
			CapturedAt:          checking.StartedAt,
			ModeAtCheck:         "ai",
			AutomationMayResume: true,
			LocalModel: &domain.LocalModelObservation{
				Provider:          "local",
				RuntimeVersion:    "0.40.16",
				Family:            domain.Qwen38Family,
				Model:             "qwen3.8:27b-q8_0",
				Quantization:      "q8_0",
				MemorySource:      "nvidia-vram",
				MemoryBytes:       60 * (1 << 30),
				RequiredBytes:     36 * (1 << 30),
				GPUCount:          4,
				CatalogPublished:  true,
				PreferenceApplied: true,
			},
		},
	})
	if err != nil || final.State != "initialized" || final.Result == nil || final.Result.LocalModel == nil || final.Result.LocalModel.Model != "qwen3.8:27b-q8_0" {
		data, _ := json.Marshal(final)
		t.Fatal("local-model-init settlement failed", string(data), err)
	}
}
