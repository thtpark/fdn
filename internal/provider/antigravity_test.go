package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type antigravityRunner struct {
	t             *testing.T
	authenticated bool
	toolAttempt   bool
	permission    bool
	requests      []string
	specs         []process.Spec
	joined        bool
}

func (r *antigravityRunner) Run(ctx context.Context, spec process.Spec) error {
	r.specs = append(r.specs, spec)
	if slices.Contains(spec.Args, "--version") {
		_, err := fmt.Fprintln(spec.Stdout, "1.5.8")
		return err
	}
	if slices.Contains(spec.Args, "--help") {
		flags := "--json-schema --output-format --agent --settings --disable-slash-commands"
		if slices.Contains(spec.Args, "acp") {
			flags = "--stdio --include-default-customizations --gemini-dir --dangerously-skip-permissions --disable-telemetry --login"
		}
		_, err := fmt.Fprintln(spec.Stderr, flags)
		return err
	}
	if len(spec.Args) == 0 || spec.Args[0] != "acp" {
		return errors.New("unexpected native provider arguments")
	}
	if spec.Started != nil {
		if err := spec.Started(12345); err != nil {
			return err
		}
	}
	defer func() { r.joined = true }()
	reader := bufio.NewScanner(spec.Stdin)
	reader.Buffer(make([]byte, 4096), domain.MaxBody)
	writer := json.NewEncoder(spec.Stdout)
	for reader.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			return err
		}
		r.requests = append(r.requests, request.Method)
		reply := func(result any) error {
			return writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Method {
		case "initialize":
			var parameters struct {
				Capabilities struct {
					Terminal bool `json:"terminal"`
					FS       struct {
						Read  bool `json:"readTextFile"`
						Write bool `json:"writeTextFile"`
					} `json:"fs"`
				} `json:"clientCapabilities"`
			}
			if err := json.Unmarshal(request.Params, &parameters); err != nil {
				return err
			}
			if parameters.Capabilities.Terminal || parameters.Capabilities.FS.Read || parameters.Capabilities.FS.Write {
				return errors.New("client file/terminal capabilities were enabled")
			}
			if err := reply(map[string]any{"protocolVersion": 1}); err != nil {
				return err
			}
		case "session/new":
			var parameters struct {
				CWD string            `json:"cwd"`
				MCP []json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal(request.Params, &parameters); err != nil || parameters.MCP == nil || len(parameters.MCP) != 0 {
				return errors.New("external MCP capabilities were not explicitly empty")
			}
			if !r.authenticated {
				if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32000, "message": "Authentication required"}}); err != nil {
					return err
				}
				continue
			}
			if err := reply(map[string]any{"sessionId": "session_antigravity_fixture",
				"models": map[string]any{"availableModels": []map[string]string{{"modelId": "gemini-fixture"}}}}); err != nil {
				return err
			}
		case "session/set_model":
			var parameters map[string]string
			if err := json.Unmarshal(request.Params, &parameters); err != nil || parameters["modelId"] != "gemini-fixture" {
				return errors.New("model was not pinned to the provider session")
			}
			if err := reply(map[string]any{}); err != nil {
				return err
			}
		case "session/prompt":
			if r.permission {
				return writer.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission",
					"params": map[string]any{"sessionId": "session_antigravity_fixture"}})
			}
			update := "agent_message_chunk"
			if r.toolAttempt {
				update = "tool_call"
			}
			response := `{"outcome":"completed","summary":"Checked fixture work","evidence":[],"questions":[],"answer_ids":[],"safe_to_retry":false}`
			if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": "session_antigravity_fixture", "update": map[string]any{
					"sessionUpdate": update, "content": map[string]string{"type": "text", "text": response}}}}); err != nil {
				return err
			}
			if err := reply(map[string]string{"stopReason": "end_turn"}); err != nil {
				return err
			}
		case "session/close":
			if err := reply(map[string]any{}); err != nil {
				return err
			}
		default:
			return errors.New("unsupported fixture ACP request")
		}
	}
	return ctx.Err()
}

func antigravityManager(t *testing.T, authenticated bool) (*Manager, *antigravityRunner) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".foundation", "state")
	c := config.DefaultWorker(state)
	c.ProviderPaths["antigravity"] = filepath.Join(home, "agy")
	runner := &antigravityRunner{t: t, authenticated: authenticated}
	return &Manager{Config: c, Runner: runner}, runner
}

