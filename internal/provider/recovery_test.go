package provider

import (
	"strings"
	"testing"
)

func TestRecoveryResultRequiresAnInspectionAndObservedBlocker(t *testing.T) {
	blocker := &RecoveryBlocker{
		Kind: "unresolved-effect", Reference: "operation_checked",
		Observation: "The original external operation has no authoritative receipt.",
		NextAction:  "Read its authoritative result without resubmitting it.",
	}
	for _, outcome := range []string{"paused", "failed"} {
		if err := (RecoveryResult{InspectionID: "inspection_owned", Blocker: blocker}).Validate(outcome); err != nil {
			t.Fatal(outcome, err)
		}
	}
	if err := (RecoveryResult{InspectionID: "inspection_owned"}).Validate("reconciled"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		outcome string
		result  RecoveryResult
	}{
		{"missing-receipt", "paused", RecoveryResult{Blocker: blocker}},
		{"missing-observation", "paused", RecoveryResult{InspectionID: "inspection_owned"}},
		{"unsafe-flag-is-not-a-blocker", "paused", RecoveryResult{InspectionID: "inspection_owned", Blocker: &RecoveryBlocker{Kind: "unsafe_retry"}}},
		{"zero-retries-is-not-a-blocker", "paused", RecoveryResult{InspectionID: "inspection_owned", Blocker: &RecoveryBlocker{Kind: "retry-budget"}}},
		{"unidentified-effect", "paused", RecoveryResult{InspectionID: "inspection_owned", Blocker: &RecoveryBlocker{Kind: "unresolved-effect"}}},
		{"unresolved-is-not-reconciled", "reconciled", RecoveryResult{InspectionID: "inspection_owned", Blocker: blocker}},
		{"wrong-phase", "completed", RecoveryResult{InspectionID: "inspection_owned", Blocker: blocker}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.result.Validate(test.outcome); err == nil {
				t.Fatal("unsupported recovery result was accepted")
			}
		})
	}
	oversized := *blocker
	oversized.Reference = strings.Repeat("x", 4097)
	if err := (RecoveryResult{InspectionID: "inspection_owned", Blocker: &oversized}).Validate("paused"); err == nil {
		t.Fatal("unbounded blocker reference was accepted")
	}
}
