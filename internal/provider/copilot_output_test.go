package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func copilotEvents(t *testing.T, events ...any) []byte {
	t.Helper()
	var output bytes.Buffer
	for _, event := range events {
		if err := json.NewEncoder(&output).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes()
}

func copilotMessage(content string) map[string]any {
	return map[string]any{"type": "assistant.message", "data": map[string]any{"content": content}}
}

func copilotChunk(id, content string, index, count int) map[string]any {
	return map[string]any{"type": "assistant.message", "data": map[string]any{
		"apiCallId": id, "messageId": "message-" + strconv.Itoa(index), "content": content, "chunkIndex": index, "chunkCount": count,
	}}
}

func copilotResult(exitCode int) map[string]any {
	return map[string]any{"type": "result", "sessionId": "synthetic-session", "exitCode": exitCode}
}

type copilotOutputRunner struct {
	output, diagnostic []byte
	failure            error
	spec               process.Spec
}

func (runner *copilotOutputRunner) Run(_ context.Context, spec process.Spec) error {
	runner.spec = spec
	_, outErr := spec.Stdout.Write(runner.output)
	_, diagnosticErr := spec.Stderr.Write(runner.diagnostic)
	return errors.Join(runner.failure, outErr, diagnosticErr)
}

func TestCopilotMachineOutputPreservesUnrenderedFinalReply(t *testing.T) {
	summary := strings.Repeat("Long JSON text must not be wrapped inside its string. ", 12) + "\"quoted\"\nnext line \u201cUnicode\u201d"
	want := Reply{Outcome: "completed", Summary: summary, Evidence: []string{"Synthetic local evidence"}}
	content, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, coding := range []bool{false, true} {
		t.Run(map[bool]string{true: "coding", false: "text"}[coding], func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "workspace")
			if err := secure.Dir(workspace); err != nil {
				t.Fatal(err)
			}
			worker := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
			runner := &copilotOutputRunner{output: copilotEvents(t,
				map[string]any{"type": "user.message", "data": map[string]string{"content": "not the reply"}},
				copilotMessage(`{"outcome":"paused","summary":"Earlier intermediate message"}`),
				map[string]any{"type": "assistant.message_delta", "data": map[string]string{"deltaContent": "not the final reply"}},
				map[string]any{"type": "tool.execution_complete", "data": map[string]string{"content": "not the final reply"}},
				copilotChunk("final-message", string(content), 0, 1),
				map[string]any{"type": "assistant.turn_end", "data": map[string]string{"turnId": "synthetic-turn"}},
				copilotResult(0),
			)}
			manager := &Manager{Config: worker, Runner: runner}
			reply, err := manager.Invoke(context.Background(), Invocation{ID: "copilot-machine", Provider: "copilot",
				Path: "/synthetic/copilot", Model: "synthetic-model", Prompt: "Synthetic private prompt",
				Coding: coding, Workspace: workspace, Timeout: time.Second})
			if err != nil || reply.Outcome != want.Outcome || reply.Summary != summary {
				t.Fatal("machine response changed JSON strings or selected non-final output", reply, err)
			}
			index := slices.Index(runner.spec.Args, "--output-format")
			if index < 0 || index+1 >= len(runner.spec.Args) || runner.spec.Args[index+1] != "json" ||
				slices.Contains(runner.spec.Args, "--silent") || slices.Contains(runner.spec.Args, "--allow-all") != coding {
				t.Fatal("machine output changed tool or output policy", runner.spec.Args)
			}
			path := filepath.Join(worker.StateDir, "invocations", "copilot-machine", "reply.json")
			saved, err := secure.Read(path, 256<<10)
			if err != nil || !bytes.Equal(saved, content) {
				t.Fatal("exact unrendered reply bytes were not retained privately", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("machine reply is not owner-only", info, err)
			}
		})
	}
}

