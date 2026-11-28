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
		{name: "room-create", args: []string{"room", "create", "--input", "-"},
			body: `{"kind":"group","name":"Mobile Team","member_ids":["agent_peer"]}`,
			path: "/v1/rooms", field: "name", value: "Mobile Team"},
		{name: "room-send", args: []string{"room", "send", "room_fixture", "--input", "-"},
			body: `{"body":"Phone room message","origin":"human"}`,
			path: "/v1/rooms/room_fixture/messages", field: "body", value: "Phone room message"},
		{name: "room-change", args: []string{"room", "change", "room_fixture", "--input", "-"},
			body: `{"name":"Renamed Mobile Team"}`,
			path: "/v1/rooms/room_fixture/change", revision: 2, field: "name", value: "Renamed Mobile Team"},
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
	if len(writes) != 7 || !bytes.Equal(writes[0].body, writes[6].body) {
		t.Fatal("embedded message replay changed the exact retained proposal", writes)
	}
}

func TestEmbeddedCLIFailsClosedBeforeDesktopOrFileInputDispatch(t *testing.T) {
	e := &Embedded{}
	for _, args := range [][]string{
		{"work", "run"}, {"provider", "exec"}, {"service", "install"}, {"agent", "register"},
		{"device", "register"}, {"session", "open"}, {"upgrade", "apply"},
		{"agent", "add"}, {"agent", "request-sign-in"}, {"assistance", "grant"}, {"assistance", "open"},
		{"agent", "request-sign-in", "agent_target", "--input", "/private/must-not-read"},
		{"local-model", "init", "--input", "-"},
		{"local-model", "status"},
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

func TestEmbeddedCLILocalModelInitWorksForFounderControlAndRejectsLocalExecution(t *testing.T) {
	ctx := context.Background()
	worker, _, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/agents/agent_target/local-models" {
			var req domain.LocalModelInitRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "invalid input"))
				return
			}
			spec, err := domain.ValidateLocalModelInitRequest(req)
			if err != nil {
				diagnosticsEnvelope(t, w, nil, domain.AsFault(err))
				return
			}
			diagnosticsEnvelope(t, w, domain.AgentOperation{
				Version:             domain.AgentOperationVersion,
				ID:                  "agent_operation_mobile_local",
				Revision:            1,
				Kind:                "local-model-init",
				RequesterID:         "agent_founder",
				TargetID:            "agent_target",
				RequesterGeneration: 1,
				TargetGeneration:    1,
				State:               "queued",
				Reason:              req.Reason,
				LocalModel:          &spec,
				CreatedAt:           1000,
				ExpiresAt:           1000 + domain.LocalModelInitTTL,
				ServerObservedAt:    1000,
			}, nil)
			return
		}
		diagnosticsEnvelope(t, w, nil, domain.Fail(domain.NotFound, "unexpected route"))
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
	data, err := embedded.Mutate(ctx, []string{"local-model", "init", "agent_target", "--input", "-"},
		json.RawMessage(`{"model":"qwen3.8:27b","quantized":true,"set_preference":true,"reason":"Initialize Qwen 3.8 27B from iPhone"}`),
		"mobile-local-model-001", 0)
	if err != nil {
		t.Fatal(err)
	}
	var op domain.AgentOperation
	if err := json.Unmarshal(data, &op); err != nil || op.ID != "agent_operation_mobile_local" || op.State != "queued" || op.LocalModel == nil || !op.LocalModel.SetPreference {
		t.Fatal("embedded local-model init returned unexpected operation", op, err)
	}
	recorded := requests()
	last := recorded[len(recorded)-1]
	if last.method != http.MethodPost || last.path != "/v1/agents/agent_target/local-models" {
		t.Fatal("embedded local-model init routed unexpectedly", last)
	}
}

func TestEmbeddedCLIRequestSignInWorksForFounderInAIOrManualMode(t *testing.T) {
	ctx := context.Background()
	founderMode := "ai"
	worker, _, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			diagnosticsEnvelope(t, w, domain.Agent{ID: "agent_founder", Name: "Founder", Founder: true, Active: true, Mode: founderMode}, nil)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/agents/agent_target/authentication" {
			var req domain.AuthenticationRefreshRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "invalid input"))
				return
			}
			diagnosticsEnvelope(t, w, domain.Message{
				ID:                    "message_refresh_mobile",
				SenderID:              "agent_founder",
				RecipientID:           "agent_target",
				Kind:                  "authentication-refresh",
				Origin:                "service",
				Urgent:                true,
				State:                 "pending",
				Revision:              1,
				AuthenticationRefresh: &domain.AuthenticationRefresh{Provider: req.Provider, ProjectID: req.ProjectID, Reason: req.Reason},
			}, nil)
			return
		}
		diagnosticsEnvelope(t, w, nil, domain.Fail(domain.NotFound, "unexpected route"))
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
	for _, mode := range []string{"ai", "manual"} {
		founderMode = mode
		opID := "mobile-request-sign-in-" + mode
		body := json.RawMessage(`{"provider":"antigravity","reason":"Switch Antigravity to GEMINI_API_KEY from iPhone"}`)
		data, err := embedded.Mutate(ctx, []string{"agent", "request-sign-in", "agent_target", "--input", "-"}, body, opID, 0)
		if err != nil {
			t.Fatalf("mode %s failed: %v", mode, err)
		}
		var msg domain.Message
		if err := json.Unmarshal(data, &msg); err != nil || msg.ID != "message_refresh_mobile" || msg.AuthenticationRefresh == nil || msg.AuthenticationRefresh.Provider != "antigravity" {
			t.Fatalf("unexpected embedded request-sign-in response in mode %s: %+v (%v)", mode, msg, err)
		}
		replay, err := embedded.Mutate(ctx, []string{"agent", "request-sign-in", "agent_target", "--input", "-"}, body, opID, 0)
		if err != nil || !bytes.Equal(data, replay) {
			t.Fatalf("embedded request-sign-in replay mismatch in mode %s: %v", mode, err)
		}
	}
	var posts []operationCLIRequest
	for _, req := range requests() {
		if req.method == http.MethodPost {
			posts = append(posts, req)
		}
	}
	if len(posts) != 4 || posts[0].path != "/v1/agents/agent_target/authentication" || !bytes.Equal(posts[0].body, posts[1].body) {
		t.Fatal("embedded request-sign-in did not preserve exact route/proposal across replay", posts)
	}
}
