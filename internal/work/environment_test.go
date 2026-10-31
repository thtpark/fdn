package work

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

type environmentOnboardingExecutor struct {
	*pushExecutor
	launch Launch
}

func (x *environmentOnboardingExecutor) Invoke(_ context.Context, invocation provider.Invocation) (provider.Reply, error) {
	x.invocations.Add(1)
	_, data, found := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !found {
		return provider.Reply{}, domain.Fail(domain.Invalid, "fixture did not receive the bounded onboarding context")
	}
	if err := json.Unmarshal([]byte(data), &x.launch); err != nil {
		return provider.Reply{}, err
	}
	return provider.Reply{Outcome: "reviewed", Summary: "Reviewed the shared native capacity snapshot"}, nil
}

func TestEnvironmentLegacyStartupAndReportingDoNotRepeatInitialAIReview(t *testing.T) {
	ctx := context.Background()
	f := newPushFixture(t)
	inventory, err := f.engine.State.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inventory.SystemCapabilities = nil
	if err := f.engine.State.Put(ctx, "inventory", inventory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for range 2 {
		status, err := f.engine.Step(ctx, false)
		if err != nil || status.State != "idle" || !status.InitialReviewDone || f.executor.invocations.Load() != 0 {
			t.Fatal("legacy enrichment repeated or blocked a completed initial review", status.State, err)
		}
		if err := f.engine.publishEnvironment(ctx); err != nil {
			t.Fatal(err)
		}
	}
	retained, err := f.engine.State.Inventory(ctx)
	if err != nil || retained.SystemCapabilities == nil || retained.Hash() != inventory.Hash() {
		t.Fatal("startup/reporting did not retain the shared inventory and review basis", err)
	}
	var reviewed string
	if found, err := f.engine.State.Get(ctx, "initial-review", &reviewed); err != nil || !found || reviewed != inventory.Hash() {
		t.Fatal("environment publication changed initial-review evidence", err)
	}
	journals, err := f.engine.State.Journals(ctx, false)
	if err != nil || len(journals) != 0 {
		t.Fatal("metadata enrichment created an AI invocation journal", err)
	}
	f.mu.Lock()
	publications := f.counts["POST /v1/agents/"+f.me.ID+"/environment"]
	f.mu.Unlock()
	if publications != 2 {
		t.Fatal("native reporter did not publish through the existing authenticated endpoint")
	}
}

func TestEnvironmentInitialOnboardingReceivesTheSharedStableSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newPushFixture(t)
	inventory, err := f.engine.State.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.State.Put(ctx, "initial-review", ""); err != nil {
		t.Fatal(err)
	}
	executor := &environmentOnboardingExecutor{pushExecutor: f.executor}
	f.engine.Executor = executor
	status, err := f.engine.Step(ctx, false)
	if err != nil || status.State != "onboarding" || executor.invocations.Load() != 1 {
		t.Fatal("initial bounded review was not executed", status.State, err)
	}
	if executor.launch.Phase != "onboarding" || executor.launch.InventoryHash != inventory.Hash() ||
		!reflect.DeepEqual(executor.launch.Capabilities, inventory.Summary()) ||
		!reflect.DeepEqual(executor.launch.SystemCapabilities, inventory.SystemCapabilities) {
		t.Fatal("onboarding received a separate collection or omitted detailed stable system capacity")
	}
	status, err = f.engine.Step(ctx, false)
	if err != nil || !status.InitialReviewDone || executor.invocations.Load() != 1 {
		t.Fatal("a completed shared-capacity review was not retained", err)
	}
}