func TestCopilotMachineOutputRequiresCompleteSuccessfulFinalMessage(t *testing.T) {
	const reply = `{"outcome":"clear","summary":"Synthetic final response"}`
	message, result := copilotMessage(reply), copilotResult(0)
	cases := map[string][]byte{
		"empty":                  nil,
		"human-json":             []byte(reply),
		"wrapped-human-json":     []byte("{\"outcome\":\"clear\",\"summary\":\"Synthetic\nwrapped text\"}"),
		"markdown":               []byte("```json\n" + reply + "\n```"),
		"non-json-prefix":        append([]byte("Human output\n"), copilotEvents(t, message, result)...),
		"malformed-event":        []byte("{"),
		"missing-type":           copilotEvents(t, map[string]any{"data": map[string]string{"content": reply}}, result),
		"missing-result":         copilotEvents(t, message),
		"missing-message":        copilotEvents(t, result),
		"missing-exit":           copilotEvents(t, message, map[string]string{"type": "result"}),
		"null-exit":              copilotEvents(t, message, map[string]any{"type": "result", "exitCode": nil}),
		"nested-exit":            copilotEvents(t, message, map[string]any{"type": "result", "data": map[string]int{"exitCode": 0}}),
		"failed-result":          copilotEvents(t, message, copilotResult(1)),
		"error-result":           copilotEvents(t, message, map[string]any{"type": "result", "exitCode": 0, "isError": true}),
		"error-event":            copilotEvents(t, message, map[string]string{"type": "error"}, result),
		"session-error":          copilotEvents(t, message, map[string]string{"type": "session.error"}, result),
		"duplicate-result":       copilotEvents(t, message, result, result),
		"post-result-event":      copilotEvents(t, message, result, map[string]string{"type": "assistant.idle"}),
		"truncated-result":       append(copilotEvents(t, message), []byte(`{"type":"result","exitCode":`)...),
		"delta-only":             copilotEvents(t, map[string]any{"type": "assistant.message_delta", "data": map[string]string{"deltaContent": reply}}, result),
		"tool-output-only":       copilotEvents(t, map[string]any{"type": "tool.execution_complete", "data": map[string]string{"content": reply}}, result),
		"empty-last-message":     copilotEvents(t, message, copilotMessage(""), result),
		"null-message-data":      copilotEvents(t, message, map[string]any{"type": "assistant.message", "data": nil}, result),
		"invalid-message-data":   copilotEvents(t, map[string]any{"type": "assistant.message", "data": map[string]int{"content": 1}}, result),
		"last-message-has-tools": copilotEvents(t, message, map[string]any{"type": "assistant.message", "data": map[string]any{"content": reply, "toolRequests": []string{"unfinished"}}}, result),
		"nonfinal-phase":         copilotEvents(t, message, map[string]any{"type": "assistant.message", "data": map[string]string{"content": reply, "phase": "commentary"}}, result),
		"subagent-only":          copilotEvents(t, map[string]any{"type": "assistant.message", "agentId": "child", "data": map[string]string{"content": reply}}, result),
		"legacy-subagent-only":   copilotEvents(t, map[string]any{"type": "assistant.message", "data": map[string]string{"content": reply, "parentToolCallId": "child-call"}}, result),
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := copilotFinalResponse(output); domain.AsFault(err).Code != domain.Provider {
				t.Fatal("incomplete or human output became a successful response", err)
			}
		})
	}
}

func TestCopilotMachineOutputValidatesOrderedMessageChunksAndBounds(t *testing.T) {
	first, second := copilotChunk("final", "first", 0, 2), copilotChunk("final", "second", 1, 2)
	response, err := copilotFinalResponse(copilotEvents(t, first,
		map[string]string{"type": "model.call_finished"}, second, copilotResult(0)))
	if err != nil || string(response) != "second" {
		t.Fatal("reasoning-boundary messages were concatenated instead of selecting the final message", string(response), err)
	}
	final := copilotChunk("observed-call", `{"outcome":"clear","summary":"Synthetic native-shaped final answer"}`, 2, 3)
	final["data"].(map[string]any)["phase"] = "final_answer"
	response, err = copilotFinalResponse(copilotEvents(t,
		copilotChunk("observed-call", "", 0, 3), copilotChunk("observed-call", "", 1, 3), final,
		map[string]any{"type": "assistant.message", "agentId": "child", "data": map[string]string{"content": "not the root result"}},
		map[string]any{"type": "assistant.message", "data": map[string]string{"content": "not the root result", "parentToolCallId": "legacy-child"}},
		copilotResult(0),
	))
	if err != nil || string(response) != final["data"].(map[string]any)["content"] {
		t.Fatal("native distinct-message reasoning chunks or child events replaced the root final answer", string(response), err)
	}
	cases := map[string][]byte{
		"missing-first":       copilotEvents(t, second, copilotResult(0)),
		"missing-last":        copilotEvents(t, first, copilotResult(0)),
		"repeated-first":      copilotEvents(t, first, first, second, copilotResult(0)),
		"replaced-incomplete": copilotEvents(t, first, copilotMessage("replacement"), copilotResult(0)),
		"different-call":      copilotEvents(t, first, copilotChunk("other", "second", 1, 2), copilotResult(0)),
		"different-count":     copilotEvents(t, first, copilotChunk("final", "second", 1, 3), copilotResult(0)),
		"missing-call":        copilotEvents(t, copilotChunk("", "first", 0, 2), second, copilotResult(0)),
		"negative-index":      copilotEvents(t, copilotChunk("final", "first", -1, 2), copilotResult(0)),
		"zero-count":          copilotEvents(t, copilotChunk("final", "first", 0, 0), copilotResult(0)),
		"missing-index":       copilotEvents(t, map[string]any{"type": "assistant.message", "data": map[string]any{"content": "first", "chunkCount": 2}}, copilotResult(0)),
		"oversized-reply":     copilotEvents(t, copilotMessage(strings.Repeat("x", (256<<10)+1)), copilotResult(0)),
		"oversized-last":      copilotEvents(t, first, copilotChunk("final", strings.Repeat("x", (256<<10)+1), 1, 2), copilotResult(0)),
		"oversized-event":     bytes.Repeat([]byte("x"), (2<<20)+1),
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := copilotFinalResponse(output); domain.AsFault(err).Code != domain.Provider {
				t.Fatal("invalid/incomplete/oversized chunks became success", err)
			}
		})
	}
	atLimit := strings.Repeat("x", 256<<10)
	response, err = copilotFinalResponse(copilotEvents(t, copilotMessage(atLimit), copilotResult(0)))
	if err != nil || string(response) != atLimit {
		t.Fatal("exact final-response byte limit rejected", len(response), err)
	}
	response, err = copilotFinalResponse(copilotEvents(t,
		copilotChunk("large-intermediate", strings.Repeat("x", (256<<10)+1), 0, 2),
		copilotChunk("large-intermediate", "bounded final message", 1, 2), copilotResult(0)))
	if err != nil || string(response) != "bounded final message" {
		t.Fatal("intermediate message was treated as the final reply", len(response), err)
	}
}

