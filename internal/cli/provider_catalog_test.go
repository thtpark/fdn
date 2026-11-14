package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

func TestModelOptionsCLIAndObserverDispatchIndependentlyOfEnvironment(t *testing.T) {
	reads := 0
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/v1/agents/agent_target/model-options" {
			t.Error("catalog query did not use its exact read-only route", r.Method, r.URL.RequestURI())
		}
		reads++
		diagnosticsEnvelope(t, w, domain.AgentModelOptions{AgentID: "agent_target", ServerObservedAt: 1800000000,
			Computers: []domain.ComputerModelOptions{}}, nil)
	})
	response, code := invoke(t, "agent", []string{"--config", file, "agent", "model-options", "agent_target"}, nil)
	if code != 0 || !response.OK {
		t.Fatal("documented standalone model-options command was unreachable", response, code)
	}
	a := &app{configFile: file}
	r, err := a.load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	value, err := (observationSource{app: a, res: r}).Snapshot(context.Background(), observe.Query{Kind: "modelOptions", ID: "agent_target"})
	if err != nil {
		t.Fatal("native/embedded observer could not reach model-options", err)
	}
	data, err := json.Marshal(value)
	if err != nil || !bytes.Contains(data, []byte("agent_target")) || reads != 2 {
		t.Fatal("catalog lookup lost its exact agent or repeated another route", reads, err)
	}
}

func TestModelOptionsReportCLIIsReachableAndRetainsExactReplay(t *testing.T) {
	var bodies [][]byte
	var actor string
	worker, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/agents/"+actor+"/model-options" {
			t.Error("catalog publication did not use its own exact route", r.Method, r.URL.RequestURI())
		}
		var req domain.ProviderCatalogRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if err := domain.ValidateProviderCatalog(req.Observation); err != nil {
			t.Error(err)
		}
		data, err := json.Marshal(req)
		if err != nil {
			t.Error(err)
		}
		bodies = append(bodies, data)
		diagnosticsEnvelope(t, w, domain.ComputerModelOptions{BindingID: actor, Generation: 1,
			ReceivedAt: req.Observation.ObservedAt, Observation: req.Observation}, nil)
	})
	var credential config.Credential
	if err := config.Read(worker.CredentialFile, &credential); err != nil {
		t.Fatal(err)
	}
	actor = credential.AgentID
	args := []string{"--config", file, "--operation-id", "catalog-report-exact", "agent", "model-options-report"}
	for range 2 {
		response, code := invoke(t, "agent", args, nil)
		if code != 0 || !response.OK {
			t.Fatal("documented standalone model-options-report command was unreachable", response, code)
		}
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("explicit catalog replay renewed or changed its original captured observation", len(bodies))
	}
}
