package provider

import (
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

type RecoveryResult struct {
	InspectionID string           `json:"inspection_id"`
	Blocker      *RecoveryBlocker `json:"blocker,omitempty"`
}

type RecoveryBlocker struct {
	Kind        string `json:"kind"`
	Reference   string `json:"reference"`
	Observation string `json:"observation"`
	NextAction  string `json:"next_action"`
}

func (r RecoveryResult) Validate(outcome string) error {
	if !domain.ValidOperation(r.InspectionID) {
		return domain.Fail(domain.Provider, "recovery requires its native inspection receipt")
	}
	if outcome == "reconciled" {
		if r.Blocker != nil {
			return domain.Fail(domain.Provider, "reconciled recovery cannot retain an unresolved blocker")
		}
		return nil
	}
	if !slices.Contains([]string{"paused", "failed"}, outcome) || r.Blocker == nil {
		return domain.Fail(domain.Provider, "blocked recovery requires a concrete observed blocker")
	}
	if !slices.Contains([]string{"input-unavailable", "access-denied", "integrity-mismatch", "unresolved-effect", "tool-failure"}, r.Blocker.Kind) {
		return domain.Fail(domain.Provider, "recovery blocker must identify an input, access, integrity, effect or tool failure, not a task retry flag")
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"recovery blocker reference", r.Blocker.Reference, 4096},
		{"recovery blocker observation", r.Blocker.Observation, 8192},
		{"recovery next action", r.Blocker.NextAction, 4096},
	} {
		if err := domain.CheckText(field.value, field.name, field.limit, true); err != nil {
			return err
		}
	}
	return nil
}
