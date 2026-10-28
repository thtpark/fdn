package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentResetCLIQueuesExactRequestWithoutInference(t *testing.T) {
	target := domain.NewID("agent")
	req := domain.AgentResetRequest{Command: domain.Command{OperationID: "reset-cli-exact"}, Reason: "Founder explicitly resets owned runtime only"}
	operation := domain.AgentOperation{Version: domain.AgentOperationVersion, ID: "agent_operation_reset", Kind: "runtime-reset", TargetID: target, State: "queued"}
	attempts := 0
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != api.Path("agents", target, "reset") {
			t.Error("reset used a different command boundary", r.Method, r.URL.Path)
		}
		var submitted domain.AgentResetRequest
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil || submitted != req {
			t.Error("reset changed the exact reason/operation", submitted, err)
		}
		attempts++
		if attempts == 1 {
			diagnosticsEnvelope(t, w, nil, domain.AsFault(domain.Fail(domain.Unavailable, "lost accepted reset acknowledgement")))
			return
		}
		diagnosticsEnvelope(t, w, operation, nil)
	})
	input := filepath.Join(filepath.Dir(file), "reset.json")
	if err := config.Write(input, req); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", file, "agent", "reset", target, "--input", input}
	first, code := invoke(t, "agent", args, nil)
	if first.OK || code != 7 {
		t.Fatal("uncertain submission was hidden", first, code)
	}
	second, code := invoke(t, "agent", args, nil)
	var got domain.AgentOperation
	if !second.OK || code != 0 || json.Unmarshal(second.Data, &got) != nil || got.State != "queued" || got.Result != nil {
		t.Fatal("queued reset was mistaken for a completed action", second, code)
	}
	recorded := requests()
	if len(recorded) != 4 || !bytes.Equal(recorded[1].body, recorded[3].body) {
		t.Fatal("reset did not replay the exact durable CLI proposal", recorded)
	}
	for _, body := range []string{
		`{"reason":"Runtime reset","force":true}`,
		`{"reason":"Runtime reset","acknowledge_stopped":true}`,
		`{"reason":"Runtime reset","erase_identity":true}`,
		`{"reason":"Runtime reset","mode":"ai"}`,
		`{"reason":""}`,
	} {
		failed, code := invoke(t, "agent", []string{"--config", file, "agent", "reset", target, "--input", "-"}, json.RawMessage(body))
		if failed.OK || code == 0 {
			t.Fatal("reset accepted unsafe or empty input", body, failed, code)
		}
	}
	for _, tail := range [][]string{{}, {target}, {target, "--force"}, {target, "--wait"}, {target, "--restart"}} {
		failed, code := invoke(t, "agent", append([]string{"--config", file, "agent", "reset"}, tail...), nil)
		if failed.OK || code == 0 {
			t.Fatal("reset accepted an incomplete or unsupported command", tail, failed, code)
		}
	}
}
