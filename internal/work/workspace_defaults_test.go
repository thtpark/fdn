package work

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestProjectWorkspaceUsesNormalLocalAccessAndPreservesExplicitDisable(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	e := &Engine{State: state}
	c := config.DefaultWorker(state.Root)
	if !c.AllowWorkspaceTools {
		t.Fatal("fresh workers still default to redundant local tool opt-in")
	}
	c.AllowWorkspaceTools = false
	binding, err := e.projectWorkspace(ctx, c, "project_default_access")
	if err != nil || !binding.Enabled || !binding.AllowTools {
		t.Fatal("legacy configuration opt-in blocked normal OS-user access", binding, err)
	}
	again, err := e.projectWorkspace(ctx, c, "project_default_access")
	if err != nil || again.Ref != binding.Ref {
		t.Fatal("automatic local setup replaced an existing owned binding", again, err)
	}
	if err := state.EnableWorkspace(ctx, binding.Ref, false); err != nil {
		t.Fatal(err)
	}
	_, err = e.projectWorkspace(ctx, c, "project_default_access")
	if err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("default local access bypassed an explicit binding disable", err)
	}
}
