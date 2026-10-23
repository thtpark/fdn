package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMessageReadCLIRequiresExplicitActionAndReplaysReceipt(t *testing.T) {
	message := domain.Message{ID: "message_read_fixture", RecipientID: "agent_fixture", Body: "fixture message", State: "pending", Revision: 1}
	posts := 0
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/message_read_fixture/read" || r.Method != http.MethodPost {
			t.Errorf("unexpected read action: %s %s", r.Method, r.URL.Path)
		}
		var req domain.MessageReadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OperationID != "explicit-read-fixture" || req.ExpectedRevision != 0 {
			t.Error("unexpected explicit read body", req, err)
		}
		posts++
		message.ReadAt = 1800000000
		diagnosticsEnvelope(t, w, message, nil)
	})
	for range 2 {
		x, code := invoke(t, "agent", []string{"--config", file, "--operation-id", "explicit-read-fixture", "message", "read", message.ID}, nil)
		var got domain.Message
		if err := json.Unmarshal(x.Data, &got); code != 0 || !x.OK || err != nil || got.ReadAt != 1800000000 || got.State != "pending" || got.Revision != 1 {
			t.Fatal("explicit read failed or changed disposition", x, code, got, err)
		}
	}
	if posts != 2 {
		t.Fatal("explicit replay did not reconcile with authoritative receipt", posts, requests())
	}
	var bodies [][]byte
	for _, request := range requests() {
		if request.method == http.MethodPost {
			bodies = append(bodies, request.body)
		}
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("explicit read replay changed the original operation", bodies)
	}
}

func TestMessagePeekCommandsNeverSendReadReceipt(t *testing.T) {
	for _, leaf := range []string{"show", "diagnose", "inbox"} {
		t.Run(leaf, func(t *testing.T) {
			_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("peek marked read: %s %s", r.Method, r.URL.Path)
				}
				switch leaf {
				case "show":
					diagnosticsEnvelope(t, w, domain.Message{ID: "message_peek", Body: "fixture"}, nil)
				case "diagnose":
					diagnosticsEnvelope(t, w, domain.MessageDiagnostics{MessageID: "message_peek"}, nil)
				default:
					diagnosticsEnvelope(t, w, domain.Page[domain.Message]{Items: []domain.Message{{ID: "message_peek"}}}, nil)
				}
			})
			args := []string{"--config", file, "message", leaf}
			if leaf != "inbox" {
				args = append(args, "message_peek")
			}
			if x, code := invoke(t, "agent", args, nil); code != 0 || !x.OK {
				t.Fatal(x, code)
			}
			for _, request := range requests() {
				if request.method != http.MethodGet {
					t.Fatal("passive read path wrote a receipt", request)
				}
			}
		})
	}
}

func TestMessageReadCLIRejectsTimestampAndExtraFlags(t *testing.T) {
	for _, tail := range [][]string{{}, {"message_fixture", "--at", "1800000000"}, {"message_fixture", "--input", "unused.json"}, {"message_fixture", "extra"}} {
		_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("invalid explicit read reached server", r.Method, r.URL.Path)
			diagnosticsEnvelope(t, w, nil, nil)
		})
		args := append([]string{"--config", file, "message", "read"}, tail...)
		if x, code := invoke(t, "agent", args, nil); code != 2 || x.OK {
			t.Fatal("unsupported read input accepted", tail, x, code)
		}
		for _, request := range requests() {
			if request.method != http.MethodGet {
				t.Fatal("invalid read wrote state", request)
			}
		}
	}
}
