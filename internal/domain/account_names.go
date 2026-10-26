package domain

import (
	"regexp"
	"strings"
)

var (
	accountUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	accountNameSeparators  = regexp.MustCompile(`[^a-z0-9_-]+`)
)

func ValidAccountUsername(value string) bool {
	return value != "root" && accountUsernamePattern.MatchString(value)
}

func AgentUsername(name string) (string, error) {
	if err := CheckText(name, "agent name", 128, true); err != nil {
		return "", err
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", Fail(Invalid, "agent name must be a single line")
	}
	value := strings.Trim(accountNameSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if !ValidAccountUsername(value) {
		return "", Fail(Invalid, "agent name must produce a non-root OS username matching [a-z_][a-z0-9_-]{0,31}")
	}
	return value, nil
}
