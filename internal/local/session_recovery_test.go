package local

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func sessionRecoveryState(t *testing.T) *State {
	t.Helper()
	state, err := Open(context.Background(), filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	return state
}

func saveRecoveryFence(t *testing.T, state *State, proof bool, generation string) string {
	t.Helper()
	fence := map[string]any{"active": true, "id": "session_exact", "cleanup_proven": proof, "generation": generation}
	if err := state.Put(context.Background(), "session-fence", fence); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fence)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Hash(string(data))
}

func TestSessionRecoveryAtomicallyClearsOnlyProvenExactFence(t *testing.T) {
	ctx := context.Background()
	state := sessionRecoveryState(t)
	fingerprint := saveRecoveryFence(t, state, true, "fence_original")
	key := "agent-operation-result:operation_one"
	receipt := map[string]string{"state": "recovered", "id": "operation_one"}
	if err := state.CommitSessionRecovery(ctx, "session_exact", fingerprint, key, receipt); err != nil {
		t.Fatal(err)
	}
	var fence struct {
		Active bool `json:"active"`
	}
	if _, err := state.Get(ctx, "session-fence", &fence); err != nil || fence.Active {
		t.Fatal("fence did not clear", fence, err)
	}
	var result map[string]string
	if found, err := state.Get(ctx, key, &result); err != nil || !found || result["state"] != "recovered" {
		t.Fatal("durable result missing", result, err)
	}
	newFingerprint := saveRecoveryFence(t, state, true, "fence_replacement")
	if err := state.CommitSessionRecovery(ctx, "session_exact", fingerprint, key, receipt); err != nil {
		t.Fatal("exact result replay failed", err)
	}
	snapshot, err := state.WorkSnapshot(ctx, []string{"session-fence"})
	if err != nil || domain.Hash(string(snapshot.Values["session-fence"])) != newFingerprint {
		t.Fatal("lost-ack replay cleared replacement fence", err)
	}
	if err := state.CommitSessionRecovery(ctx, "session_exact", newFingerprint, key, map[string]string{"state": "changed"}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("changed result reused operation", err)
	}
}

func TestSessionRecoveryRejectsUnprovenAndChangedFenceWithoutReceipt(t *testing.T) {
	for _, item := range []struct {
		name          string
		proof, change bool
		code          domain.Code
	}{{"legacy-unproven", false, false, domain.Prerequisite}, {"changed-generation", true, true, domain.Conflict}} {
		t.Run(item.name, func(t *testing.T) {
			ctx := context.Background()
			state := sessionRecoveryState(t)
			fingerprint := saveRecoveryFence(t, state, item.proof, "fence_original")
			if item.change {
				saveRecoveryFence(t, state, true, "fence_replacement")
			}
			key := "agent-operation-result:operation_rejected"
			if err := state.CommitSessionRecovery(ctx, "session_exact", fingerprint, key, map[string]string{"state": "recovered"}); domain.AsFault(err).Code != item.code {
				t.Fatal("unsafe recovery accepted", err)
			}
			var receipt any
			if found, err := state.Get(ctx, key, &receipt); err != nil || found {
				t.Fatal("failed recovery fabricated result", found, err)
			}
			var fence struct {
				Active bool `json:"active"`
			}
			if _, err := state.Get(ctx, "session-fence", &fence); err != nil || !fence.Active {
				t.Fatal("failed recovery changed fence", fence, err)
			}
		})
	}
}

func TestSessionRecoveryReceiptFailureRollsBackFence(t *testing.T) {
	ctx := context.Background()
	state := sessionRecoveryState(t)
	fingerprint := saveRecoveryFence(t, state, true, "fence_atomic")
	if _, err := state.db.ExecContext(ctx, `CREATE TRIGGER fixture_receipt_failure BEFORE INSERT ON kv WHEN NEW.key='agent-operation-result:operation_atomic' BEGIN SELECT RAISE(ABORT, 'fixture receipt write failure'); END`); err != nil {
		t.Fatal(err)
	}
	key := "agent-operation-result:operation_atomic"
	if err := state.CommitSessionRecovery(ctx, "session_exact", fingerprint, key, map[string]string{"state": "recovered"}); err == nil {
		t.Fatal("fixture did not fail after the fence update")
	}
	snapshot, err := state.WorkSnapshot(ctx, []string{"session-fence", key})
	if err != nil || domain.Hash(string(snapshot.Values["session-fence"])) != fingerprint || snapshot.Values[key] != nil {
		t.Fatal("failed durable receipt left a partially cleared fence", snapshot, err)
	}
}

func TestSessionRecoveryCompetingOperationsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	state := sessionRecoveryState(t)
	fingerprint := saveRecoveryFence(t, state, true, "fence_single")
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, id := range []string{"operation_one", "operation_two"} {
		go func(id string) {
			start.Wait()
			results <- state.CommitSessionRecovery(ctx, "session_exact", fingerprint, "agent-operation-result:"+id, map[string]string{"id": id})
		}(id)
	}
	start.Done()
	wins := 0
	for range 2 {
		if err := <-results; err == nil {
			wins++
		} else if domain.AsFault(err).Code != domain.Conflict {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("competing recoveries did not have one exact winner", wins)
	}
}
