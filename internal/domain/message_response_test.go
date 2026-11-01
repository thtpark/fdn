package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageResponseDefaultsAndExplicitOptOut(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{"inferred-direct", `{}`, true},
		{"direct", `{"kind":"direct"}`, true},
		{"request", `{"kind":"request"}`, true},
		{"explicit-yes", `{"kind":"direct","response_requested":true}`, true},
		{"explicit-no", `{"kind":"direct","response_requested":false}`, false},
		{"case-insensitive-explicit-no", `{"kind":"direct","RESPONSE_REQUESTED":false}`, false},
		{"request-explicit-no", `{"kind":"request","response_requested":false}`, false},
		{"authentication", `{"kind":"authentication"}`, false},
		{"permission", `{"kind":"permission"}`, false},
		{"session", `{"kind":"session"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var command MessageCommand
			if err := json.Unmarshal([]byte(test.body), &command); err != nil || command.ResponseRequest != test.want {
				t.Fatal("wire default changed", command.ResponseRequest, err)
			}
			var request MessageRequest
			if err := json.Unmarshal([]byte(test.body), &request); err != nil || request.ResponseRequest != test.want {
				t.Fatal("CLI/request default differs from wire", request.ResponseRequest, err)
			}
		})
	}
}

func TestMessageResponseInputRemainsStrictAndRejectsNull(t *testing.T) {
	for _, data := range []string{
		`{"response_requested":null}`, `{"RESPONSE_REQUESTED":null}`, `{"response_requested":"false"}`,
		`{"response_requested":0}`, `{"unsupported":true}`, `{} {}`,
	} {
		for _, target := range []any{new(MessageCommand), new(MessageRequest)} {
			if err := json.Unmarshal([]byte(data), target); err == nil {
				t.Fatal("invalid message input was silently accepted", data)
			}
		}
	}
}

func TestExplicitAIOptOutAndRetainedFalseProposalsKeepTheirChoice(t *testing.T) {
	reply := MessageRequest{
		RecipientID: "agent_sender", Body: "A generated response.", Origin: "ai",
		Kind: "direct", ReplyToID: "message_original", ResponseRequest: false,
	}
	data, err := json.Marshal(reply)
	if err != nil || !strings.Contains(string(data), `"response_requested":false`) {
		t.Fatal("generated reply lost its explicit opt-out", string(data), err)
	}
	var decoded MessageRequest
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.ResponseRequest || decoded.ReplyToID != reply.ReplyToID {
		t.Fatal("retained exact false proposal was reinterpreted as a new default", decoded, err)
	}
}
