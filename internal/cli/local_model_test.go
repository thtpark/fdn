package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func TestLocalModelCLIFounderRemoteDispatchAndStatus(t *testing.T) {
	ctx := context.Background()
	_, cfgFile, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/agent_target/local-models":
			var req domain.LocalModelInitRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "bad body"))
				return
			}
			spec, err := domain.ValidateLocalModelInitRequest(req)
			if err != nil {
				diagnosticsEnvelope(t, w, nil, domain.AsFault(err))
				return
			}
			diagnosticsEnvelope(t, w, domain.AgentOperation{
				Version:             domain.AgentOperationVersion,
				ID:                  "agent_operation_localcli",
				Revision:            1,
				Kind:                "local-model-init",
				RequesterID:         "agent_founder",
				TargetID:            "agent_target",
				RequesterGeneration: 1,
				TargetGeneration:    1,
				State:               "queued",
				Reason:              req.Reason,
				LocalModel:          &spec,
				CreatedAt:           1000,
				ExpiresAt:           1000 + domain.LocalModelInitTTL,
				ServerObservedAt:    1000,
			}, nil)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agent-operations/agent_operation_localcli":
			spec, _ := domain.ParseLocalModelSpec("qwen3.8:27b", true, "q8_0", true)
			diagnosticsEnvelope(t, w, domain.AgentOperation{
				Version:             domain.AgentOperationVersion,
				ID:                  "agent_operation_localcli",
				Revision:            3,
				Kind:                "local-model-init",
				RequesterID:         "agent_founder",
				TargetID:            "agent_target",
				RequesterGeneration: 1,
				TargetGeneration:    1,
				State:               "initialized",
				Reason:              "Initialize Qwen 3.8 27B",
				LocalModel:          &spec,
				CreatedAt:           1000,
				ExpiresAt:           1000 + domain.LocalModelInitTTL,
				StartedAt:           1005,
				FinishedAt:          1060,
				ServerObservedAt:    1065,
				Result: &domain.AgentOperationResult{
					Code:                "local-model-initialized",
					CapturedAt:          1060,
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
			}, nil)
		default:
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.NotFound, "unexpected route "+r.URL.Path))
		}
	})

	var out, diag bytes.Buffer
	code := RunAgent(ctx, []string{
		"--config", cfgFile,
		"--operation-id", "init-agent3-qwen",
		"local-model", "init", "agent_target",
		"--model", "qwen3.8:27b",
		"--quantized",
		"--quantization", "q8_0",
		"--set-preference",
		"--reason", "Initialize Qwen 3.8 27B on Agent3",
	}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatal("CLI local-model init failed", code, out.String(), diag.String())
	}
	var env api.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.OK {
		t.Fatal("invalid envelope", out.String(), err)
	}
	var op domain.AgentOperation
	if err := json.Unmarshal(env.Data, &op); err != nil || op.ID != "agent_operation_localcli" || op.State != "queued" || op.LocalModel == nil || op.LocalModel.Quantization != "q8_0" || !op.LocalModel.SetPreference {
		t.Fatal("unexpected queued operation", op, err)
	}
	recorded := requests()
	if len(recorded) == 0 || recorded[len(recorded)-1].path != "/v1/agents/agent_target/local-models" {
		t.Fatal("unexpected request route", recorded)
	}

	out.Reset()
	diag.Reset()
	code = RunAgent(ctx, []string{"--config", cfgFile, "local-model", "status", "agent_operation_localcli"}, strings.NewReader(""), &out, &diag)
	if code != 0 {
		t.Fatal("CLI local-model status failed", code, out.String(), diag.String())
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.OK {
		t.Fatal("invalid status envelope", out.String(), err)
	}
	if err := json.Unmarshal(env.Data, &op); err != nil || op.State != "initialized" || op.Result == nil || op.Result.LocalModel.Model != "qwen3.8:27b-q8_0" {
		t.Fatal("unexpected status operation", op, err)
	}
}
