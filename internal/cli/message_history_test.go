package cli

import (
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMessageHistoryCLIPreservesPagingAndLegacyCommands(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("cursor") != "opaque" {
			t.Error("history lost its explicit page", r.Method, r.URL.String())
		}
		switch r.URL.Path {
		case "/v1/message-history":
			if r.URL.Query().Get("folder") != "sent" || r.URL.Query().Get("state") != "all" {
				t.Error("history lost folder/disposition", r.URL.String())
			}
		case "/v1/messages/message_fixture/thread-page":
		default:
			t.Error("unexpected history route", r.URL.String())
		}
		diagnosticsEnvelope(t, w, domain.Page[domain.Message]{Items: []domain.Message{}, NextCursor: "next"}, nil)
	})
	for _, args := range [][]string{
		{"message", "list", "--folder", "sent", "--limit", "2", "--cursor", "opaque"},
		{"message", "thread-page", "message_fixture", "--limit", "2", "--cursor", "opaque"},
	} {
		envelope, code := invoke(t, "agent", append([]string{"--config", file}, args...), nil)
		if code != 0 || !envelope.OK {
			t.Fatal("history CLI failed", envelope, code)
		}
	}
	for _, request := range requests() {
		if request.method != http.MethodGet || len(request.body) != 0 {
			t.Fatal("browsing history made a mutation", request)
		}
	}
}
