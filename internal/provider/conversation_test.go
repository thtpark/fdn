package provider

import (
	"encoding/json"
	"testing"
)

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
