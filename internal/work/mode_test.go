package work

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestOnlyFounderControlMirrorsModeAndStaleObservationsCannotUndoPause(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	c := config.DefaultWorker(state.Root)
	path := filepath.Join(state.Root, "agent.json")
	if err := config.Write(path, c); err != nil {
		t.Fatal(err)
	}
	actor := domain.Agent{ID: "agent_fixture", Active: true, Revision: 1, Mode: "ai"}
	if _, err := ReconcileMode(ctx, state, c, path, actor); err != nil {
		t.Fatal(err)
	}
	paused := actor
	paused.Revision = 2
	paused.Mode = "manual"
	if _, err := ReconcileMode(ctx, state, c, path, paused); err == nil {
		t.Fatal("manual mode without Founder request was accepted")
	}
	paused.ModeRequestedBy = "agent_founder"
	paused.ModeReason = "Founder requests maintenance"
	c, err = ReconcileMode(ctx, state, c, path, paused)
	if err != nil || c.Mode != "manual" {
		t.Fatal("Founder pause was not mirrored", c, err)
	}
	if _, err := ReconcileMode(ctx, state, c, path, actor); err == nil {
		t.Fatal("stale server observation undid the current pause")
	}
	stored, err := config.LoadWorker(path)
	if err != nil || stored.Mode != "manual" {
		t.Fatal("protected local mode was changed", err)
	}
	resume := paused
	resume.Revision = 3
	resume.Mode = "ai"
	resume.ModeReason = "Founder explicitly resumes"
	c, err = ReconcileMode(ctx, state, c, path, resume)
	if err != nil || c.Mode != "ai" {
		t.Fatal("Founder resume not applied", c, err)
	}
}

func TestUnexplainedLegacyManualStateDoesNotAutoResume(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	c := config.DefaultWorker(state.Root)
	c.Mode = "manual"
	actor := domain.Agent{ID: "agent_fixture", Active: true, Revision: 1, Mode: "ai"}
	if _, err := ReconcileMode(ctx, state, c, "", actor); err == nil {
		t.Fatal("legacy pause was silently reset")
	}
	actor.Revision = 2
	actor.ModeRequestedBy = "agent_founder"
	if updated, err := ReconcileMode(ctx, state, c, "", actor); err != nil || updated.Mode != "ai" {
		t.Fatal("explicit Founder reconciliation failed", err)
	}
}
