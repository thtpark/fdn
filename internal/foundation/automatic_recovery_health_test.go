package foundation

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCleanupGetsServerTimedRecoveryGraceBeforeHumanAlert(t *testing.T) {
	f := newFixture(t)
	base := f.now
	h := observedHealth(base.Unix() + 3600)
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "session-fence", h.ObservedAt-600
	publishHealth(t, f.s, f.worker.ID, h)
	for _, seconds := range []int{0, 29, 30} {
		f.now = base.Add(time.Duration(seconds) * time.Second)
		sweepHealth(t, f.s)
		messages := healthMessages(t, f.s, f.worker.ID, f.founder.ID)
		if seconds < 30 && len(messages) != 0 {
			t.Fatal("human was interrupted before autonomous cleanup grace", seconds, messages)
		}
		if seconds == 30 && (len(messages) != 1 || !strings.Contains(messages[0].Body, "automatically")) {
			t.Fatal("persistent cleanup did not produce the bounded fallback alert", messages)
		}
	}
	sweepHealth(t, f.s)
	if messages := healthMessages(t, f.s, f.worker.ID, f.founder.ID); len(messages) != 1 {
		t.Fatal("cleanup fallback notification was duplicated", messages)
	}
}

func TestAutomaticSessionCleanupRecoveryDoesNotInterruptHuman(t *testing.T) {
	f := newFixture(t)
	h := observedHealth(f.now.Unix())
	h.Phase, h.Blocker, h.BlockedSinceAt = "blocked", "session-fence", h.ObservedAt
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	f.now = f.now.Add(2 * time.Second)
	h = observedHealth(f.now.Unix())
	h.Sequence = 2
	publishHealth(t, f.s, f.worker.ID, h)
	sweepHealth(t, f.s)
	f.now = f.now.Add(30 * time.Second)
	sweepHealth(t, f.s)
	if messages := healthMessages(t, f.s, f.worker.ID, f.founder.ID); len(messages) != 0 {
		t.Fatal("an autonomously recovered session interrupted Founder", messages)
	}
}
