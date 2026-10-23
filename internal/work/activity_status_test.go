package work

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
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
