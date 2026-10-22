package domain

import (
	"strings"
	"unicode"
)

const MaxScreens = 16
const MaxScreenApplications = 32

func ValidScreenID(id string) bool { return len(id) <= 64 && ValidOperation(id) }

// ValidateScreens accepts public capabilities, not native display names or paths.
func ValidateScreens(screens []Screen) error {
	if len(screens) > MaxScreens {
		return Fail(Invalid, "at most 16 screens may be published")
	}
	seen := make(map[string]bool, len(screens))
	primary := false
	for _, screen := range screens {
		if !ValidScreenID(screen.ID) || seen[screen.ID] {
			return Fail(Invalid, "screen IDs must be unique safe identifiers of at most 64 bytes")
		}
		seen[screen.ID] = true
		if err := CheckText(screen.Name, "public screen label", 128, true); err != nil {
			return err
		}
		if screen.Name != strings.TrimSpace(screen.Name) {
			return Fail(Invalid, "screen labels must not have surrounding whitespace")
		}
		for _, r := range screen.Name {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && !strings.ContainsRune(" ._()-", r) {
				return Fail(Invalid, "screen labels may contain only letters, numbers, spaces, dots, underscores, parentheses and hyphens")
			}
		}
		if screen.Scope != "screen" && screen.Scope != "desktop" {
			return Fail(Invalid, "screen scope must be screen or desktop")
		}
		if screen.Width < 1 || screen.Width > 32768 || screen.Height < 1 || screen.Height > 32768 {
			return Fail(Invalid, "screen dimensions must be between 1 and 32768 pixels")
		}
		if screen.Primary && primary {
			return Fail(Invalid, "only one published screen may be primary")
		}
		primary = primary || screen.Primary
		if len(screen.Applications) > MaxScreenApplications {
			return Fail(Invalid, "at most 32 public application IDs may be published per screen")
		}
		applications := make(map[string]bool, len(screen.Applications))
		for _, id := range screen.Applications {
			if !ValidScreenID(id) || applications[id] {
				return Fail(Invalid, "application IDs must be unique safe identifiers of at most 64 bytes per screen")
			}
			applications[id] = true
		}
	}
	return nil
}
