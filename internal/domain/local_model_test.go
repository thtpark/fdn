package domain

import "testing"

func TestLocalModelSpecAndMemorySizing(t *testing.T) {
	spec, err := ParseLocalModelSpec("qwen3.8", false, "", false)
	if err != nil || spec.Family != Qwen38Family || spec.Quantized || spec.Quantization != "bf16" || spec.Model != "qwen3.8:27b-bf16" {
		t.Fatal("unexpected unquantized Qwen 3.8 spec", spec, err)
	}
	if _, _, _, err := SelectLocalModelForMemory(spec, 60*(1<<30)); AsFault(err).Code != Prerequisite {
		t.Fatal("unquantized 27B bf16 fit in 60 GiB without KV headroom", err)
	}
	if model, quant, req, err := SelectLocalModelForMemory(spec, 80*(1<<30)); err != nil || model != "qwen3.8:27b-bf16" || quant != "bf16" || req != 64*(1<<30) {
		t.Fatal("unquantized 27B bf16 rejected on 80 GiB", model, quant, req, err)
	}

	autoSpec, err := ParseLocalModelSpec("qwen 3.8 27b", true, "", true)
	if err != nil || !autoSpec.Quantized || autoSpec.Quantization != "auto" || autoSpec.Model != "" || !autoSpec.SetPreference {
		t.Fatal("unexpected auto-quantized Qwen 3.8 spec", autoSpec, err)
	}
	if _, _, _, err := SelectLocalModelForMemory(autoSpec, 16*(1<<30)); AsFault(err).Code != Prerequisite {
		t.Fatal("quantized 27B fit in 16 GiB", err)
	}
	if model, quant, _, err := SelectLocalModelForMemory(autoSpec, 24*(1<<30)); err != nil || model != "qwen3.8:27b-q4_K_M" || quant != "q4_K_M" {
		t.Fatal("24 GiB did not select q4_K_M", model, quant, err)
	}
	if model, quant, _, err := SelectLocalModelForMemory(autoSpec, 60*(1<<30)); err != nil || model != "qwen3.8:27b-q8_0" || quant != "q8_0" {
		t.Fatal("60 GiB did not select q8_0", model, quant, err)
	}

	q4Spec, err := ParseLocalModelSpec("qwen3.8:27b", true, "q4_k_m", false)
	if err != nil || q4Spec.Model != "qwen3.8:27b-q4_K_M" || q4Spec.Quantization != "q4_K_M" {
		t.Fatal("explicit q4_K_M failed", q4Spec, err)
	}
	if model, quant, _, err := SelectLocalModelForMemory(q4Spec, 60*(1<<30)); err != nil || model != "qwen3.8:27b-q4_K_M" || quant != "q4_K_M" {
		t.Fatal("explicit q4_K_M overridden on 60 GiB", model, quant, err)
	}

	for _, bad := range []struct {
		model string
		quant bool
		level string
	}{
		{"llama4:70b", false, ""},
		{"qwen3.8:27b", true, "bf16"},
		{"qwen3.8:27b-bf16", true, ""},
		{"qwen3.8:27b-q4_K_M", false, "q8_0"},
		{"qwen3.8:27b", true, "int2"},
	} {
		if _, err := ParseLocalModelSpec(bad.model, bad.quant, bad.level, false); AsFault(err).Code != Invalid {
			t.Fatal("invalid local model spec accepted", bad, err)
		}
	}
}

func TestLocalModelAgentOperationValidation(t *testing.T) {
	spec, err := ParseLocalModelSpec("qwen3.8:27b", true, "", true)
	if err != nil {
		t.Fatal(err)
	}
	op := AgentOperation{
		Version: AgentOperationVersion, ID: "agent_operation_local01", Revision: 2,
		Kind: "local-model-init", RequesterID: "agent_founder", TargetID: "agent_target",
		RequesterGeneration: 1, TargetGeneration: 1, State: "checking", Reason: "Initialize quantized Qwen 3.8 27B",
		LocalModel: &spec, CreatedAt: 1000, ExpiresAt: 1000 + LocalModelInitTTL, StartedAt: 1010, ServerObservedAt: 1015,
	}
	if err := ValidateAgentOperation(op); err != nil {
		t.Fatal("valid pending local-model-init operation rejected", err)
	}
	badTTL := op
	badTTL.ExpiresAt = 1000 + AgentOperationTTL
	if err := ValidateAgentOperation(badTTL); err == nil {
		t.Fatal("wrong local-model-init TTL accepted")
	}

	report := AgentOperationReport{
		Command: Command{OperationID: "op_res", ExpectedRevision: 2},
		State:   "initialized",
		Result: &AgentOperationResult{
			Code: "local-model-initialized", CapturedAt: 1100, ModeAtCheck: "ai", AutomationMayResume: true,
			LocalModel: &LocalModelObservation{
				Provider: "local", RuntimeVersion: "0.40.16", Family: Qwen38Family,
				Model: "qwen3.8:27b-q8_0", Quantization: "q8_0", MemorySource: "nvidia-vram",
				MemoryBytes: 60 * (1 << 30), RequiredBytes: 36 * (1 << 30), GPUCount: 4,
				CatalogPublished: true, PreferenceApplied: true,
			},
		},
	}
	if err := ValidateAgentOperationOutcome(op, report); err != nil {
		t.Fatal("valid initialized outcome rejected", err)
	}
	missingPref := *report.Result.LocalModel
	missingPref.PreferenceApplied = false
	badReport := report
	badReport.Result = &AgentOperationResult{Code: "local-model-initialized", CapturedAt: 1100, ModeAtCheck: "ai", LocalModel: &missingPref}
	if err := ValidateAgentOperationOutcome(op, badReport); err == nil {
		t.Fatal("set_preference=true accepted without preference_applied")
	}

	diagOp := op
	diagOp.Kind = "diagnostics"
	diagOp.LocalModel = nil
	diagOp.ExpiresAt = 1000 + AgentOperationTTL
	if err := ValidateAgentOperationOutcome(diagOp, report); err == nil {
		t.Fatal("diagnostics accepted local-model-init outcome")
	}
}
