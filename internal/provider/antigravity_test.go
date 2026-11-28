package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	t                       *testing.T
	authenticated           bool
	toolAttempt             bool
	permission              bool
	lastDisableBuiltInTools bool
	requests                []string
	specs                   []process.Spec
	joined                  bool
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
				CWD  string            `json:"cwd"`
				MCP  []json.RawMessage `json:"mcpServers"`
				Meta *struct {
					DisableCamel bool `json:"disableBuiltInTools"`
					DisableSnake bool `json:"disable_built_in_tools"`
				} `json:"_meta"`
			}
			if err := json.Unmarshal(request.Params, &parameters); err != nil || parameters.MCP == nil || len(parameters.MCP) != 0 {
				return errors.New("external MCP capabilities were not explicitly empty")
			}
			r.lastDisableBuiltInTools = parameters.Meta != nil && parameters.Meta.DisableCamel && parameters.Meta.DisableSnake
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
				runner.lastDisableBuiltInTools != !coding ||
				!slices.Contains(spec.Args, "--include-default-customizations=false") || !slices.Contains(spec.Args, antigravityAgent) {
				t.Fatal("native isolation or nonprompting policy missing", spec.Args, runner.lastDisableBuiltInTools)
			}
			root := filepath.Join(manager.Config.StateDir, "invocations", inv.ID, "antigravity-gemini")
			data, err := secure.Read(filepath.Join(root, "config", "agents", antigravityAgent, "agent.md"), domain.MaxBody)
			if err != nil || !bytes.Contains(data, []byte("subagent: false")) || !bytes.Contains(data, []byte("mcpServers: []")) ||
				!bytes.Contains(data, []byte("# Instructions")) ||
				(!coding && (!bytes.Contains(data, []byte("tools: []")) || !bytes.Contains(data, []byte("excludeDefaultComponents: true")))) {
				t.Fatal("fixed tool/agent capabilities were not written", err)
			}
			var settings struct {
				AgentMode    string `json:"agentMode"`
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
			if !coding && (settings.Permission != "strict" || len(settings.Permissions.Deny) != 6 || slices.Contains(settings.Permissions.Deny, "unsandboxed(*)")) {
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

type carbonAntigravityRunner struct {
	requireOAuthFile bool
	replaceOAuthFile string
	lastSetModel     string
	authMethod       string
}

func (r *carbonAntigravityRunner) Run(ctx context.Context, spec process.Spec) error {
	if slices.Contains(spec.Args, "--version") {
		_, err := fmt.Fprintln(spec.Stdout, "1.5.18")
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
	home := ""
	hasAPIKey := false
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
		if strings.HasPrefix(kv, "GEMINI_API_KEY=") {
			hasAPIKey = true
		}
	}
	geminiArg := ".gemini"
	for i := 0; i < len(spec.Args)-1; i++ {
		if spec.Args[i] == "--gemini-dir" {
			geminiArg = spec.Args[i+1]
		}
	}
	cliDir := filepath.Join(home, filepath.FromSlash(geminiArg), "antigravity-cli")
	if filepath.IsAbs(geminiArg) {
		cliDir = filepath.Join(geminiArg, "antigravity-cli")
	}
	oauthPath := filepath.Join(cliDir, "antigravity-oauth-token")
	if spec.Started != nil {
		if err := spec.Started(23456); err != nil {
			return err
		}
	}
	apiKeySelected := false
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
		reply := func(result any) error {
			return writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Method {
		case "initialize":
			if err := reply(map[string]any{"protocolVersion": 1}); err != nil {
				return err
			}
		case "authenticate":
			var params struct {
				MethodID string `json:"methodId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return err
			}
			r.authMethod = params.MethodID
			apiKeySelected = params.MethodID == "gemini-api-key" && hasAPIKey
			if err := reply(map[string]any{}); err != nil {
				return err
			}
		case "session/new":
			if apiKeySelected {
				if err := reply(map[string]any{
					"sessionId": "session_carbon_apikey",
					"models": map[string]any{
						"availableModels": []map[string]string{
							{"modelId": "gemini-4-carbon-high"},
							{"modelId": "gemini-4-carbon-medium"},
							{"modelId": "gemini-4-carbon-low"},
							{"modelId": "gemini-4-hydrogen-high"},
						},
					},
					"configOptions": []map[string]any{
						{
							"id":       "model",
							"category": "model",
							"options": []map[string]string{
								{"value": "gemini-4-carbon"},
								{"value": "gemini-4-hydrogen"},
							},
						},
					},
				}); err != nil {
					return err
				}
				continue
			}
			if r.requireOAuthFile {
				if _, err := os.Stat(oauthPath); err != nil {
					if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
						"error": map[string]any{"code": -32000, "message": "Authentication required"}}); err != nil {
						return err
					}
					continue
				}
				if r.replaceOAuthFile != "" {
					_ = os.Remove(oauthPath)
					_ = os.WriteFile(oauthPath, []byte(r.replaceOAuthFile), 0600)
				}
			}
			if err := reply(map[string]any{
				"sessionId": "session_oauth_linux",
				"models": map[string]any{
					"availableModels": []map[string]string{{"modelId": "gemini-4-hydrogen-high"}},
				},
			}); err != nil {
				return err
			}
		case "session/set_model":
			var params map[string]string
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return err
			}
			r.lastSetModel = params["modelId"]
			if err := reply(map[string]any{}); err != nil {
				return err
			}
		case "session/prompt":
			response := `{"outcome":"reviewed","summary":"Reviewed capabilities with Carbon","evidence":[],"questions":[],"answer_ids":[],"safe_to_retry":false}`
			if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": "session_carbon_apikey", "update": map[string]any{
					"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": response}}}}); err != nil {
				return err
			}
			if !apiKeySelected {
				// Use matching session ID when testing OAuth path
				return errors.New("prompt test expects matching session")
			}
			if err := reply(map[string]string{"stopReason": "end_turn"}); err != nil {
				return err
			}
		case "session/close":
			if err := reply(map[string]any{}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func TestAntigravityAPIKeyUnlocksCarbonAndResolvesBaseModelToHigh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".foundation", "state")
	c := config.DefaultWorker(state)
	c.ProviderPaths["antigravity"] = filepath.Join(home, "agy")
	runner := &carbonAntigravityRunner{requireOAuthFile: true}
	manager := &Manager{Config: c, Runner: runner}

	// Write a starter OAuth token in ~/.gemini/antigravity-cli to verify GEMINI_API_KEY overrides it cleanly.
	oauthDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := secure.Dir(oauthDir); err != nil {
		t.Fatal(err)
	}
	if err := secure.Write(filepath.Join(oauthDir, "antigravity-oauth-token"), []byte("starter-oauth-token")); err != nil {
		t.Fatal(err)
	}
	if err := SaveAntigravityAPIKey(state, "AIzaSyTestKeyForCarbonVerification12345"); err != nil {
		t.Fatal(err)
	}

	health, err := manager.Check(context.Background(), "antigravity")
	if err != nil || health.Authentication != "authenticated" {
		t.Fatal("GEMINI_API_KEY readiness failed", health, err)
	}
	if !slices.Contains(health.Models, "gemini-4-carbon") || !slices.Contains(health.Models, "gemini-4-carbon-high") {
		t.Fatal("expected both bare gemini-4-carbon and effort variants in catalog", health.Models)
	}

	reply, err := manager.Invoke(context.Background(), Invocation{
		ID:       "invocation_carbon_bare",
		Provider: "antigravity",
		Path:     c.ProviderPaths["antigravity"],
		Model:    "gemini-4-carbon",
		Prompt:   "Return JSON",
		Timeout:  5e9,
	})
	if err != nil || reply.Outcome != "reviewed" || runner.authMethod != "gemini-api-key" || runner.lastSetModel != "gemini-4-carbon-high" {
		t.Fatal("bare gemini-4-carbon did not authenticate with API key and resolve to gemini-4-carbon-high", reply, runner.authMethod, runner.lastSetModel, err)
	}
}

func TestAntigravityIsolatedGeminiDirBridgesLinuxOAuthAndSyncsRefreshedToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".foundation", "state")
	c := config.DefaultWorker(state)
	c.ProviderPaths["antigravity"] = filepath.Join(home, "agy")
	runner := &carbonAntigravityRunner{requireOAuthFile: true, replaceOAuthFile: "refreshed-oauth-token"}
	manager := &Manager{Config: c, Runner: runner}

	oauthDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := secure.Dir(oauthDir); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(oauthDir, "antigravity-oauth-token")
	if err := secure.Write(tokenFile, []byte("initial-oauth-token")); err != nil {
		t.Fatal(err)
	}

	health, err := manager.Check(context.Background(), "antigravity")
	if err != nil || health.Authentication != "authenticated" {
		t.Fatal("isolated --gemini-dir failed to bridge Linux OAuth token", health, err)
	}
	updated, err := os.ReadFile(tokenFile)
	if err != nil || string(updated) != "refreshed-oauth-token" {
		t.Fatal("atomic OAuth token refresh inside isolated --gemini-dir was not synced back", string(updated), err)
	}
}

func TestAntigravityStateHomeOAuthBridgesWhenLaunchAgentHomeDiffers(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	localHome := filepath.Join(realHome, "workspace", "fdn", ".local")
	state := filepath.Join(localHome, ".foundation", "state")
	if err := secure.Dir(state); err != nil {
		t.Fatal(err)
	}
	c := config.DefaultWorker(state)
	c.ProviderPaths["antigravity"] = filepath.Join(realHome, "agy")
	runner := &carbonAntigravityRunner{requireOAuthFile: true, replaceOAuthFile: "refreshed-local-oauth"}
	manager := &Manager{Config: c, Runner: runner}

	oauthDir := filepath.Join(localHome, ".gemini", "antigravity-cli")
	if err := secure.Dir(oauthDir); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(oauthDir, "antigravity-oauth-token")
	if err := secure.Write(tokenFile, []byte("local-oauth-token")); err != nil {
		t.Fatal(err)
	}

	health, err := manager.Check(context.Background(), "antigravity")
	if err != nil || health.Authentication != "authenticated" {
		t.Fatal("expected OAuth stored under stateHome/.gemini/antigravity-cli to authenticate under real HOME", health, err)
	}
	updated, err := os.ReadFile(tokenFile)
	if err != nil || string(updated) != "refreshed-local-oauth" {
		t.Fatal("expected refreshed OAuth token to sync back to stateHome/.gemini/antigravity-cli", string(updated), err)
	}
}

var _ process.Runner = (*antigravityRunner)(nil)
