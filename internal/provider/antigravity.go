package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

const antigravityAgent = "fdn-controller"
const maxACPFrameBytes = 16 << 20

type acpFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *acpError) Error() string { return fmt.Sprintf("Antigravity ACP error %d", e.Code) }

type acpSession struct {
	SessionID string `json:"sessionId"`
	Models    *struct {
		Available []struct {
			ID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Options  []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"configOptions"`
}

func acpCall(reader *bufio.Reader, writer io.Writer, id int, method string, params any, notify func(acpFrame) error) (json.RawMessage, error) {
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return nil, err
	}
	for range 65536 {
		data, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, domain.Fail(domain.Provider, "Antigravity ACP frame exceeds its bound")
			}
			return nil, err
		}
		if len(data) > maxACPFrameBytes {
			return nil, domain.Fail(domain.Provider, "Antigravity ACP frame exceeds its bound")
		}
		var frame acpFrame
		if err := config.Decode(data, &frame); err != nil {
			return nil, domain.Fail(domain.Provider, "Antigravity returned a malformed ACP frame")
		}
		if frame.JSONRPC != "2.0" {
			return nil, domain.Fail(domain.Provider, "Antigravity ACP protocol is unsupported")
		}
		if frame.Method != "" {
			if frame.ID != nil {
				if frame.Method == "session/request_permission" {
					if err := json.NewEncoder(writer).Encode(map[string]any{
						"jsonrpc": "2.0", "id": *frame.ID,
						"result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}},
					}); err != nil {
						return nil, err
					}
					return nil, domain.Fail(domain.Prerequisite, "Antigravity requested permission outside its fixed unattended tool policy")
				}
				if err := json.NewEncoder(writer).Encode(map[string]any{
					"jsonrpc": "2.0", "id": *frame.ID,
					"error": map[string]any{"code": -32601, "message": "client capability disabled"},
				}); err != nil {
					return nil, err
				}
				return nil, domain.Fail(domain.Prerequisite, "Antigravity requested an unavailable interactive/client capability")
			}
			if notify != nil {
				if err := notify(frame); err != nil {
					return nil, err
				}
			}
			continue
		}
		if frame.ID == nil || *frame.ID != id {
			return nil, domain.Fail(domain.Provider, "Antigravity ACP response does not match its exact request")
		}
		if frame.Error != nil {
			return nil, frame.Error
		}
		if len(frame.Result) == 0 || bytes.Equal(frame.Result, []byte("null")) {
			return nil, domain.Fail(domain.Provider, "Antigravity ACP response omitted its result")
		}
		return frame.Result, nil
	}
	return nil, domain.Fail(domain.Provider, "Antigravity ACP exceeded its notification budget")
}

func acpInitialize(reader *bufio.Reader, writer io.Writer) error {
	result, err := acpCall(reader, writer, 1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "Foundation", "version": domain.Version},
	}, nil)
	if err != nil {
		return err
	}
	var response struct {
		Version int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Version != 1 {
		return domain.Fail(domain.Prerequisite, "Antigravity requires the supported ACP protocol")
	}
	return nil
}

func acpAuthenticateAPIKey(reader *bufio.Reader, writer io.Writer, id int) error {
	_, err := acpCall(reader, writer, id, "authenticate", map[string]string{"methodId": "gemini-api-key"}, nil)
	return err
}

func acpNewSession(reader *bufio.Reader, writer io.Writer, id int, workspace string) (acpSession, error) {
	result, err := acpCall(reader, writer, id, "session/new", map[string]any{
		"cwd": workspace, "mcpServers": []any{},
	}, nil)
	if err != nil {
		return acpSession{}, err
	}
	var session acpSession
	if err := json.Unmarshal(result, &session); err != nil || !domain.ValidOperation(session.SessionID) {
		return session, domain.Fail(domain.Provider, "Antigravity omitted its exact admitted session")
	}
	return session, nil
}

func AntigravityAPIKeyPath(stateDir string) string {
	return filepath.Join(stateDir, "providers", "antigravity", "gemini-api-key")
}

func ValidateGeminiAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" || len(key) > 512 {
		return "", domain.Fail(domain.Invalid, "Gemini API key must be between 1 and 512 printable ASCII characters")
	}
	for i := 0; i < len(key); i++ {
		if key[i] <= 0x20 || key[i] >= 0x7f {
			return "", domain.Fail(domain.Invalid, "Gemini API key must not contain whitespace or control characters")
		}
	}
	return key, nil
}

func SaveAntigravityAPIKey(stateDir, raw string) error {
	key, err := ValidateGeminiAPIKey(raw)
	if err != nil {
		return err
	}
	path := AntigravityAPIKeyPath(stateDir)
	if err := secure.Dir(filepath.Dir(path)); err != nil {
		return err
	}
	return secure.Write(path, []byte(key+"\n"))
}

func ClearAntigravityAPIKey(stateDir string) error {
	err := os.Remove(AntigravityAPIKeyPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func VerifyGeminiAPIKey(ctx context.Context, client *http.Client, rawKey string) error {
	key, err := ValidateGeminiAPIKey(rawKey)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return domain.Fail(domain.Unavailable, "could not reach Google Gemini API to verify key")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return domain.Fail(domain.Denied, "Gemini API key was rejected by Google Gemini API; verify the key and try again")
	}
	if resp.StatusCode != http.StatusOK {
		return domain.Fail(domain.Unavailable, "Google Gemini API key check returned HTTP "+strconv.Itoa(resp.StatusCode))
	}
	return nil
}

func (m *Manager) LoadAntigravityAPIKey() (string, error) {
	if m.Config.StateDir != "" {
		data, err := secure.Read(AntigravityAPIKeyPath(m.Config.StateDir), 1024)
		if err == nil {
			return ValidateGeminiAPIKey(string(data))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if envKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); envKey != "" {
		return ValidateGeminiAPIKey(envKey)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		data, err := secure.Read(filepath.Join(home, ".gemini", "antigravity-cli", "gemini-api-key"), 1024)
		if err == nil {
			return ValidateGeminiAPIKey(string(data))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

func collectAntigravityModels(session acpSession) []string {
	var available []string
	if session.Models != nil {
		for _, model := range session.Models.Available {
			if model.ID != "" && !slices.Contains(available, model.ID) {
				available = append(available, model.ID)
			}
		}
	}
	var baseOptions []string
	for _, option := range session.ConfigOptions {
		if option.Category == "model" {
			for _, model := range option.Options {
				if model.Value != "" && !slices.Contains(baseOptions, model.Value) {
					baseOptions = append(baseOptions, model.Value)
				}
			}
		}
	}
	if len(available) == 0 {
		return baseOptions
	}
	var models []string
	insertedBase := map[string]bool{}
	for _, id := range available {
		for _, base := range baseOptions {
			if !insertedBase[base] && (id == base || strings.HasPrefix(id, base+"-")) {
				if !slices.Contains(models, base) {
					models = append(models, base)
				}
				insertedBase[base] = true
			}
		}
		if !slices.Contains(models, id) {
			models = append(models, id)
		}
	}
	for _, base := range baseOptions {
		if !slices.Contains(models, base) {
			models = append(models, base)
		}
	}
	return models
}

func resolveAntigravitySessionModel(requested string, session acpSession) string {
	if requested == "" || session.Models == nil {
		return requested
	}
	for _, item := range session.Models.Available {
		if item.ID == requested {
			return requested
		}
	}
	highCandidate := requested + "-high"
	for _, item := range session.Models.Available {
		if item.ID == highCandidate {
			return highCandidate
		}
	}
	return requested
}

var antigravitySharedAuthFiles = []string{
	"antigravity-oauth-token",
	"installation_id",
	"jetski_state.pbtxt",
	"google_accounts.json",
	"oauth_creds.json",
}

func (m *Manager) prepareAntigravityGeminiDir(home, root string, coding, writeAgent bool) (relative, apiKey string, syncBack func() error, err error) {
	syncBack = func() error { return nil }
	apiKey, err = m.LoadAntigravityAPIKey()
	if err != nil {
		return "", "", syncBack, err
	}
	gemini := filepath.Join(root, "antigravity-gemini")
	relative, err = filepath.Rel(home, gemini)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", syncBack, domain.Fail(domain.Prerequisite, "Antigravity isolated invocation state must remain inside this computer's existing HOME")
	}
	cliDir := filepath.Join(gemini, "antigravity-cli")
	if err := secure.Dir(cliDir); err != nil {
		return "", "", syncBack, err
	}
	if writeAgent {
		agent := filepath.Join(gemini, "config", "agents", antigravityAgent)
		if err := secure.Dir(agent); err != nil {
			return "", "", syncBack, err
		}
		if err := secure.Create(filepath.Join(agent, "agent.md"), []byte(antigravityAgentDefinition(coding))); err != nil {
			return "", "", syncBack, err
		}
		if err := config.Write(filepath.Join(cliDir, "settings.json"), antigravitySettings(coding)); err != nil {
			return "", "", syncBack, err
		}
	}
	if apiKey == "" {
		sourceCLI := filepath.Join(home, ".gemini", "antigravity-cli")
		for _, name := range antigravitySharedAuthFiles {
			src := filepath.Join(sourceCLI, name)
			if _, statErr := os.Stat(src); statErr != nil {
				continue
			}
			dst := filepath.Join(cliDir, name)
			if err := os.Symlink(src, dst); err != nil && !errors.Is(err, os.ErrExist) {
				return "", "", syncBack, err
			}
		}
		syncBack = func() error {
			dstToken := filepath.Join(cliDir, "antigravity-oauth-token")
			info, statErr := os.Lstat(dstToken)
			if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<10 {
				return nil
			}
			refreshed, readErr := os.ReadFile(dstToken)
			if readErr != nil || len(refreshed) == 0 {
				return readErr
			}
			if err := secure.Dir(sourceCLI); err != nil {
				return err
			}
			return secure.Write(filepath.Join(sourceCLI, "antigravity-oauth-token"), refreshed)
		}
	}
	return filepath.ToSlash(relative), apiKey, syncBack, nil
}

func isAntigravityAuthRequired(err error) bool {
	var failure *acpError
	return errors.As(err, &failure) && failure.Code == -32000 && strings.Contains(strings.ToLower(failure.Message), "authentication required")
}

func antigravityACPArguments(coding bool) []string {
	args := []string{"acp", "--stdio", "--include-default-customizations=false", "--disable-telemetry"}
	if coding {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

func (m *Manager) antigravityReadiness(ctx context.Context, path string) (authenticated bool, models []string, err error) {
	directory := filepath.Join(m.Config.StateDir, "providers", "antigravity", "readiness")
	if err := secure.Dir(directory); err != nil {
		return false, nil, err
	}
	apiKey, err := m.LoadAntigravityAPIKey()
	if err != nil {
		return false, nil, err
	}
	args := antigravityACPArguments(false)
	env := process.Environment()
	var stage string
	var syncBack func() error
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		if rel, relErr := filepath.Rel(home, directory); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			stage, err = os.MkdirTemp(directory, ".probe-")
			if err != nil {
				return false, nil, err
			}
			defer func() { _ = os.RemoveAll(stage) }()
			var relative string
			relative, apiKey, syncBack, err = m.prepareAntigravityGeminiDir(home, stage, false, false)
			if err != nil {
				return false, nil, err
			}
			args = append(args, "--gemini-dir", relative)
		}
	}
	if apiKey != "" {
		env = process.Environment("GEMINI_API_KEY=" + apiKey)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = m.exchangeProcess(ctx, process.Spec{Path: path, Args: args, Dir: directory,
		Env: env}, func(input io.Reader, output io.Writer) error {
		reader := bufio.NewReaderSize(input, domain.MaxBody)
		if err := acpInitialize(reader, output); err != nil {
			return err
		}
		nextID := 2
		if apiKey != "" {
			if err := acpAuthenticateAPIKey(reader, output, nextID); err != nil {
				if isAntigravityAuthRequired(err) {
					return nil
				}
				return err
			}
			nextID++
		}
		session, err := acpNewSession(reader, output, nextID, directory)
		nextID++
		if err != nil {
			if isAntigravityAuthRequired(err) {
				return nil
			}
			return err
		}
		authenticated = true
		models = collectAntigravityModels(session)
		if _, err := acpCall(reader, output, nextID, "session/close", map[string]string{"sessionId": session.SessionID}, nil); err != nil {
			return err
		}
		if len(models) == 0 || len(models) > domain.MaxProviderModels {
			return domain.Fail(domain.Unavailable, "Antigravity read-only model catalog is unavailable")
		}
		for _, model := range models {
			if !domain.ValidModelID(model) {
				return domain.Fail(domain.Provider, "Antigravity returned an unsafe model identifier")
			}
		}
		return nil
	})
	if syncBack != nil {
		err = errors.Join(err, syncBack())
	}
	return
}

func antigravityAgentDefinition(coding bool) string {
	tools, execution := "[]", "off"
	if coding {
		tools = "[view_file, grep_search, run_command, replace_file_content]"
		execution = "eager"
	}
	return "---\nname: " + antigravityAgent + "\ndescription: Foundation source-bound controller adapter.\n" +
		"tools: " + tools + "\nmainAgent: true\nsubagent: false\nmodel: inherit\ncommandExecutionPolicy: " + execution +
		"\nmcpServers: []\nskills: []\nplugins: []\n---\n" +
		"Follow the supplied Foundation context and return exactly its structured JSON reply. " +
		"Never delegate to subagents, open credential/consent UI, or expand task, OS or spending authority.\n"
}

func antigravitySettings(coding bool) map[string]any {
	deny := []string{"mcp(*)"}
	policy := "always-proceed"
	if !coding {
		policy = "strict"
		deny = []string{"command(*)", "read_file(*)", "write_file(*)", "read_url(*)", "execute_url(*)", "mcp(*)", "unsandboxed(*)"}
	}
	return map[string]any{"toolPermission": policy, "disableSlashCommands": true,
		"permissions": map[string]any{"allow": []string{}, "ask": []string{}, "deny": deny}}
}

func (m *Manager) invokeAntigravity(ctx context.Context, inv Invocation, directory string) (reply Reply, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return reply, err
	}
	relative, apiKey, syncBack, err := m.prepareAntigravityGeminiDir(home, directory, inv.Coding, true)
	if err != nil {
		return reply, err
	}
	workspace := directory
	if inv.Coding {
		if inv.Workspace == "" {
			return reply, domain.Fail(domain.Prerequisite, "Antigravity coding requires the authorized workspace")
		}
		workspace = inv.Workspace
	}
	args := append(antigravityACPArguments(inv.Coding), "--gemini-dir", relative, "--agent", antigravityAgent)
	environment := invocationEnvironment(m.Config, inv)
	if apiKey != "" {
		environment = append(environment, "GEMINI_API_KEY="+apiKey)
	}
	ctx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()
	stdout, err := newInvocationOutput(filepath.Join(directory, "stdout.log"), 16<<20)
	if err != nil {
		return reply, err
	}
	stderr, err := newInvocationOutput(filepath.Join(directory, "stderr.log"), 256<<10)
	if err != nil {
		_, closeErr := stdout.finish()
		return reply, errors.Join(err, closeErr)
	}
	var response strings.Builder
	err = m.exchangeProcess(ctx, process.Spec{Path: inv.Path, Args: args, Dir: workspace, Env: environment,
		Stdout: stdout, Stderr: stderr, Started: inv.Started}, func(input io.Reader, output io.Writer) error {
		reader := bufio.NewReaderSize(input, maxACPFrameBytes)
		if err := acpInitialize(reader, output); err != nil {
			return err
		}
		nextID := 2
		if apiKey != "" {
			if err := acpAuthenticateAPIKey(reader, output, nextID); err != nil {
				if isAntigravityAuthRequired(err) {
					return domain.Fail(domain.Prerequisite, "Antigravity authentication is required; complete sign-in or configure GEMINI_API_KEY")
				}
				return err
			}
			nextID++
		}
		session, err := acpNewSession(reader, output, nextID, workspace)
		nextID++
		if err != nil {
			if isAntigravityAuthRequired(err) {
				return domain.Fail(domain.Prerequisite, "Antigravity authentication is required; complete sign-in or configure GEMINI_API_KEY")
			}
			return err
		}
		if inv.Model != "" {
			selectedModel := resolveAntigravitySessionModel(inv.Model, session)
			if _, err := acpCall(reader, output, nextID, "session/set_model", map[string]string{
				"sessionId": session.SessionID, "modelId": selectedModel,
			}, nil); err != nil {
				return err
			}
			nextID++
		}
		result, err := acpCall(reader, output, nextID, "session/prompt", map[string]any{
			"sessionId": session.SessionID, "prompt": []map[string]string{{"type": "text", "text": inv.Prompt}},
		}, func(frame acpFrame) error {
			if frame.Method != "session/update" {
				return nil
			}
			var update struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					Type    string `json:"sessionUpdate"`
					Content struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if err := json.Unmarshal(frame.Params, &update); err != nil || update.SessionID != session.SessionID {
				return domain.Fail(domain.Provider, "Antigravity update belongs to a different session")
			}
			if !inv.Coding && (update.Update.Type == "tool_call" || update.Update.Type == "tool_call_update") {
				return domain.Fail(domain.Provider, "Antigravity text-only call attempted a disabled tool")
			}
			if update.Update.Type == "agent_message_chunk" && update.Update.Content.Type == "text" {
				if response.Len()+len(update.Update.Content.Text) > 256<<10 {
					return domain.Fail(domain.Provider, "Antigravity final structured reply exceeds its bound")
				}
				response.WriteString(update.Update.Content.Text)
			}
			return nil
		})
		nextID++
		if err != nil {
			return err
		}
		var finished struct {
			Reason string `json:"stopReason"`
		}
		if err := json.Unmarshal(result, &finished); err != nil || finished.Reason != "end_turn" {
			return domain.Fail(domain.Provider, "Antigravity turn did not complete; retain native evidence without replay")
		}
		_, err = acpCall(reader, output, nextID, "session/close", map[string]string{"sessionId": session.SessionID}, nil)
		return err
	})
	_, outErr := stdout.finish()
	_, diagErr := stderr.finish()
	if err := errors.Join(err, outErr, diagErr, syncBack()); err != nil {
		return reply, err
	}
	data := []byte(strings.TrimSpace(response.String()))
	if err := config.Decode(data, &reply); err != nil {
		return reply, domain.Fail(domain.Provider, "Antigravity did not return the required structured reply")
	}
	if err := reply.Validate(); err != nil {
		return reply, err
	}
	return reply, secure.Create(filepath.Join(directory, "reply.json"), data)
}
