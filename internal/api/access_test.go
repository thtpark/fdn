package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/testidentity"
)

func TestAccessAuditCorrelatesActorWithoutLoggingRequestSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "foundation", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor, err := testidentity.Register(ctx, store, "", "audit-fixture", 4100)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	service, err := NewAuthenticated(store, logger, func(r *http.Request) (domain.Agent, error) { return store.Me(r.Context(), actor.ID) })
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	secret := "secret-material-not-for-logs"
	var ids []string
	for _, path := range []string{"/v1/me?token=" + secret, "/v1/unknown/" + secret} {
		request := httptest.NewRequest(http.MethodGet, "https://localhost"+path, nil)
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Set("X-Fdn-Request-ID", secret)
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		id := response.Header().Get("X-Fdn-Request-ID")
		if !strings.HasPrefix(id, "request_") {
			t.Fatal("server did not supply its own request ID")
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Fatal("independent requests reused audit correlation")
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("request content entered audit: %s", output.String())
	}
	decoder := json.NewDecoder(&output)
	for index := range 2 {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event["request_id"] != ids[index] || event["actor_id"] != actor.ID {
			t.Fatalf("audit cannot identify request actor: %+v", event)
		}
		if index == 0 && event["route"] != "GET /v1/me" {
			t.Fatalf("audit should record route pattern, not raw URL: %+v", event)
		}
	}
}
