package cli

import (
	"context"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAssistanceStatusOnlyReadsRetainedSessionAndNeverOpensLogin(t *testing.T) {
	sessionID := domain.NewID("session")
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/sessions/"+sessionID {
			t.Error("assistance status performed an unexpected operation", r.Method, r.URL.Path)
		}
		diagnosticsEnvelope(t, w, domain.Session{ID: sessionID, State: "closed"}, nil)
	})
	a := &app{configFile: file, operation: "op_status", json: true, nonInteractive: true}
	r, err := a.load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	proposal := assistanceOpenProposal{GrantID: "grant_fixture", SessionID: sessionID}
	if err := r.state.Put(context.Background(), "assistance-open:op_open", proposal); err != nil {
		t.Fatal(err)
	}
	result, err := a.assistanceCommand(context.Background(), r, []string{"status", "op_open"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(domain.Session).State != "closed" {
		t.Fatal("status did not return observed closure")
	}
	for _, request := range requests() {
		if request.method != "GET" {
			t.Error("status mutated the target", request.method, request.path)
		}
	}
	proposal.SessionID = ""
	if err := r.state.Put(context.Background(), "assistance-open:op_unknown", proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.assistanceCommand(context.Background(), r, []string{"status", "op_unknown"}); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("unknown session was not retained without a retry", err)
	}
}
