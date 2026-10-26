package work

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

func TestActivityWakeSuppressesNormalAndUnchangedWorkPolling(t *testing.T) {
	e, _ := healthEngine(t)
	ctx := context.Background()
	if err := e.beginHealth(ctx, e.Config); err != nil {
		t.Fatal(err)
	}
	if err := e.finishHealth(ctx, Status{State: "idle", Mode: "ai"}, nil); err != nil {
		t.Fatal(err)
	}
	launch := Launch{Phase: "scope", Task: &domain.Task{ID: "task_private"}}
	if err := e.phaseHealth(ctx, "invoking", &launch, "invocation_scope", 300, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.statusWake:
		t.Fatal("normal phase caused extra immediate heartbeat")
	default:
	}
	if err := e.waitingActivity(ctx, "clarification"); err != nil {
		t.Fatal(err)
	}
	if err := e.finishHealth(ctx, Status{State: "blocked", Mode: "ai"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.statusWake:
	default:
		t.Fatal("new blocker was not scheduled promptly")
	}
	for range 3 {
		if err := e.beginHealth(ctx, e.Config); err != nil {
			t.Fatal(err)
		}
		if err := e.phaseHealth(ctx, "preparing", &launch, "", 120, false); err != nil {
			t.Fatal(err)
		}
		if err := e.waitingActivity(ctx, "clarification"); err != nil {
			t.Fatal(err)
		}
		if err := e.finishHealth(ctx, Status{State: "blocked", Mode: "ai"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-e.statusWake:
		t.Fatal("unchanged blocked polls spammed critical publication")
	default:
	}
	status, err := e.Status(ctx)
	if err != nil || status.ActivityText != "Waiting for clarification" || status.Health.Activity != "clarification" {
		t.Fatal("local status is not human-readable", status, err)
	}
	if err := e.phaseHealth(ctx, "invoking", &launch, "invocation_resumed", 300, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.statusWake:
	default:
		t.Fatal("observed recovery did not request prompt publication")
	}
}

func TestUnobservedLocalActivityDoesNotPretendHealthy(t *testing.T) {
	e, _ := healthEngine(t)
	status, err := e.Status(context.Background())
	if err != nil || status.ActivityText != "No activity observed yet" {
		t.Fatal("missing activity became success", status, err)
	}
}

func TestProjectedFenceRecoveryIsObservedWithoutAStoredPhaseChange(t *testing.T) {
	e, _ := healthEngine(t)
	ctx := context.Background()
	if err := e.beginHealth(ctx, e.Config); err != nil {
		t.Fatal(err)
	}
	if err := e.finishHealth(ctx, Status{State: "idle", Mode: "ai"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.State.Put(ctx, "session-fence", remote.Fence{}); err != nil {
		t.Fatal(err)
	}
	before, err := e.localSafetyFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	health, err := e.health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.observeProjectedActivity(health)
	if err := e.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: "session_transient"}); err != nil {
		t.Fatal(err)
	}
	blocked, err := e.health(ctx)
	if err != nil || blocked.Blocker != "session-fence" || !e.observeProjectedActivity(blocked) {
		t.Fatal("transient projected fence was not observed", blocked, err)
	}
	if err := e.State.Put(ctx, "session-fence", remote.Fence{}); err != nil {
		t.Fatal(err)
	}
	after, err := e.localSafetyFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("fixture did not restore the same local safety fingerprint")
	}
	recovered, err := e.health(ctx)
	if err != nil || recovered.Blocker != "" || !e.observeProjectedActivity(recovered) {
		t.Fatal("projected recovery disappeared between local observations", recovered, err)
	}
	if e.observeProjectedActivity(recovered) {
		t.Fatal("unchanged idle projection created status churn")
	}
}
