package domain

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var statusCredentialPattern = regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*(?:PRIVATE|SECRET) KEY(?: BLOCK)?-----|\b(?:gh[pousr]_[a-z0-9_]{20,}|github_pat_[a-z0-9_]{20,}|sk-(?:proj-)?[a-z0-9_-]{20,}|(?:AKIA|ASIA)[A-Z0-9]{16})\b|\bbearer\s+[a-z0-9._~+/-]{16,}=*|\b(?:password|passwd|access[_ -]?token|refresh[_ -]?token|api[_ -]?key|client[_ -]?secret|secret|token|cookie)\s*[:=]\s*\S+|\b(?:https?|ssh)://[^/\s@:]+:[^/\s@]+@|\beyJ[a-z0-9_-]{8,}\.eyJ[a-z0-9_-]{8,}\.[a-z0-9_-]{12,}`)

// NormalizeAgentStatusText validates deliberate public text, not arbitrary private output.
// Credential-shape rejection is a guardrail, not a guarantee of secret detection.
func NormalizeAgentStatusText(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", Fail(Invalid, "agent status must be valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) {
			return "", Fail(Invalid, "agent status must be a single visible line without control or invisible formatting characters")
		}
	}
	text = strings.TrimSpace(text)
	if len(text) > AgentStatusMaxRunes*4 || utf8.RuneCountInString(text) > AgentStatusMaxRunes {
		return "", Fail(Invalid, "agent status exceeds 160 Unicode codepoints or 640 bytes")
	}
	if statusCredentialPattern.MatchString(text) {
		return "", Fail(Invalid, "agent status must not contain credential or private-key forms")
	}
	return text, nil
}

func ValidateAgentStatusRequest(req AgentStatusRequest) error {
	if !ValidOperation(req.OperationID) || req.ExpectedRevision != 0 {
		return Fail(Invalid, "agent status requires operation_id and no expected_revision")
	}
	if req.Text == nil {
		return Fail(Invalid, "agent status requires text; an empty string clears the authored note")
	}
	_, err := NormalizeAgentStatusText(*req.Text)
	return err
}
