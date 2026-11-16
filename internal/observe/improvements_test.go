package observe

import (
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestImprovementObservationKeepsRepositoryAndPublicIntakeSeparate(t *testing.T) {
	queries := []Query{{Kind: "improvements", Project: domain.FoundationProjectID}, {Kind: "repository", ID: "repository_fixture"},
		{Kind: "products"}, {Kind: "build", ID: "build_fixture"}, {Kind: "release", ID: "release_fixture"}}
	private, public, err := Filters(queries, "agent_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !public.Matches(domain.Update{Topic: "improvement"}, "agent_fixture") ||
		public.Matches(domain.Update{Topic: "repository"}, "agent_fixture") ||
		!private.Matches(domain.Update{Topic: "repository"}, "agent_fixture") {
		t.Fatal("private hosted-source invalidation entered public intake", private, public)
	}
	if !queries[1].Matches(domain.Update{Topic: "repository", SourceID: "repository_fixture"}, "agent_fixture") ||
		queries[1].Matches(domain.Update{Topic: "repository", SourceID: "repository_other"}, "agent_fixture") {
		t.Fatal("repository observation was not exact-source scoped")
	}
}
