package observe

import (
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestEntryObservationUsesExactSharedSourceWithoutPolling(t *testing.T) {
	query := Query{Kind: "entry", ID: "entry_original"}
	if err := query.Validate(); err != nil {
		t.Fatal(err)
	}
	private, public, err := Filters([]Query{query}, "agent_fixture")
	if err != nil || len(public.Sources) != 1 || public.Sources[0] != query.ID || len(private.Sources) != 0 {
		t.Fatal("entry query lost its exact shared source", private, public, err)
	}
	for _, topic := range []string{"comment", "worklog"} {
		if !query.Matches(domain.Update{Topic: topic, SourceID: query.ID}, "agent_fixture") ||
			query.Matches(domain.Update{Topic: topic, SourceID: "entry_other"}, "agent_fixture") {
			t.Fatal("entry observation crossed its source scope", topic)
		}
	}
	if query.SampleInterval() != 0 {
		t.Fatal("entry observation started a polling timer")
	}
}
