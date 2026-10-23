package remote

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestSessionFenceObservationRequiresDurableNativeCleanupProof(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if snapshot, err := ReadSessionFence(ctx, state); err != nil || snapshot.Fence.Active {
		t.Fatal(snapshot, err)
	}
	if err := state.Put(ctx, "session-fence", Fence{Active: true, ID: "session_exact"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := ReadSessionFence(ctx, state)
	if err != nil || !domain.ValidHash(legacy.Fingerprint) {
		t.Fatal(legacy, err)
	}
	closed := &domain.Session{ID: "session_exact", State: "closed"}
	if observation := legacy.RecoveryObservation(closed, "free"); observation.RecoveryState != "cleanup-unproven" || observation.CleanupProven {
		t.Fatal("legacy fence manufactured absence proof", observation)
	}
	if err := state.Put(ctx, "session-fence", Fence{Active: true, ID: "session_exact", CleanupProven: true, Generation: "fence_owned"}); err != nil {
		t.Fatal(err)
	}
	proved, err := ReadSessionFence(ctx, state)
	if err != nil || proved.Fingerprint == legacy.Fingerprint {
		t.Fatal("changed proof did not change fingerprint", proved, err)
	}
	for _, item := range []struct{ name, state, slot, want string }{{"exact-closed", "closed", "free", "recoverable"}, {"denied", "denied", "free", "recoverable"}, {"failed", "failed", "free", "recoverable"}, {"expired", "expired", "free", "recoverable"}, {"active-endpoint", "active", "free", "session-live"}, {"consented", "consented", "free", "session-live"}, {"shared-slot", "closed", "occupied", "slot-occupied"}, {"unobserved-slot", "closed", "unknown", "slot-occupied"}} {
		t.Run(item.name, func(t *testing.T) {
			observation := proved.RecoveryObservation(&domain.Session{ID: "session_exact", State: item.state}, item.slot)
			if observation.RecoveryState != item.want {
				t.Fatal(observation)
			}
		})
	}
	if observation := proved.RecoveryObservation(&domain.Session{ID: "session_other", State: "closed"}, "free"); observation.RecoveryState != "session-unavailable" {
		t.Fatal("different session proved recovery", observation)
	}
}

func TestSessionFenceFutureEvidenceFailsClosed(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	future := map[string]any{"active": true, "id": "session_exact", "cleanup_proven": true, "new_ownership_scheme": true}
	if err := state.Put(ctx, "session-fence", future); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSessionFence(ctx, state); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("future ownership scheme was treated as recovery proof", err)
	}
	var retained map[string]any
	if _, err := state.Get(ctx, "session-fence", &retained); err != nil || retained["new_ownership_scheme"] != true {
		t.Fatal("future ownership evidence was rewritten", retained, err)
	}
}
