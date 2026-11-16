package software

import (
	"strings"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
)

// ValidateRecipe checks a source-bound build description. It never executes a
// step; builder admission, tool isolation and authority belong to the caller.
func ValidateRecipe(recipe domain.BuildRecipe) error {
	if recipe.FormatVersion != 1 || !versionPattern.MatchString(recipe.Version) || !validPlatform(recipe.Platform) ||
		!validProfile(recipe.Profile) || recipe.Profile == "debian" && !strings.HasPrefix(recipe.Platform, "linux-") ||
		recipe.TimeoutSeconds < 1 || recipe.TimeoutSeconds > 3600 ||
		len(recipe.Steps) == 0 || len(recipe.Steps) > 64 || len(recipe.Outputs) == 0 || len(recipe.Outputs) > MaxPayloads {
		return domain.Fail(domain.Invalid, "software recipe requires a versioned native profile, bounded build steps, timeout and outputs")
	}
	total := 0
	for _, step := range recipe.Steps {
		if len(step.Args) == 0 || len(step.Args) > 64 || strings.TrimSpace(step.Args[0]) == "" {
			return domain.Fail(domain.Invalid, "software build steps require an explicit bounded argument vector")
		}
		for _, arg := range step.Args {
			if len(arg) > 4096 || !utf8.ValidString(arg) || strings.ContainsAny(arg, "\x00\r\n") {
				return domain.Fail(domain.Invalid, "software build arguments exceed their single-line byte bounds")
			}
			total += len(arg)
		}
	}
	if total > 64<<10 {
		return domain.Fail(domain.Invalid, "software recipe exceeds its argument byte limit")
	}
	names := newNames()
	for _, output := range recipe.Outputs {
		if err := names.add(output.Path); err != nil {
			return err
		}
		if !validMode(output.Mode) {
			return domain.Fail(domain.Invalid, "software build outputs require explicit safe regular-file modes")
		}
	}
	return validateDependencies(recipe.Dependencies, recipe.Platform)
}
