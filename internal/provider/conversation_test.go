package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConversationActionsRequireMeaningfulPublicTaskMetadata(t *testing.T) {
	action := &ConversationAction{Instructions: "Private execution details stay in the local journal."}
	reply := Reply{Outcome: "responded", Summary: "Plan the requested inspection.", Action: action}
	if err := reply.Validate(); err == nil {
		t.Fatal("standalone action omitted its public title and scope")
	}
	action.Title, action.Description, action.AcceptanceCriteria = "Inspect the dependency report",
		"Compare the generated report with the retained inventory without changing packages.",
		"Check all entries and report actual discrepancies."
	if err := reply.Validate(); err != nil {
		t.Fatal("complete public task metadata was rejected", err)
	}
	action.Title = strings.Repeat("x", 513)
	if err := reply.Validate(); err == nil {
		t.Fatal("oversized public task title was accepted")
	}
	action.Title, action.Description, action.AcceptanceCriteria = "", "", ""
	action.TaskID = "task_existing"
	if err := reply.Validate(); err != nil {
		t.Fatal("same-task continuation incorrectly needed replacement metadata", err)
	}
}

func TestPublicTaskReportHasExactSummaryAndEvidenceBounds(t *testing.T) {
	report := PublicReport{Summary: strings.Repeat("s", 32768), Evidence: []string{strings.Repeat("e", 8192)}}
	if err := report.Validate(); err != nil {
		t.Fatal("valid public report boundary was rejected", err)
	}
	report.Summary += "s"
	if err := report.Validate(); err == nil {
		t.Fatal("oversized public summary was accepted")
	}
	report.Summary, report.Evidence[0] = "Checked result.", strings.Repeat("e", 8193)
	if err := report.Validate(); err == nil {
		t.Fatal("oversized public evidence was accepted")
	}
}

func TestAIResponseRequestsDefaultTrueAndAllowExplicitOptOut(t *testing.T) {
	for _, test := range []struct {
		input string
		want  bool
	}{
		{`{"outcome":"responded","summary":"A grounded reply"}`, true},
		{`{"outcome":"responded","summary":"Please continue","response_requested":true}`, true},
		{`{"outcome":"responded","summary":"No further response needed","response_requested":false}`, false},
	} {
		var reply Reply
		if err := json.Unmarshal([]byte(test.input), &reply); err != nil || reply.RequestsResponse() != test.want {
			t.Fatal("AI response continuation choice changed", reply, err)
		}

		encoded, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		var retained Reply
		if err := json.Unmarshal(encoded, &retained); err != nil || retained.RequestsResponse() != test.want {
			t.Fatal("retained model reply lost response choice", retained, err)
		}
	}
}
