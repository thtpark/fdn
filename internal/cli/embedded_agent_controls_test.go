package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestEmbeddedProtectedAgentControlsUseExactInMemoryRequestsWithoutNativeExecution(t *testing.T) {
	ctx := context.Background()
	worker, _, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("protected control did not submit a typed POST", r.Method)
		}
		diagnosticsEnvelope(t, w, domain.AgentOperation{Version: 1, ID: "agent_operation_fixture", State: "queued"}, nil)
	})
	state, err := local.Open(ctx, worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	client, err := api.NewLocalClient(worker)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := config.Read(worker.CredentialFile, &client.Credential); err != nil {
		t.Fatal(err)
	}
	embedded, err := NewEmbedded(worker, state, client, func() any { return map[string]bool{"ready": true} })
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		args       []string
		body, path string
	}{
		{"diagnostics", []string{"agent", "diagnose", "agent_target", "--refresh"},
			`{"reason":"Founder requested fresh diagnostics"}`, "/v1/agents/agent_target/diagnostics"},
		{"reset", []string{"agent", "reset", "agent_target", "--input", "-"},
			`{"reason":"Reset only the existing runtime"}`, "/v1/agents/agent_target/reset"},
		{"restart", []string{"agent", "restart", "agent_target", "--input", "-"},
			`{"reason":"Restore only the fixed native services"}`, "/v1/agents/agent_target/restart"},
		{"repair", []string{"agent", "repair", "agent_target", "--input", "-"},
			`{"reason":"Recover the exact proven session","session_id":"session_fixture","expected_fence":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "/v1/agents/agent_target/repairs"},
	} {
		data, err := embedded.Mutate(ctx, test.args, json.RawMessage(test.body), "embedded-control-"+test.name, 0)
		if err != nil {
			t.Fatal(test.name, err)
		}
		var result domain.AgentOperation
		if err := json.Unmarshal(data, &result); err != nil || result.State != "queued" || result.Result != nil {
			t.Fatal(result, err)
		}
		last := requests()[len(requests())-1]
		if last.path != test.path || last.method != http.MethodPost {
			t.Fatal("protected control changed its exact boundary", last)
		}
		var command domain.Command
		if err := json.Unmarshal(last.body, &command); err != nil || command.OperationID != "embedded-control-"+test.name || command.ExpectedRevision != 0 {
			t.Fatal("protected control lost exact replay metadata", command, err)
		}
	}
	for _, bad := range []struct {
		args []string
		body string
	}{
		{[]string{"agent", "diagnose", "agent_target", "--refresh"}, `{"reason":"Different source"}`},
		{[]string{"agent", "diagnose", "agent_target", "--refresh"}, `{"reason":"Founder requested fresh diagnostics","force":true}`},
		{[]string{"agent", "reset", "agent_target", "--input", "/private/file"}, `{"reason":"No paths"}`},
		{[]string{"agent-service", "restart-helper", "--policy", "/etc/fdn/updater.json"}, `{}`},
	} {
		if _, err := embedded.Mutate(ctx, bad.args, json.RawMessage(bad.body), "embedded-invalid", 0); err == nil {
			t.Fatal("mobile control admitted arbitrary paths, scope or native execution", bad)
		}
	}
}
