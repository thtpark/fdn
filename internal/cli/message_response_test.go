package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
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
		{"desktop-reply-empty-attachments", map[string]any{
			"origin": "human", "kind": "direct", "urgent": false, "response_requested": true,
			"reply_to_id": "message_parent", "attachment_ids": []string{}}, true, "direct", 0},
		{"desktop-reply-attached", map[string]any{
			"origin": "human", "kind": "direct", "urgent": false, "response_requested": false,
			"reply_to_id": "message_parent", "attachment_ids": []string{"file_fixture"}}, false, "direct", 0},
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
				if ids, present := test.input["attachment_ids"].([]string); present &&
					(!slices.Equal(request.AttachmentIDs, ids) || request.ReplyToID != "message_parent") {
					t.Error("desktop reply lost its exact attachment IDs or reply link", request)
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

func TestAttachedReplyCLIRetainsExactProposalAfterLostResponse(t *testing.T) {
	var bodies [][]byte
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request domain.MessageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		data, err := json.Marshal(request)
		if err != nil {
			t.Error(err)
		}
		bodies = append(bodies, data)
		if len(bodies) == 1 {
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Unavailable, "fixture lost reply acknowledgement"))
			return
		}
		diagnosticsEnvelope(t, w, domain.Message{ID: "message_reply_fixture", ReplyToID: request.ReplyToID}, nil)
	})
	input := map[string]any{"recipient_id": "agent_fixture", "body": "A reviewed reply.", "origin": "human",
		"kind": "direct", "response_requested": false, "reply_to_id": "message_parent", "attachment_ids": []string{"file_fixture"}}
	args := []string{"--config", file, "--operation-id", "attached-reply-replay", "message", "send", "--input", "-"}
	if response, code := invoke(t, "agent", args, input); code != 7 || response.OK || len(bodies) != 1 {
		t.Fatal("uncertain attached reply was retried or lost before transport", response, code)
	}
	if response, code := invoke(t, "agent", args, input); code != 0 || !response.OK || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("attached reply changed on exact operation replay", response, code)
	}
}
