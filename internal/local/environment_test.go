package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type environmentVersionRunner struct {
	calls   int
	fail    bool
	version string
}

func (r *environmentVersionRunner) Run(_ context.Context, spec process.Spec) error {
	r.calls++
	if len(spec.Args) != 1 || spec.Args[0] != "--version" {
		return domain.Fail(domain.Invalid, "fixture rejected a non-version command")
	}
	if r.fail {
		return domain.Fail(domain.Unavailable, "fixture unavailable")
	}
	version := r.version
	if version == "" {
		version = "0.3.16"
	}
	return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]any{
		"version": version, "commit": "fixture", "api_version": domain.APIVersion, "schema_version": domain.SchemaVersion}})
}

func TestEnvironmentEmbeddedFactsNeverRunDesktopPrograms(t *testing.T) {
	state, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	observation, err := state.CollectEmbeddedEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.CPU.LogicalCores <= 0 || observation.Memory.TotalBytes == nil || *observation.Memory.TotalBytes == 0 ||
		len(observation.Executables) != 1 || observation.Executables[0].State != "embedded" {
		t.Fatal("embedded environment did not report measurable resources and its actual runtime")
	}
	inventory, err := state.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Observations) != 2 || inventory.Observations[0].Category != "hardware" ||
		inventory.Observations[1].Category != "filesystem" {
		t.Fatal("embedded initialization performed desktop tool or network discovery")
	}
}

const legacyEnvironmentInventory = `{"id":"inventory_legacy","collected_at":1710000000,"os":"darwin","arch":"arm64","cpus":4,"observations":[{"category":"hardware","name":"physical-memory","present":true,"availability":"unknown","permission":"read-only","unit":"bytes","error_code":"os-enumeration-failed"},{"category":"tool","name":"private-tool-fixture","present":true,"availability":"unknown","permission":"not-checked"}]}`

func environmentTestState(t *testing.T) *State {
	t.Helper()
	state, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	return state
}

