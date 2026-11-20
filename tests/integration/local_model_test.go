//go:build integration

package integration_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

type integrationLocalModelExecutor struct {
	mu           sync.Mutex
	memoryBytes  int64
	models       []string
	localInvoked atomic.Int64
}

func (x *integrationLocalModelExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if name == "local" {
		if len(x.models) == 0 {
			return provider.Health{Provider: "local", Authentication: "unknown", Models: []string{}}, domain.Fail(domain.Prerequisite, "no local model initialized")
		}
		return provider.Health{
			Provider:       "local",
			Version:        "0.40.16",
			Path:           "/PRIVATE_LOCAL_OLLAMA_PATH",
			Authentication: "authenticated",
			Models:         append([]string{}, x.models...),
		}, nil
	}
	return provider.Health{Provider: name, Version: "1.0.0", Path: "/PRIVATE_CLOUD_PROVIDER", Authentication: "login-required", Models: []string{}}, nil
}

func (x *integrationLocalModelExecutor) Invoke(_ context.Context, inv provider.Invocation) (provider.Reply, error) {
	if inv.Provider != "local" || inv.Model != "qwen3.8:27b-q8_0" {
		return provider.Reply{}, domain.Fail(domain.Provider, "unexpected non-local invocation: "+inv.Provider+"/"+inv.Model)
	}
	x.localInvoked.Add(1)
	return provider.Reply{
		Outcome:     "reviewed",
		Summary:     "Completed capability onboarding with local quantized Qwen 3.8 27B",
		Evidence:    []string{"local model invoked without cloud login"},
		Questions:   []provider.Question{},
		AnswerIDs:   []string{},
		SafeToRetry: false,
	}, nil
}

func (x *integrationLocalModelExecutor) InitializeLocalModel(_ context.Context, spec domain.LocalModelSpec) (provider.LocalInitResult, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	mem := provider.LocalMemory{Source: "nvidia-vram", TotalBytes: x.memoryBytes, GPUCount: 4}
	modelTag, quant, required, err := domain.SelectLocalModelForMemory(spec, x.memoryBytes)
	res := provider.LocalInitResult{
		Provider:       "local",
		RuntimeVersion: "0.40.16",
		Family:         spec.Family,
		Model:          modelTag,
		Quantization:   quant,
		Memory:         mem,
		RequiredBytes:  required,
	}
	if err != nil {
		res.MemoryInsufficient = true
		return res, err
	}
	x.models = []string{modelTag}
	res.Models = []string{modelTag}
	return res, nil
}

func TestFounderLocalModelInitRejectsOversizedWeightsThenSwitchesAgentToQuantizedQwen38(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	exec := &integrationLocalModelExecutor{memoryBytes: 60 * (1 << 30)}
	engine := newEngine(t, h, &executor{})
	engine.Executor = exec
	startOperationWorker(t, engine)

	route := api.Path("agents", h.workerAgent.ID, "local-models")

	// 1. Unquantized bf16 does not fit in 60 GiB usable GPU VRAM.
	unquantOp, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", route, domain.LocalModelInitRequest{
		Command: command(),
		Model:   "qwen3.8:27b",
		Reason:  "Try unquantized Qwen 3.8 27B on 60 GiB VRAM",
	})
	if err != nil {
		t.Fatal(err)
	}
	unquantOp = waitAgentOperation(t, h.founder, unquantOp.ID, engine)
	if unquantOp.State != "rejected" || unquantOp.Result == nil || unquantOp.Result.Code != "memory-insufficient" {
		t.Fatal("expected memory-insufficient rejection", unquantOp)
	}

	// 2. Quantized request fits 60 GiB (selects q8_0), publishes model-options, sets preference, and wakes onboarding!
	quantOp, err := api.Call[domain.AgentOperation](ctx, h.founder, "POST", route, domain.LocalModelInitRequest{
		Command:       command(),
		Model:         "qwen3.8:27b",
		Quantized:     true,
		SetPreference: true,
		Reason:        "Initialize quantized Qwen 3.8 27B instead of codex or copilot",
	})
	if err != nil {
		t.Fatal(err)
	}
	quantOp = waitAgentOperation(t, h.founder, quantOp.ID, engine)
	if quantOp.State != "initialized" || quantOp.Result == nil || quantOp.Result.LocalModel == nil ||
		quantOp.Result.LocalModel.Model != "qwen3.8:27b-q8_0" || !quantOp.Result.LocalModel.CatalogPublished || !quantOp.Result.LocalModel.PreferenceApplied {
		t.Fatal("quantized local-model-init did not complete as expected", quantOp)
	}

	options, err := api.Call[domain.AgentModelOptions](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "model-options"), nil)
	if err != nil || len(options.Computers) != 1 || !options.Computers[0].Allows("local", "qwen3.8:27b-q8_0", options.ServerObservedAt) {
		t.Fatal("local model not exposed in model-options", options, err)
	}
	prefs, err := api.Call[domain.ProviderPreferences](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "preferences"), nil)
	if err != nil || prefs.Provider != "local" || prefs.Model != "qwen3.8:27b-q8_0" {
		t.Fatal("preferences not set to local qwen3.8:27b-q8_0", prefs, err)
	}

	// Worker should automatically wake and complete onboarding using local Qwen 3.8 without Codex/Copilot login!
	deadline := time.Now().Add(10 * time.Second)
	for exec.localInvoked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if exec.localInvoked.Load() == 0 {
		t.Fatal("worker did not wake and invoke local Qwen 3.8 after initialization")
	}
}
