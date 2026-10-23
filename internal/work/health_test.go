package work

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

func healthEngine(t *testing.T) (*Engine, *int64) {
	t.Helper()
	state, err := local.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	c := config.DefaultWorker(state.Root)
	engine := New(c, "", nil, state, nil)
	engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	now := int64(1800000000)
	engine.Now = func() time.Time { return time.Unix(now, 0) }
	return engine, &now
}

func readHealth(t *testing.T, engine *Engine) *domain.WorkerHealth {
	t.Helper()
	health, err := engine.health(context.Background())
	if err != nil || health == nil {
		t.Fatalf("missing health observation: %+v %v", health, err)
	}
	return health
}

func TestWorkerHealthReportsLivePhaseWithoutFinishedWorkStatus(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	if health, err := engine.health(ctx); err != nil || health != nil {
		t.Fatal("unobserved worker appeared healthy", health, err)
	}
	if err := engine.beginHealth(ctx, engine.Config); err != nil {
		t.Fatal(err)
	}
	launch := Launch{Phase: "message", Message: &domain.Message{ID: "message_requested", Body: "PRIVATE_MESSAGE_MUST_STAY_LOCAL"}}
	if err := engine.phaseHealth(ctx, "invoking", &launch, "invocation_message", 300, true); err != nil {
		t.Fatal(err)
	}
	if err := engine.State.SaveJournal(ctx, local.Journal{ID: "invocation_message", State: "invoking", CreatedAt: *now}); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(ctx)
	if err != nil || status.Health == nil || status.Health.Phase != "invoking" || status.Health.ActionID != launch.Message.ID || status.Health.DeadlineAt != *now+300 {
		t.Fatal("live provider phase waits for Step to return", status, err)
	}
	if status.Health.Blocker != "" || status.Health.StepFinishedAt != 0 {
		t.Fatal("live invocation incorrectly became an interrupted fence", status.Health)
	}
	data, err := json.Marshal(status.Health)
	if err != nil || strings.Contains(string(data), launch.Message.Body) {
		t.Fatal("health included private message text", string(data), err)
	}
	sequence := status.Health.Sequence
	*now += 10
	if health := readHealth(t, engine); health.Sequence != sequence || health.LastProgressAt != *now-10 || health.ObservedAt != *now {
		t.Fatal("read-only collection changed progress or sequence", health)
	}
}

