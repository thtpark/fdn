package domain

import (
	"slices"
	"strings"
	"time"
)

const MaxSharingSeconds int64 = 24 * 60 * 60
const MaxSharingTerminals = 32

type Terminal struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Generation string `json:"generation"`
	Kind       string `json:"kind"`
	CanInput   bool   `json:"can_input"`
	Protected  bool   `json:"protected"`
}

type Sharing struct {
	AgentID           string     `json:"agent_id"`
	Terminals         []Terminal `json:"terminals"`
	TerminalID        string     `json:"terminal_id"`
	ScreenID          string     `json:"screen_id"`
	Protected         bool       `json:"protected"`
	FollowExecution   bool       `json:"follow_execution"`
	Revision          int64      `json:"revision"`
	SelectionRevision int64      `json:"selection_revision"`
	UpdatedAt         int64      `json:"updated_at"`
	ServerObservedAt  int64      `json:"server_observed_at"`
}

type SharingPublishRequest struct {
	Command
	Terminals []Terminal `json:"terminals"`
}

type SharingSelectRequest struct {
	Command
	Mode       string `json:"mode"`
	ResourceID string `json:"resource_id"`
	Protected  bool   `json:"protected"`
	Reason     string `json:"reason"`
}

func ValidateTerminals(terminals []Terminal) error {
	if len(terminals) > MaxSharingTerminals {
		return Fail(Invalid, "terminal inventory exceeds its limit")
	}
	seen := map[string]bool{}
	for _, terminal := range terminals {
		if !ValidScreenID(terminal.ID) || seen[terminal.ID] || !ValidOperation(terminal.Generation) {
			return Fail(Invalid, "terminal inventory requires unique safe IDs and exact native generations")
		}
		if err := CheckText(terminal.Name, "terminal name", 160, true); err != nil {
			return err
		}
		if strings.ContainsAny(terminal.Name, "\r\n\t\x1b") {
			return Fail(Invalid, "terminal names must be nonsecret single-line labels")
		}
		if !slices.Contains([]string{"shell", "execution"}, terminal.Kind) || (terminal.Kind == "execution" && terminal.CanInput) {
			return Fail(Invalid, "terminal kind/input capability is unsupported")
		}
		seen[terminal.ID] = true
	}
	return nil
}

func (s Session) Sharing() bool    { return s.Purpose == "sharing" }
func (s Session) InputLease() bool { return s.Purpose == "sharing-input" }

// Server time is projected outside immutable proposals. Request-start anchoring
// is conservative even when the target's wall clock is skewed.
func (s Session) SharingDeadline(requestStarted time.Time) (time.Time, error) {
	if !s.Sharing() || s.ServerObservedAt <= 0 || s.ExpiresAt <= s.ServerObservedAt {
		return time.Time{}, Fail(Prerequisite, "sharing requires a fresh unexpired server-time observation")
	}
	return requestStarted.Add(time.Duration(s.ExpiresAt-s.ServerObservedAt) * time.Second), nil
}
