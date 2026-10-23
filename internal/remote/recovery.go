package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

type SessionFenceSnapshot struct {
	Fence       Fence
	Fingerprint string
}

func ReadSessionFence(ctx context.Context, state *local.State) (SessionFenceSnapshot, error) {
	snapshot, err := state.WorkSnapshot(ctx, []string{"session-fence"})
	if err != nil {
		return SessionFenceSnapshot{}, err
	}
	data, found := snapshot.Values["session-fence"]
	if !found {
		return SessionFenceSnapshot{}, nil
	}
	var fence Fence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fence); err != nil {
		return SessionFenceSnapshot{}, domain.Fail(domain.Prerequisite, "unsupported session fence evidence; retain state for an authorized native operator")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return SessionFenceSnapshot{}, domain.Fail(domain.Prerequisite, "session fence evidence has an unsupported format")
	}
	if fence.Active && (!domain.ValidOperation(fence.ID) || (fence.Generation != "" && !domain.ValidOperation(fence.Generation))) {
		return SessionFenceSnapshot{}, domain.Fail(domain.Prerequisite, "session fence has an unsupported ownership reference")
	}
	return SessionFenceSnapshot{Fence: fence, Fingerprint: domain.Hash(string(data))}, nil
}

func (snapshot SessionFenceSnapshot) RecoveryObservation(session *domain.Session, slot string) *domain.SessionFenceObservation {
	if !snapshot.Fence.Active {
		return nil
	}
	observation := &domain.SessionFenceObservation{SessionID: snapshot.Fence.ID, Fingerprint: snapshot.Fingerprint,
		CleanupProven: snapshot.Fence.CleanupProven, SessionState: "unknown", RecoveryState: "session-unavailable"}
	if session == nil || session.ID != snapshot.Fence.ID {
		return observation
	}
	observation.SessionState = session.State
	if session.State != "closed" && session.State != "expired" && session.State != "denied" && session.State != "revoked" && session.State != "failed" {
		observation.RecoveryState = "session-live"
		return observation
	}
	if slot != "free" {
		observation.RecoveryState = "slot-occupied"
		return observation
	}
	if !snapshot.Fence.CleanupProven {
		observation.RecoveryState = "cleanup-unproven"
		return observation
	}
	observation.RecoveryState = "recoverable"
	return observation
}