func TestWorkerHealthIdlePollingDoesNotCountAsProgress(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	if err := engine.beginHealth(ctx, engine.Config); err != nil {
		t.Fatal(err)
	}
	if err := engine.finishHealth(ctx, Status{State: "idle", Mode: "ai"}, nil); err != nil {
		t.Fatal(err)
	}
	if health := readHealth(t, engine); health.LastProgressAt != 0 {
		t.Fatal("idle step manufactured business progress", health)
	}
	launch := Launch{Phase: "message", Message: &domain.Message{ID: "message_completed"}}
	if err := engine.phaseHealth(ctx, "applying", &launch, "invocation_completed", 30, true); err != nil {
		t.Fatal(err)
	}
	if err := engine.completedHealth(ctx); err != nil {
		t.Fatal(err)
	}
	progress := *now
	for range 3 {
		*now += 30
		if err := engine.beginHealth(ctx, engine.Config); err != nil {
			t.Fatal(err)
		}
		if err := engine.finishHealth(ctx, Status{State: "idle", Mode: "ai"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	health := readHealth(t, engine)
	if health.LastProgressAt != progress || health.LastActionID != launch.Message.ID || health.ActionID != "" || health.StepFinishedAt != *now {
		t.Fatal("polling hid stalled action progress or lost completion", health)
	}
}

func TestWorkerHealthProblemsAreTimestampedRedactedAndResolved(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	secret := "PRIVATE_PATH_OR_PROVIDER_TRANSCRIPT"
	engine.recordProblem(ctx, "watch-problem", domain.Fail(domain.Unavailable, secret))
	first := readHealth(t, engine)
	if len(first.Problems) != 1 || first.Problems[0].Component != "watch" || first.Problems[0].FirstSeenAt != *now {
		t.Fatal("watch failure was not visible before first controller step", first)
	}
	*now += 30
	engine.recordProblem(ctx, "watch-problem", domain.Fail(domain.Unavailable, secret))
	repeated := readHealth(t, engine)
	if repeated.Problems[0].FirstSeenAt != first.Problems[0].FirstSeenAt || repeated.Problems[0].LastSeenAt != *now {
		t.Fatal("polling reset the diagnostic episode", repeated)
	}
	data, _ := json.Marshal(repeated)
	if strings.Contains(string(data), secret) {
		t.Fatal("private error crossed the health-report boundary", string(data))
	}
	*now += 30
	engine.clearProblem(ctx, "watch-problem")
	resolved := readHealth(t, engine)
	if resolved.Problems[0].ResolvedAt != *now || resolved.Problems[0].LastSeenAt != repeated.Problems[0].LastSeenAt {
		t.Fatal("recovery did not retain failure timing", resolved)
	}
	var private domain.Fault
	if found, err := engine.State.Get(ctx, "watch-problem", &private); err != nil || !found || private.Message != secret {
		t.Fatal("private diagnostic evidence was erased", private, err)
	}
}

func TestWorkerHealthSignInDoesNotClearPauseOrInvocationFence(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	if err := engine.beginHealth(ctx, engine.Config); err != nil {
		t.Fatal(err)
	}
	if err := engine.blockHealth(ctx, "authentication-required"); err != nil {
		t.Fatal(err)
	}
	observation := provider.Health{Version: "1.2.3", Authentication: "credentials-present-unverified"}
	if err := engine.providerHealth(ctx, "codex", "gpt-fixture", observation, nil); err != nil {
		t.Fatal(err)
	}
	if err := engine.finishHealth(ctx, Status{State: "authentication-refreshed", Mode: "manual"}, nil); err != nil {
		t.Fatal(err)
	}
	paused := readHealth(t, engine)
	if paused.Authentication != "credentials-present-unverified" || paused.Phase != "paused" || paused.Blocker != "founder-paused" {
		t.Fatal("sign-in was mistaken for resumption or validated vendor usability", paused)
	}
	if err := engine.State.SaveJournal(ctx, local.Journal{ID: "invocation_uncertain", Kind: "message", State: "uncertain", CreatedAt: *now}); err != nil {
		t.Fatal(err)
	}
	fenced := readHealth(t, engine)
	if fenced.Blocker != "invocation-uncertain" || fenced.JournalID != "invocation_uncertain" {
		t.Fatal("signed-in worker hid retained uncertain work", fenced)
	}
	journals, err := engine.State.Journals(ctx, true)
	if err != nil || len(journals) != 1 || journals[0].State != "uncertain" {
		t.Fatal("read-only diagnostics changed recovery authority", journals, err)
	}
}

func TestWorkerHealthRepeatedBlockerRetainsStartAndSuccessfulReplyClearsIt(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	for range 2 {
		if err := engine.beginHealth(ctx, engine.Config); err != nil {
			t.Fatal(err)
		}
		if err := engine.blockHealth(ctx, "authentication-required"); err != nil {
			t.Fatal(err)
		}
		if err := engine.finishHealth(ctx, Status{Mode: "ai"}, domain.Fail(domain.Prerequisite, "private details")); err != nil {
			t.Fatal(err)
		}
		*now += 30
	}
	blocked := readHealth(t, engine)
	if blocked.BlockedSinceAt != *now-60 {
		t.Fatal("unchanged polling reset blocker age", blocked)
	}
	if err := engine.beginHealth(ctx, engine.Config); err != nil {
		t.Fatal(err)
	}
	launch := Launch{Message: &domain.Message{ID: "message_next"}}
	if err := engine.phaseHealth(ctx, "applying", &launch, "invocation_next", 30, true); err != nil {
		t.Fatal(err)
	}
	if err := engine.completedHealth(ctx); err != nil {
		t.Fatal(err)
	}
	status := Status{State: "message", Mode: "ai", Blockers: []Blocker{{TaskID: "task_unrelated", Code: domain.Prerequisite}}}
	if err := engine.finishHealth(ctx, status, nil); err != nil {
		t.Fatal(err)
	}
	if health := readHealth(t, engine); health.Blocker != "" || health.Phase != "idle" || health.LastActionID != launch.Message.ID {
		t.Fatal("an unrelated task blocker hid a successful message response", health)
	}
}

func TestWorkerHealthFutureFormatFailsWithoutOverwritingEvidence(t *testing.T) {
	ctx := context.Background()
	engine, _ := healthEngine(t)
	future := workerHealthState{Health: newWorkerHealth(engine.Config)}
	future.Health.Version++
	if err := engine.State.Put(ctx, workerHealthKey, future); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.health(ctx); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("future observation format was silently accepted", err)
	}
	if err := engine.beginHealth(ctx, engine.Config); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("future observation format was overwritten", err)
	}
	var retained workerHealthState
	if _, err := engine.State.Get(ctx, workerHealthKey, &retained); err != nil || retained.Health.Version != future.Health.Version {
		t.Fatal("future evidence was changed", retained, err)
	}
}

func TestWorkerHealthFiltersProviderMetadataAndExposesOldFences(t *testing.T) {
	ctx := context.Background()
	engine, now := healthEngine(t)
	if err := engine.providerHealth(ctx, "codex", "/PRIVATE/model path", provider.Health{Version: "/PRIVATE/version output", Authentication: "unexpected private output"}, nil); err != nil {
		t.Fatal(err)
	}
	health := readHealth(t, engine)
	if health.Model != "" || health.ProviderVersion != "" || health.Authentication != "unknown" {
		t.Fatal("unvalidated provider output entered shared telemetry", health)
	}
	if err := engine.State.SaveJournal(ctx, local.Journal{ID: "invocation_legacy", State: "invoking", CreatedAt: *now}); err != nil {
		t.Fatal(err)
	}
	if health := readHealth(t, engine); health.Blocker != "invocation-uncertain" {
		t.Fatal("old invoking journal became healthy after upgrade", health)
	}
	if err := engine.State.Put(ctx, "session-fence", map[string]any{"active": true, "id": "PRIVATE_SESSION_REFERENCE"}); err != nil {
		t.Fatal(err)
	}
	health = readHealth(t, engine)
	data, _ := json.Marshal(health)
	if health.Blocker != "session-fence" || strings.Contains(string(data), "PRIVATE_SESSION_REFERENCE") {
		t.Fatal("session fence was hidden or private session reference leaked", health)
	}
}
