package work

import (
	"testing"
	"time"
)

func TestAgentOperationCaptureClockUsesServerAnchorAndElapsedTime(t *testing.T) {
	requestStarted := time.Now()
	clock := agentOperationClock{serverAt: 1800000000, requestStarted: requestStarted}
	if capture := clock.capturedAt(requestStarted.Add(7 * time.Second)); capture != clock.serverAt+7 {
		t.Fatal("operation deadline used target wall clock", capture)
	}
	if capture := clock.capturedAt(requestStarted.Add(-time.Second)); capture != 0 {
		t.Fatal("backward elapsed time produced trusted capture evidence", capture)
	}
	if capture := (agentOperationClock{}).capturedAt(time.Now()); capture != 0 {
		t.Fatal("missing server clock produced trusted capture evidence", capture)
	}
}
