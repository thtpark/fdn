package cli

import (
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestDiscussionShowCLIReadsOneEntryWithoutMutation(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/entries/entry_fixture" || r.URL.RawQuery != "" {
			t.Error("unexpected entry read", r.Method, r.URL.String())
		}
		diagnosticsEnvelope(t, w, domain.Entry{
			ID: "entry_fixture", TaskID: "task_fixture", Kind: "comment", ParentID: "entry_original",
			AuthorID: "agent_fixture", Body: "A synthetic reply.", Revision: 2, Generation: 2,
		}, nil)
	})
	envelope, code := invoke(t, "agent", []string{"--config", file, "discussion", "show", "entry_fixture"}, nil)
	if code != 0 || !envelope.OK {
		t.Fatal("discussion show failed", envelope, code)
	}
	for _, args := range [][]string{{"discussion", "show"}, {"discussion", "show", "entry_fixture", "--cursor", "unexpected"}} {
		if _, code := invoke(t, "agent", append([]string{"--config", file}, args...), nil); code != 2 {
			t.Fatal("invalid entry arguments were accepted", args, code)
		}
	}
	entryReads := 0
	for _, request := range requests() {
		if request.method != http.MethodGet || len(request.body) != 0 {
			t.Fatal("entry read performed a mutation", request)
		}
		if request.path == "/v1/entries/entry_fixture" {
			entryReads++
		}
	}
	if entryReads != 1 {
		t.Fatal("invalid arguments performed an entry lookup", entryReads)
	}
}
