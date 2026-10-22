package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthenticationRefreshDTOs(t *testing.T) {
	if AuthenticationRequestTTL != int64(15*60) {
		t.Fatal("authentication request TTL must remain fifteen minutes")
	}
	message := Message{Kind: "authentication-refresh", ExpiresAt: 1234, AuthenticationRefresh: &AuthenticationRefresh{Provider: "codex", Reason: "explicit refresh"}}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"expires_at":1234`) || !strings.Contains(string(data), `"authentication_refresh":{"provider":"codex","reason":"explicit refresh"}`) || strings.Contains(string(data), `"project_id"`) {
		t.Fatalf("unexpected authentication control DTO: %s", data)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.AuthenticationRefresh == nil || *decoded.AuthenticationRefresh != *message.AuthenticationRefresh || decoded.ExpiresAt != message.ExpiresAt {
		t.Fatal("authentication control did not round trip", decoded, err)
	}
	data, err = json.Marshal(MessageRequest{Kind: "direct"})
	if err != nil || strings.Contains(string(data), `"expires_at"`) || strings.Contains(string(data), `"authentication_refresh"`) {
		t.Fatalf("ordinary messages gained protected or empty fields: %s (%v)", data, err)
	}
	data, err = json.Marshal(AuthenticationRefreshRequest{Command: Command{OperationID: "refresh-fixture"}, Provider: "copilot", Reason: "explicit refresh"})
	if err != nil || strings.Contains(string(data), `"project_id"`) || !strings.Contains(string(data), `"operation_id":"refresh-fixture"`) {
		t.Fatalf("unexpected refresh request DTO: %s (%v)", data, err)
	}
}
