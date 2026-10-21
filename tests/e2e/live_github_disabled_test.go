//go:build e2e && !e2e_live

package e2e_test

import "testing"

// Keep inherited opt-in environment variables inert in the ordinary e2e suite.
func TestOptInLiveGitHubReviewMerge(t *testing.T) {
	t.Skip("live code excluded: explicitly build with -tags=e2e,e2e_live and supply the repository/environment opt-in")
}
