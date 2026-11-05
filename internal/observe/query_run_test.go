package observe

import (
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestRunObservationUsesExactSharedSourceWithoutPolling(t *testing.T) {
	query := Query{Kind: "run", ID: "run_fixture"}
	if err := query.Validate(); err != nil {
		t.Fatal(err)
	}
	private, public, err := Filters([]Query{query}, "agent_fixture")
	if err != nil || len(public.Sources) != 1 || public.Sources[0] != query.ID || len(private.Sources) != 0 {
		t.Fatal("run query lost its exact shared source", private, public, err)
	}
	if !query.Matches(domain.Update{Topic: "run", SourceID: query.ID}, "agent_fixture") ||
		query.Matches(domain.Update{Topic: "run", SourceID: "run_other"}, "agent_fixture") ||
		query.Matches(domain.Update{Topic: "message", SourceID: query.ID}, "agent_fixture") {
		t.Fatal("run observation crossed its exact source scope")
	}
	if query.SampleInterval() != 0 {
		t.Fatal("run observation started a polling timer")
	}
}
