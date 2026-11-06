package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/observe"
)

func TestEmbeddedCLIManualMessageCommentAndTaskWrites(t *testing.T) {
	ctx := context.Background()
	worker, _, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("embedded manual write used an unexpected method", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "invalid fixture write"))
			return
		}
		body["id"] = "result_fixture"
		diagnosticsEnvelope(t, w, body, nil)
	})
	state, err := local.Open(ctx, worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	client, err := api.NewLocalClient(worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := config.Read(worker.CredentialFile, &client.Credential); err != nil {
		t.Fatal(err)
	}
	embedded, err := NewEmbedded(worker, state, client, func() any {
		return map[string]any{"runtime": "foreground-founder", "ready": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		args     []string
		body     string
		path     string
		revision int64
		field    string
		value    string
	}{
		{name: "message", args: []string{"message", "send", "--input", "-"},
			body: `{"recipient_id":"agent_peer","body":"Phone message","response_requested":false}`,
			path: "/v1/messages", field: "body", value: "Phone message"},
		{name: "comment", args: []string{"comment", "add", "task_fixture", "--input", "-"},
			body: `{"body":"Phone comment"}`, path: "/v1/tasks/task_fixture/entries",
			revision: 7, field: "body", value: "Phone comment"},
		{name: "task", args: []string{"task", "change", "task_fixture", "--input", "-"},
			body: `{"title":"Phone task title"}`, path: "/v1/tasks/task_fixture/change",
			revision: 7, field: "title", value: "Phone task title"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := "embedded-" + test.name
			data, err := embedded.Mutate(ctx, test.args, json.RawMessage(test.body), operation, test.revision)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result["id"] != "result_fixture" || result[test.field] != test.value {
				t.Fatal("embedded manual write returned an incorrect result", result)
			}
			recorded := requests()
			last := recorded[len(recorded)-1]
			if last.method != http.MethodPost || last.path != test.path {
				t.Fatal("embedded manual write used the wrong route", last.method, last.path)
			}
			var command domain.Command
			if err := json.Unmarshal(last.body, &command); err != nil {
				t.Fatal(err)
			}
			if command.OperationID != operation || command.ExpectedRevision != test.revision {
				t.Fatal("embedded manual write lost its exact operation or revision", command)
			}
		})
	}
	_, err = embedded.Mutate(ctx, []string{"message", "send", "--input", "-"},
		json.RawMessage(`{"recipient_id":"agent_peer","body":"Phone message","response_requested":false}`),
		"embedded-message", 0)
	if err != nil {
		t.Fatal(err)
	}
	var writes []operationCLIRequest
	for _, request := range requests() {
		if request.method == http.MethodPost {
			writes = append(writes, request)
		}
	}
	if len(writes) != 4 || !bytes.Equal(writes[0].body, writes[3].body) {
		t.Fatal("embedded message replay changed the exact retained proposal", writes)
	}
}

func TestEmbeddedCLIFailsClosedBeforeDesktopOrFileInputDispatch(t *testing.T) {
	e := &Embedded{}
	for _, args := range [][]string{
		{"work", "run"}, {"provider", "exec"}, {"service", "install"}, {"agent", "register"},
		{"device", "register"}, {"session", "open"}, {"upgrade", "apply"},
		{"agent", "add"}, {"agent", "request-sign-in"}, {"assistance", "grant"}, {"assistance", "open"},
		{"message", "send", "--input", "/private/must-not-read"},
		{"file", "upload", "--path", "/private/must-not-read"},
		{"file", "download", "file_fixture", "--output", "/private/must-not-write"},
		{"file", "begin", "--input", "/private/must-not-read"},
		{"file", "put-chunk", "file_fixture", "--input", "/private/must-not-read"},
		{"comment", "add", "task_fixture", "--input", "-",
			"--config", "/private/must-not-read"},
	} {
		if _, err := e.Mutate(context.Background(), args, []byte(`{}`), domain.NewID("op"), 0); err == nil {
			t.Fatal("embedded CLI admitted an unsupported capability", args)
		}
		for _, kind := range []string{"files", "file", "fileChunk", "publications", "publication", "publicationVersions", "publicationVersion"} {
			if err := e.ValidateQuery(observe.Query{Kind: kind, ID: "fixture"}); err != nil {
				t.Fatal("embedded runtime omitted a bounded file/publication read", kind, err)
			}
		}
	}
	for _, kind := range []string{"provider", "inventory", "inventoryRefresh", "workspaces", "screens", "upgrade", "doctor", "preferences"} {
		if err := e.ValidateQuery(observe.Query{Kind: kind}); domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal("mobile runtime invented a desktop read projection", kind, err)
		}
	}
}
