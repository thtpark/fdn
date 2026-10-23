package domain

import (
	"regexp"
	"slices"
)

const MaxHealthProblems = 9
const MaxHealthPendingReports = 1024
const maxHealthTimestamp int64 = 253402300799
const maxHealthDeadlineSeconds int64 = 7 * 24 * 60 * 60

var healthVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$`)
var healthModelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/+-]{0,255}$`)

// ValidHealthVersion accepts version data, never executable paths or CLI output.
func ValidHealthVersion(version string) bool {
	return version == "" || version == "unknown" || version == "dev" || healthVersionPattern.MatchString(version)
}

func ValidateWorkerHealth(h WorkerHealth) error {
	if h.Version != WorkerHealthVersion || h.Sequence < 0 || h.ObservedAt <= 0 || h.ObservedAt > maxHealthTimestamp {
		return Fail(Invalid, "health requires a supported version, nonnegative sequence and positive observation time")
	}
	if h.Sequence == 0 && (h.Phase != "unknown" || h.ActionKind != "" || h.ActionID != "" || h.TaskID != "" || h.JournalID != "" || h.DeadlineAt != 0) {
		return Fail(Invalid, "health sequence zero is reserved for pre-controller diagnostic observations")
	}
	if !slices.Contains([]string{"ai", "manual"}, h.Mode) || (h.Provider != "" && !ValidProvider(h.Provider)) || (h.PreferredProvider != "" && !ValidProvider(h.PreferredProvider)) || !ValidHealthVersion(h.ProviderVersion) || (h.Model != "" && !healthModelPattern.MatchString(h.Model)) {
		return Fail(Invalid, "health mode, provider, model or safe version data is unsupported")
	}
	if !slices.Contains([]string{"unknown", "authenticated", "login-required", "credentials-present-unverified", "signed-in", "sign-in-required"}, h.Authentication) {
		return Fail(Invalid, "health authentication observation is unsupported")
	}
	if !slices.Contains([]string{"unknown", "checking", "reconciling", "provider-check", "preparing", "invoking", "applying", "reporting", "idle", "paused", "blocked", "session"}, h.Phase) {
		return Fail(Invalid, "health phase is unsupported")
	}
	if !slices.Contains([]string{"", "onboarding", "message", "attention", "task", "research"}, h.ActionKind) || !slices.Contains([]string{"", "onboarding", "message", "attention", "task", "research"}, h.LastActionKind) {
		return Fail(Invalid, "health action kind is unsupported")
	}
	if !slices.Contains([]string{"", "configuration", "identity-unavailable", "transport-unavailable", "authentication-required", "authentication-unknown", "provider-unavailable", "provider-permission", "provider-model-unavailable", "founder-paused", "maintenance", "session-fence", "session-active", "invocation-uncertain", "unreconciled-claim", "workspace-permission", "approval-required", "retry-wait", "report-pending", "slot-occupied", "work-prerequisite", "controller-error"}, h.Blocker) {
		return Fail(Invalid, "health blocker is unsupported")
	}
	for _, id := range []string{h.ActionID, h.TaskID, h.JournalID, h.LastActionID} {
		if id != "" && !ValidOperation(id) {
			return Fail(Invalid, "health references must be bounded safe identifiers")
		}
	}
	if (h.ActionKind == "" && h.ActionID != "") || (h.LastActionKind == "" && h.LastActionID != "") {
		return Fail(Invalid, "health action references require an action kind")
	}
	for _, action := range []struct{ kind, id string }{{h.ActionKind, h.ActionID}, {h.LastActionKind, h.LastActionID}} {
		if slices.Contains([]string{"message", "attention", "task"}, action.kind) && action.id == "" {
			return Fail(Invalid, "health work actions require a reference")
		}
	}
	if h.PendingReports < 0 || h.PendingReports > MaxHealthPendingReports || len(h.Problems) > MaxHealthProblems {
		return Fail(Invalid, "health exceeds the pending-report or problem budget")
	}
	if h.PhaseStartedAt < 0 || (h.PhaseStartedAt == 0 && h.Sequence != 0) || h.PhaseStartedAt > h.ObservedAt {
		return Fail(Invalid, "health phase start must precede its observation")
	}
	for _, at := range []int64{h.AuthenticationObservedAt, h.LastProgressAt, h.StepStartedAt, h.StepFinishedAt, h.LastActionAt, h.BlockedSinceAt} {
		if at < 0 || at > h.ObservedAt {
			return Fail(Invalid, "health clocks must precede the observation")
		}
	}
	if h.DeadlineAt < 0 || h.DeadlineAt > maxHealthTimestamp || (h.DeadlineAt != 0 && h.DeadlineAt-h.ObservedAt > maxHealthDeadlineSeconds) {
		return Fail(Invalid, "health deadline is outside its bounded observation")
	}
	seen := map[string]bool{}
	for _, p := range h.Problems {
		if !slices.Contains([]string{"configuration", "mode-control", "provider-update", "controller", "watch", "heartbeat", "connection", "session", "control"}, p.Component) || seen[p.Component] {
			return Fail(Invalid, "health problem components must be supported and unique")
		}
		seen[p.Component] = true
		if !slices.Contains([]Code{Invalid, NotFound, Denied, Conflict, Prerequisite, Unavailable, Provider, Internal, Cancelled}, p.Code) {
			return Fail(Invalid, "health problem codes must be supported safe codes")
		}
		if p.FirstSeenAt <= 0 || p.LastSeenAt < p.FirstSeenAt || p.LastSeenAt > h.ObservedAt || p.ResolvedAt < 0 || p.ResolvedAt > h.ObservedAt || (p.ResolvedAt != 0 && p.ResolvedAt < p.LastSeenAt) {
			return Fail(Invalid, "health problem clocks are inconsistent")
		}
	}
	return nil
}

// HealthAge anchors a worker-relative age at server receipt, tolerating clock skew.
// A missing worker clock has unknown age (-1).
func HealthAge(observedAt, eventAt, receivedAt, now int64) int64 {
	if eventAt <= 0 {
		return -1
	}
	return max(0, observedAt-eventAt) + max(0, now-receivedAt)
}

func HealthDeadlineRemaining(h WorkerHealth, receivedAt, now int64) int64 {
	return h.DeadlineAt - h.ObservedAt - max(0, now-receivedAt)
}
