package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentPreferencesCLIShowsAndPreservesExactLostResponseProposal(t *testing.T) {
	var bodies [][]byte
	reads := 0
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/v1/agents/agent_target/preferences" {
			t.Error("unexpected preference path", r.URL.RequestURI())
		}
		if r.Method == http.MethodGet {
			reads++
			diagnosticsEnvelope(t, w, domain.ProviderPreferences{Provider: "codex", Revision: 3}, nil)
			return
		}
		var request domain.AgentPreferencesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Provider == nil || *request.Provider != "copilot" ||
			request.Model == nil || *request.Model != "preferred-model" || request.ExpectedRevision != 3 {
			t.Error("CLI changed the agent default proposal", request, err)
		}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Unavailable, "Fixture lost the accepted response."))
			return
		}
		diagnosticsEnvelope(t, w, domain.ProviderPreferences{Provider: "copilot", Model: "preferred-model", Revision: 4}, nil)
	})
	args := []string{"--config", file, "--operation-id", "agent-model-exact", "agent", "preferences", "agent_target", "--input", "-"}
	input := map[string]any{"provider": "copilot", "model": "preferred-model", "reason": "Founder chooses a model for new sessions."}
	if response, code := invoke(t, "agent", args, input); code != 7 || response.OK {
		t.Fatal("lost response was hidden", response, code)
	}
	if response, code := invoke(t, "agent", args, input); code != 0 || !response.OK {
		t.Fatal("exact preference replay failed", response, code)
	}
	if reads != 1 || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("preference replay fetched a newer revision or changed its body", reads, bodies)
	}
	input["model"] = "different-model"
	if response, code := invoke(t, "agent", args, input); code != 5 || response.OK || len(bodies) != 2 {
		t.Fatal("changed preference reused an immutable proposal", response, code)
	}
	if response, code := invoke(t, "agent", []string{"--config", file, "agent", "preferences", "agent_target"}, nil); code != 0 || !response.OK {
		t.Fatal("preference read failed", response, code)
	}
}