func TestCopilotMachineOutputPreservesFailureAndStrictReplyValidation(t *testing.T) {
	const valid = `{"outcome":"clear","summary":"Synthetic valid response"}`
	cases := map[string]*copilotOutputRunner{
		"invalid-reply-json": {output: copilotEvents(t, copilotMessage("{"), copilotResult(0))},
		"unknown-reply-key":  {output: copilotEvents(t, copilotMessage(`{"outcome":"clear","summary":"Synthetic","unknown":true}`), copilotResult(0))},
		"invalid-outcome":    {output: copilotEvents(t, copilotMessage(`{"outcome":"done","summary":"Cannot self-verify"}`), copilotResult(0))},
		"empty-summary":      {output: copilotEvents(t, copilotMessage(`{"outcome":"clear","summary":""}`), copilotResult(0))},
		"native-failure":     {output: copilotEvents(t, copilotMessage(valid), copilotResult(0)), failure: errors.New("synthetic native failure")},
		"stdout-overflow":    {output: bytes.Repeat([]byte("x"), (2<<20)+1)},
		"stderr-overflow":    {output: copilotEvents(t, copilotMessage(valid), copilotResult(0)), diagnostic: bytes.Repeat([]byte("x"), (256<<10)+1)},
	}
	for name, runner := range cases {
		t.Run(name, func(t *testing.T) {
			worker := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
			manager := &Manager{Config: worker, Runner: runner}
			_, err := manager.Invoke(context.Background(), Invocation{ID: "failed-machine", Provider: "copilot",
				Path: "/synthetic/copilot", Prompt: "Synthetic bounded prompt", Timeout: time.Second})
			if err == nil {
				t.Fatal("machine parsing hid a real native/schema/output failure")
			}
			if _, err := os.Stat(filepath.Join(worker.StateDir, "invocations", "failed-machine", "reply.json")); !os.IsNotExist(err) {
				t.Fatal("failed response became a saved valid reply", err)
			}
		})
	}
}

type copilotLegacyOutputRunner struct{ fixtureRunner }

func (runner *copilotLegacyOutputRunner) Run(ctx context.Context, spec process.Spec) error {
	if slices.Contains(spec.Args, "--help") {
		_, err := io.WriteString(spec.Stdout, "--available-tools --allow-all --disable-builtin-mcps --no-custom-instructions --no-ask-user --no-auto-update --silent")
		return err
	}
	return runner.fixtureRunner.Run(ctx, spec)
}

func TestCopilotRequiresMachineOutputCapabilityBeforeAuthentication(t *testing.T) {
	worker := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	worker.ProviderPaths["copilot"] = "/synthetic/copilot"
	runner := &copilotLegacyOutputRunner{}
	_, err := (&Manager{Config: worker, Runner: runner}).Check(context.Background(), "copilot")
	if domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("missing machine output was not an explicit compatibility failure", err)
	}
	for _, spec := range runner.specs {
		if slices.Contains(spec.Args, "--headless") || slices.Contains(spec.Args, "login") {
			t.Fatal("unsupported adapter started authentication or inference", spec.Args)
		}
	}
}
