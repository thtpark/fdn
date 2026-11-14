package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

func TestAgentSelectedDefaultReplaysExactProposalWithoutReInferenceOrNewProbe(t *testing.T) {
	f := newPushFixture(t)
	executor := &selectionExecutor{models: []string{"chosen-model"}}
	f.engine.Executor = executor
	base := f.engine.Client.HTTP.Transport
	preferences := domain.ProviderPreferences{Revision: 1}
	catalogCalls, setCalls := 0, 0
	var original []byte
	lose := true
	f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		path := request.URL.Path
		if path == "/v1/agents/"+f.me.ID+"/model-options" {
			catalogCalls++
			var req domain.ProviderCatalogRequest
			if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
				return nil, err
			}
			if err := domain.ValidateProviderCatalog(req.Observation); err != nil {
				return nil, err
			}
			return f.response(domain.ComputerModelOptions{BindingID: f.me.ID, Generation: 1, ReceivedAt: f.engine.Now().Unix(), Observation: req.Observation}, nil)
		}
		if path == "/v1/agents/"+f.me.ID+"/preferences" {
			if request.Method == http.MethodGet {
				return f.response(preferences, nil)
			}
			setCalls++
			var body bytes.Buffer
			if _, err := body.ReadFrom(request.Body); err != nil {
				return nil, err
			}
			if len(original) != 0 && !bytes.Equal(original, body.Bytes()) {
				t.Error("uncertain preference replay changed its exact proposal")
			}
			var req domain.AgentPreferencesRequest
			if err := json.Unmarshal(body.Bytes(), &req); err != nil {
				return nil, err
			}
			if req.Catalog == nil || req.Catalog.BindingID != f.me.ID || req.Catalog.Generation != 1 {
				t.Fatal("self selection was not bound to its observed computer")
			}
			if len(original) == 0 {
				original = append([]byte{}, body.Bytes()...)
				preferences = domain.ProviderPreferences{Provider: *req.Provider, Model: *req.Model, Revision: 2, UpdatedBy: f.me.ID}
			}
			if lose {
				lose = false
				return nil, errors.New("lost preference acknowledgement")
			}
			return f.response(preferences, nil)
		}
		return base.RoundTrip(request)
	})
	journal := local.Journal{ID: "invocation_preferred_model"}
	choice := &provider.PreferenceChoice{Provider: "copilot", Model: "chosen-model", Reason: "Select the authenticated model for my next call"}
	if err := f.engine.applyPreferredModel(context.Background(), &journal, choice); err == nil {
		t.Fatal("lost acknowledgement became success")
	}
	if err := f.engine.applyPreferredModel(context.Background(), &journal, choice); err != nil {
		t.Fatal(err)
	}
	if catalogCalls != 1 || setCalls != 2 || len(executor.names) != 1 || preferences.Model != "chosen-model" {
		t.Fatal("preference replay re-probed/inferred or changed the desired default", catalogCalls, setCalls, executor.names, preferences)
	}
	prompt, err := invocationPrompt([]byte(`{"phase":"message","agent_id":"agent_fixture"}`))
	if err != nil || !strings.Contains(prompt, "preferred_model={provider,model,reason}") || !strings.Contains(prompt, "never the pinned task/provider/model") {
		t.Fatal("provider does not receive the self-default and unchanged-task contract", err)
	}
}