func environmentLegacyInventory(t *testing.T) Inventory {
	t.Helper()
	var inventory Inventory
	if err := json.Unmarshal([]byte(legacyEnvironmentInventory), &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func environmentFixtureCapabilities() SystemCapabilities {
	physical, memory, disk := uint64(2), uint64(1<<55), uint64(1<<55)
	return SystemCapabilities{
		System: domain.SystemEnvironment{OS: "darwin", OSVersion: "retained-os", Kernel: "retained-kernel",
			Architecture: "arm64", GoVersion: "retained-runtime"},
		CPU:              domain.CPUEnvironment{Model: "retained-cpu", LogicalCores: 4, PhysicalCores: &physical},
		MemoryTotalBytes: &memory, DiskTotalBytes: &disk,
		Problems: []domain.EnvironmentProblem{{Component: "fixture-static", Code: "retained"}},
	}
}

func TestInventoryReviewHashPreservesHistoricalJSONContract(t *testing.T) {
	inventory := environmentLegacyInventory(t)
	want := domain.Hash(legacyEnvironmentInventory)
	if inventory.Hash() != want {
		t.Fatal("historical inventory review hash changed")
	}
	capabilities := environmentFixtureCapabilities()
	inventory.SystemCapabilities = &capabilities
	if inventory.Hash() != want {
		t.Fatal("metadata-only enrichment invalidated the historical review")
	}
	inventory.SystemCapabilities.System.OSVersion = "changed-metadata"
	if inventory.Hash() != want {
		t.Fatal("safe system metadata became an implicit review refresh")
	}
	inventory.CollectedAt++
	if inventory.Hash() == want {
		t.Fatal("an explicit new review basis was ignored")
	}
}

func TestSystemCapabilitiesLegacyEnrichmentPreservesReviewAndPrivateEvidence(t *testing.T) {
	ctx := context.Background()
	state := environmentTestState(t)
	legacy := environmentLegacyInventory(t)
	if err := state.Put(ctx, "inventory", legacy); err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, "initial-review", legacy.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := state.BindIdentity(ctx, "agent_environment_fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.PutKnowledge(ctx, KnowledgeChange{Kind: "memory", Name: "retained", Content: "private fixture", Selected: true}); err != nil {
		t.Fatal(err)
	}
	journal := Journal{ID: "journal_environment_retained", Kind: "onboarding", State: "uncertain",
		Context: json.RawMessage(legacyEnvironmentInventory), PromptHash: legacy.Hash(), CreatedAt: legacy.CollectedAt}
	if err := state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	outbox := Outbox{OperationID: "environment-retained-operation", Method: "POST", Path: "/v1/fixture",
		Body: json.RawMessage(`{"operation_id":"environment-retained-operation"}`), CreatedAt: legacy.CollectedAt}
	if err := state.Enqueue(ctx, outbox); err != nil {
		t.Fatal(err)
	}
	enriched, err := state.SystemCapabilitiesInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.SystemCapabilities == nil || enriched.Hash() != legacy.Hash() ||
		enriched.SystemCapabilities.System.OS != legacy.OS || enriched.SystemCapabilities.System.Architecture != legacy.Arch ||
		enriched.SystemCapabilities.CPU.LogicalCores != legacy.CPUs || enriched.SystemCapabilities.MemoryTotalBytes != nil {
		t.Fatal("enrichment changed reviewed capacity or fabricated an unknown count")
	}
	enriched.SystemCapabilities = nil
	if !reflect.DeepEqual(enriched, legacy) {
		t.Fatal("enrichment changed historical observations, ID or collection time")
	}
	var review string
	if found, err := state.Get(ctx, "initial-review", &review); err != nil || !found || review != legacy.Hash() {
		t.Fatal("initial review receipt changed", err)
	}
	journals, err := state.Journals(ctx, false)
	if err != nil || !reflect.DeepEqual(journals, []Journal{journal}) {
		t.Fatal("retained invocation was changed", err)
	}
	items, err := state.Outbox(ctx)
	if err != nil || !reflect.DeepEqual(items, []Outbox{outbox}) {
		t.Fatal("retained exact operation was changed", err)
	}
	knowledge, err := state.Knowledge(ctx, "memory:retained")
	if err != nil || knowledge.Content != "private fixture" || !knowledge.Selected {
		t.Fatal("private knowledge was changed", err)
	}
	if err := state.BindIdentity(ctx, "agent_other_fixture"); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("enrichment removed identity binding", err)
	}
	var schema int
	if err := state.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema != 1 {
		t.Fatal("local schema changed", err)
	}
	reopened, err := Open(ctx, state.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	observation, err := reopened.CollectEmbeddedEnvironment(ctx)
	if err != nil || observation.Memory.TotalBytes != nil {
		t.Fatal("unknown reviewed memory was replaced on reopen", err)
	}
	for _, problem := range observation.Problems {
		if problem.Component == "memory-total" && problem.Code == "inventory-observation-unavailable" {
			return
		}
	}
	t.Fatal("unknown retained capacity was not surfaced explicitly")
}

func TestSystemCapabilitiesProjectionDoesNotAliasPrivateSnapshot(t *testing.T) {
	capabilities := environmentFixtureCapabilities()
	before, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	observation := capabilities.environment()
	*observation.CPU.PhysicalCores = 1
	*observation.Memory.TotalBytes = 1
	*observation.Disk.TotalBytes = 1
	observation.Problems[0].Code = "changed"
	after, err := json.Marshal(capabilities)
	if err != nil || string(before) != string(after) {
		t.Fatal("public projection mutated private pointer-backed capacity", err)
	}
}

func TestEnvironmentRefreshUsesSharedInventoryWithoutChangingReview(t *testing.T) {
	ctx := context.Background()
	state := environmentTestState(t)
	inventory := environmentLegacyInventory(t)
	capabilities := environmentFixtureCapabilities()
	inventory.SystemCapabilities = &capabilities
	inventory.Observations[0].Count = copyResourceCount(capabilities.MemoryTotalBytes)
	if err := state.Put(ctx, "inventory", inventory); err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("version-only fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	runner := &environmentVersionRunner{version: "0.3.16"}
	for _, version := range []string{"0.3.16", "0.3.17"} {
		runner.version = version
		observation, err := state.CollectEnvironment(ctx, runner, map[string]string{"codex": filepath.Join(bin, "missing-provider")})
		if err != nil {
			t.Fatal(err)
		}
		if observation.System.OSVersion != capabilities.System.OSVersion || observation.System.Kernel != capabilities.System.Kernel ||
			!reflect.DeepEqual(observation.CPU, capabilities.CPU) || !reflect.DeepEqual(observation.Memory.TotalBytes, capabilities.MemoryTotalBytes) ||
			!reflect.DeepEqual(observation.Disk.TotalBytes, capabilities.DiskTotalBytes) || observation.System.GoVersion == "retained-runtime" {
			t.Fatal("telemetry rediscovered static capacity or failed to refresh the running runtime")
		}
		if observation.Memory.FreeBytes == nil && observation.Memory.AvailableBytes == nil || observation.Disk.AvailableBytes == nil {
			t.Fatal("telemetry omitted measurable live usage")
		}
		if len(observation.Executables) != 4 {
			t.Fatal("native executable projection changed shape")
		}
		for _, executable := range observation.Executables {
			if executable.State != "observed" || executable.Version != version {
				t.Fatal("executable versions were frozen into capacity inventory")
			}
		}
		public, err := json.Marshal(observation)
		if err != nil || strings.Contains(string(public), "private-tool-fixture") || strings.Contains(string(public), state.Root) ||
			strings.Contains(string(public), inventory.ID) {
			t.Fatal("environment projection exposed private inventory", err)
		}
	}
	embedded, err := state.CollectEmbeddedEnvironment(ctx)
	if err != nil || embedded.CPU.Model != capabilities.CPU.Model || runner.calls != 8 {
		t.Fatal("embedded reporting did not reuse the shared inventory without native probes", err)
	}
	retained, err := state.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(retained)
	if err != nil || string(before) != string(after) {
		t.Fatal("live usage or executable observations rewrote private inventory", err)
	}
	var review string
	if found, err := state.Get(ctx, "initial-review", &review); err != nil || !found || review != retained.Hash() {
		t.Fatal("periodic reporting invalidated initial review", err)
	}
}

func TestEnvironmentInitialInventoryHonorsProviderOverridesAndSharedTotals(t *testing.T) {
	ctx := context.Background()
	state := environmentTestState(t)
	bin := t.TempDir()
	codex, copilot := filepath.Join(bin, "managed-codex"), filepath.Join(bin, "managed-copilot")
	if err := os.WriteFile(codex, []byte("provider presence fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copilot, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	runner := &environmentVersionRunner{}
	observation, err := state.CollectEnvironment(ctx, runner, map[string]string{"codex": codex, "copilot": copilot})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := state.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Observations) != 10 || inventory.SystemCapabilities == nil ||
		inventory.SystemCapabilities.System != observation.System || !reflect.DeepEqual(inventory.SystemCapabilities.CPU, observation.CPU) ||
		!reflect.DeepEqual(inventory.SystemCapabilities.MemoryTotalBytes, observation.Memory.TotalBytes) ||
		!reflect.DeepEqual(inventory.SystemCapabilities.DiskTotalBytes, observation.Disk.TotalBytes) {
		t.Fatal("first report and initial capacity discovery used different facts")
	}
	present := map[string]bool{}
	for _, fact := range inventory.Observations {
		present[fact.Name] = fact.Present
		if fact.Name == "physical-memory" && !reflect.DeepEqual(fact.Count, observation.Memory.TotalBytes) {
			t.Fatal("private physical memory was collected independently")
		}
	}
	if !present["codex"] || present["copilot"] || runner.calls != 0 {
		t.Fatal("provider overrides were lost or provider programs were executed")
	}
	var review string
	if found, err := state.Get(ctx, "initial-review", &review); err != nil || found {
		t.Fatal("telemetry manufactured an AI review", err)
	}
}

func TestSystemCapabilitiesStaleCandidateCannotOverwriteConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	state := environmentTestState(t)
	candidate := environmentLegacyInventory(t)
	capabilities := environmentFixtureCapabilities()
	candidate.SystemCapabilities = &capabilities
	newer, err := state.RefreshInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := state.persistSystemInventory(ctx, candidate)
	if err != nil || !reflect.DeepEqual(retained, newer) {
		t.Fatal("stale collection overwrote a newer explicit refresh", err)
	}
	currentLegacy := environmentLegacyInventory(t)
	currentLegacy.ID, currentLegacy.CPUs = "inventory_newer_legacy", 8
	memory := uint64(32 << 30)
	currentLegacy.Observations[0].Count = &memory
	if err := state.Put(ctx, "inventory", currentLegacy); err != nil {
		t.Fatal(err)
	}
	retained, err = state.persistSystemInventory(ctx, candidate)
	if err != nil || retained.Hash() != currentLegacy.Hash() || retained.SystemCapabilities.CPU.LogicalCores != 8 ||
		retained.SystemCapabilities.MemoryTotalBytes == nil || *retained.SystemCapabilities.MemoryTotalBytes != memory {
		t.Fatal("enrichment did not reconcile the current transaction-bound review basis", err)
	}
}

func TestEnvironmentCapacityMismatchRequiresExplicitInventoryRefresh(t *testing.T) {
	ctx := context.Background()
	state := environmentTestState(t)
	inventory := environmentLegacyInventory(t)
	capabilities := environmentFixtureCapabilities()
	minimal := uint64(1)
	capabilities.MemoryTotalBytes, capabilities.DiskTotalBytes = &minimal, &minimal
	inventory.SystemCapabilities = &capabilities
	if err := state.Put(ctx, "inventory", inventory); err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	runner := &environmentVersionRunner{}
	if _, err := state.CollectEnvironment(ctx, runner); domain.AsFault(err).Code != domain.Prerequisite || runner.calls != 0 {
		t.Fatal("changed capacity was silently clamped, refreshed or reported as success", err)
	}
	retained, err := state.Inventory(ctx)
	if err != nil || retained.Hash() != inventory.Hash() {
		t.Fatal("capacity mismatch changed the review basis", err)
	}
	fresh, err := state.RefreshInventory(ctx)
	if err != nil || fresh.ID == inventory.ID || fresh.Hash() == inventory.Hash() || fresh.SystemCapabilities.CPU.Model == "retained-cpu" {
		t.Fatal("explicit refresh did not recapture a new review basis", err)
	}
	if _, err := state.CollectEmbeddedEnvironment(ctx); err != nil {
		t.Fatal("explicit inventory refresh did not unblock valid reporting", err)
	}
	var review string
	if found, err := state.Get(ctx, "initial-review", &review); err != nil || !found || review != inventory.Hash() || review == fresh.Hash() {
		t.Fatal("explicit refresh manufactured a new review receipt", err)
	}
}

func TestEnvironmentExecutableProbeIsBoundedVersionOnlyAndUnavailableIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-cli")
	if err := os.WriteFile(path, []byte("non-executable fixture content"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &environmentVersionRunner{}
	observed := observeFoundationExecutable(context.Background(), runner, "agent-cli", path)
	if observed.State != "observed" || observed.Version != "0.3.16" || !domain.ValidHash(observed.SHA256) || runner.calls != 1 {
		t.Fatal("version probe or digest was not observed", observed)
	}
	runner.fail = true
	observed = observeFoundationExecutable(context.Background(), runner, "agent-cli", path)
	if observed.State != "unavailable" || observed.Version != "" {
		t.Fatal("failed binary probe fabricated a version", observed)
	}
}
