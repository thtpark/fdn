package cli

import (
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestTaskRunCLIReadsOneExactRunWithoutMutation(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run_fixture" || r.URL.RawQuery != "" {
			t.Error("unexpected run read", r.Method, r.URL.String())
		}
		diagnosticsEnvelope(t, w, domain.Run{
			ID: "run_fixture", TaskID: "task_fixture", Kind: "verification", AgentID: "agent_fixture",
			State: "completed", Attempt: 1, Summary: "Checked the actual result.", Evidence: []string{},
		}, nil)
	})
	envelope, code := invoke(t, "agent", []string{"--config", file, "task", "run", "run_fixture"}, nil)
	if code != 0 || !envelope.OK {
		t.Fatal("task run failed", envelope, code)
	}
	for _, args := range [][]string{{"task", "run"}, {"task", "run", "run_fixture", "--cursor", "unexpected"}} {
		if _, code := invoke(t, "agent", append([]string{"--config", file}, args...), nil); code != 2 {
			t.Fatal("invalid run arguments were accepted", args, code)
		}
	}
	runReads := 0
	for _, request := range requests() {
		if request.method != http.MethodGet || len(request.body) != 0 {
			t.Fatal("run read performed a mutation", request)
		}
		if request.path == "/v1/runs/run_fixture" {
			runReads++
		}
	}
	if runReads != 1 {
		t.Fatal("invalid arguments performed a run lookup", runReads)
	}
}
