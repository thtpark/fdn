package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeMessageResponseDefaultsAndExplicitFalse(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	client := messageFixtureClient(t, f.server, f.founder.ID)
	for _, test := range []struct {
		name string
		set  *bool
		want bool
	}{{"omitted", nil, true}, {"enabled", boolPointer(true), true}, {"disabled", boolPointer(false), false}} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{
				"operation_id": domain.NewID("op"), "recipient_id": f.target.ID,
				"body": "Native response default fixture.", "origin": "human", "kind": "direct",
			}
			if test.set != nil {
				body["response_requested"] = *test.set
			}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			sent, err := Call[domain.Message](context.Background(), client, "POST", "/v1/messages", json.RawMessage(data))
			if err != nil || sent.ResponseRequest != test.want || sent.ReadAt != 0 {
				t.Fatal("native response default or read semantics changed", sent, err)
			}
			replay, err := Call[domain.Message](context.Background(), client, "POST", "/v1/messages", json.RawMessage(data))
			if err != nil || replay.ID != sent.ID || replay.ResponseRequest != test.want {
				t.Fatal("exact replay changed response intent", replay, err)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestNativeMessageSendRejectsSelf(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	for _, actor := range []domain.Agent{f.founder, f.target} {
		client := messageFixtureClient(t, f.server, actor.ID)
		_, err := Call[domain.Message](context.Background(), client, "POST", "/v1/messages", domain.MessageRequest{
			Command: domain.Command{OperationID: domain.NewID("op")}, RecipientID: actor.ID,
			Body: "This must not be delivered.", Origin: "human", Kind: "direct",
		})
		if domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("native transport admitted a self message", actor.ID, err)
		}
	}
}
