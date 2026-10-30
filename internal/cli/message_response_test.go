package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMessageSendDefaultsResponseButKeepsExplicitOptOut(t *testing.T) {
	for _, test := range []struct {
		name     string
		input    map[string]any
		want     bool
		wantKind string
		wantCode int
	}{
		{"omitted", map[string]any{}, true, "direct", 0},
		{"disabled", map[string]any{"response_requested": false}, false, "direct", 0},
		{"inferred-request", map[string]any{"request": map[string]any{"kind": "work"}}, true, "request", 0},
		{"inferred-request-disabled", map[string]any{"request": map[string]any{"kind": "work"}, "response_requested": false}, false, "request", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			sent := false
			_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
					t.Error("unexpected send route", r.Method, r.URL.Path)
				}
				var request domain.MessageRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				sent = true
				if request.ResponseRequest != test.want || request.Kind != test.wantKind {
					t.Error("CLI overwrote the response choice", request)
				}
				if test.wantCode != 0 {
					diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "operational requests require a response request"))
					return
				}
				diagnosticsEnvelope(t, w, domain.Message{ID: "message_fixture", ResponseRequest: request.ResponseRequest}, nil)
			})
			test.input["recipient_id"], test.input["body"] = "agent_fixture", "A deliberate fixture message."
			envelope, code := invoke(t, "agent", []string{"--config", file, "message", "send", "--input", "-"}, test.input)
			if code != test.wantCode || envelope.OK != (test.wantCode == 0) || !sent {
				t.Fatal("message send failed", envelope, code, sent)
			}
		})
	}
}
