package domain

import (
	"strings"
	"testing"
)

func TestAgentStatusValidationPublicText(t *testing.T) {
	for _, text := range []string{"", "   ", "  Testing the approved change  ", "Waiting for provider sign-in", "Checking token handling", "Verifying café input", strings.Repeat("x", AgentStatusMaxRunes), strings.Repeat("🧪", AgentStatusMaxRunes)} {
		req := AgentStatusRequest{Command: Command{OperationID: "public-status"}, Text: &text}
		if err := ValidateAgentStatusRequest(req); err != nil {
			t.Fatalf("valid public text rejected: %q: %v", text, err)
		}
		if got, err := NormalizeAgentStatusText(text); err != nil || got != strings.TrimSpace(text) {
			t.Fatalf("text was not normalized: %q: %v", got, err)
		}
	}
	for name, text := range map[string]string{
		"rune limit":            strings.Repeat("é", AgentStatusMaxRunes+1),
		"byte limit":            strings.Repeat("🧪", AgentStatusMaxRunes+1),
		"invalid utf8":          string([]byte{0xff}),
		"nul":                   "Testing\x00output",
		"newline":               "Testing\noutput",
		"trimmed newline":       "Testing\n",
		"tab":                   "Testing\toutput",
		"escape":                "Testing\x1b[31moutput",
		"unicode line":          "Testing\u2028output",
		"unicode paragraph":     "Testing\u2029output",
		"zero width":            "Testing\u200boutput",
		"joiner":                "Testing\u200doutput",
		"bidi":                  "Testing\u202eoutput",
		"bom":                   "\ufeffTesting",
		"grapheme joiner":       "Testing\u034foutput",
		"variation selector":    "Testing\ufe0foutput",
		"private key":           "-----BEGIN OPENSSH PRIVATE KEY-----",
		"encrypted private key": "-----BEGIN ENCRYPTED PRIVATE KEY-----",
		"pgp key":               "-----BEGIN PGP PRIVATE KEY BLOCK-----",
		"token":                 "ghp_" + strings.Repeat("x", 36),
		"fine grained token":    "github_pat_" + strings.Repeat("a", 40),
		"provider key":          "sk-proj-" + strings.Repeat("a", 32),
		"cloud key":             "AKIA1234567890ABCDEF",
		"password assignment":   "password=fixture-not-a-real-password",
		"key assignment":        "api_key: fixture-not-a-real-key",
		"bearer":                "Authorization: Bearer " + strings.Repeat("x", 24),
		"credential url":        "https://fixture:private@example.invalid",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeAgentStatusText(text)
			if err == nil || AsFault(err).Code != Invalid || strings.Contains(err.Error(), text) {
				t.Fatalf("unsafe text accepted or echoed: %v", err)
			}
		})
	}
}

func TestAgentStatusValidationCommandAndRequiredText(t *testing.T) {
	text := "Testing"
	for name, req := range map[string]AgentStatusRequest{
		"missing operation": {Text: &text},
		"invalid operation": {Command: Command{OperationID: "../private"}, Text: &text},
		"missing text":      {Command: Command{OperationID: "public-status"}},
		"positive revision": {Command: Command{OperationID: "public-status", ExpectedRevision: 1}, Text: &text},
		"negative revision": {Command: Command{OperationID: "public-status", ExpectedRevision: -1}, Text: &text},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAgentStatusRequest(req); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("invalid status metadata accepted", err)
			}
		})
	}
}

func TestAgentStatusOptionalHealthActivityValidation(t *testing.T) {
	for _, activity := range []string{"", "onboarding", "scope", "acceptance", "preflight", "execution", "verification", "message", "attention", "research", "idle", "paused", "session", "clarification", "approval", "retry"} {
		h := healthValidationFixture()
		h.Activity = activity
		if err := ValidateWorkerHealth(h); err != nil {
			t.Fatalf("supported optional activity %q rejected: %v", activity, err)
		}
	}
	for _, activity := range []string{"private task title", "raw-error", "/private/workspace", "verification\n"} {
		h := healthValidationFixture()
		h.Activity = activity
		if err := ValidateWorkerHealth(h); err == nil || AsFault(err).Code != Invalid {
			t.Fatal("unallowlisted activity accepted", err)
		}
	}
}