func TestAntigravityReadinessIsAuthenticatedAndModelOnlyWithoutInference(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		t.Run(fmt.Sprint(authenticated), func(t *testing.T) {
			manager, runner := antigravityManager(t, authenticated)
			health, err := manager.Check(context.Background(), "antigravity")
			want := "login-required"
			if authenticated {
				want = "authenticated"
			}
			if err != nil || health.Authentication != want || !runner.joined {
				t.Fatal("readiness lost explicit authentication or native cleanup", health, err)
			}
			if slices.Contains(runner.requests, "session/prompt") || slices.Contains(runner.requests, "authenticate") {
				t.Fatal("read-only status started inference or login", runner.requests)
			}
			if authenticated && !slices.Equal(health.Models, []string{"gemini-fixture"}) {
				t.Fatal("authenticated model catalog missing", health)
			}
		})
	}
}

func TestAntigravityCodingAndToolDisabledTextUseExactIsolatedCLIContract(t *testing.T) {
	for _, coding := range []bool{true, false} {
		t.Run(fmt.Sprint(coding), func(t *testing.T) {
			manager, runner := antigravityManager(t, true)
			inv := Invocation{ID: "invocation_antigravity_fixture", Provider: "antigravity", Path: "/fixture/agy",
				Model: "gemini-fixture", Prompt: "Return the requested structured reply", Coding: coding, Timeout: 1e9,
				Workspace: t.TempDir()}
			started := 0
			inv.Started = func(pid int) error { started = pid; return nil }
			reply, err := manager.Invoke(context.Background(), inv)
			if err != nil || reply.Outcome != "completed" || started != 12345 || !runner.joined {
				t.Fatal("Antigravity did not return a joined structured invocation", reply, err)
			}
			spec := runner.specs[len(runner.specs)-1]
			if slices.Contains(spec.Args, "--dangerously-skip-permissions") != coding ||
				!slices.Contains(spec.Args, "--include-default-customizations=false") || !slices.Contains(spec.Args, antigravityAgent) {
				t.Fatal("native isolation or nonprompting policy missing", spec.Args)
			}
			root := filepath.Join(manager.Config.StateDir, "invocations", inv.ID, "antigravity-gemini")
			data, err := secure.Read(filepath.Join(root, "config", "agents", antigravityAgent, "agent.md"), domain.MaxBody)
			if err != nil || !bytes.Contains(data, []byte("subagent: false")) || !bytes.Contains(data, []byte("mcpServers: []")) ||
				(!coding && !bytes.Contains(data, []byte("tools: []"))) {
				t.Fatal("fixed tool/agent capabilities were not written", err)
			}
			var settings struct {
				Permission   string `json:"toolPermission"`
				DisableSlash bool   `json:"disableSlashCommands"`
				Permissions  struct {
					Deny  []string `json:"deny"`
					Allow []string `json:"allow"`
					Ask   []string `json:"ask"`
				} `json:"permissions"`
			}
			if err := config.Read(filepath.Join(root, "antigravity-cli", "settings.json"), &settings); err != nil {
				t.Fatal(err)
			}
			if !coding && (settings.Permission != "strict" || len(settings.Permissions.Deny) != 7) {
				t.Fatal("all text tools were not explicitly denied", settings)
			}
		})
	}
}

func TestAntigravityUnexpectedPermissionOrTextToolFailsWithoutInteractiveApproval(t *testing.T) {
	for _, permission := range []bool{false, true} {
		t.Run(fmt.Sprint(permission), func(t *testing.T) {
			manager, runner := antigravityManager(t, true)
			runner.permission, runner.toolAttempt = permission, !permission
			_, err := manager.Invoke(context.Background(), Invocation{ID: "invocation_denied", Provider: "antigravity",
				Path: "/fixture/agy", Prompt: "Return JSON", Timeout: 1e9})
			if err == nil || !runner.joined {
				t.Fatal("unexpected tool/approval became success or left a native owner", err)
			}
		})
	}
}

func TestAntigravityBoundedACPRejectsOversizedFrame(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", domain.MaxBody+2)), domain.MaxBody)
	var output bytes.Buffer
	if _, err := acpCall(reader, &output, 1, "initialize", map[string]any{}, nil); err == nil || domain.AsFault(err).Code != domain.Provider {
		t.Fatal("unbounded ACP data was accepted", err)
	}
}

var _ process.Runner = (*antigravityRunner)(nil)
