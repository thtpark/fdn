package provider

import "testing"

func TestVerificationDecisionRequiresReviewedOutcome(t *testing.T) {
	for _, tc := range []struct {
		outcome, decision string
		valid             bool
	}{
		{"reviewed", "accept", true},
		{"reviewed", "rework", true},
		{"reviewed", "", true},
		{"reviewed", "merge", false},
		{"completed", "accept", false},
		{"responded", "rework", false},
		{"paused", "accept", false},
	} {
		t.Run(tc.outcome+"-"+tc.decision, func(t *testing.T) {
			err := (Reply{Outcome: tc.outcome, Decision: tc.decision, Summary: "intentional verification summary"}).Validate()
			if (err == nil) != tc.valid {
				t.Fatal("unexpected decision validation result", err)
			}
		})
	}
}
