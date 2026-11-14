package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

const antigravityAgent = "fdn-controller"

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
	for range 4096 {
		data, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, domain.Fail(domain.Provider, "Antigravity ACP frame exceeds its bound")
			}
			return nil, err
		}
		if len(data) > domain.MaxBody {
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

func acpNewSession(reader *bufio.Reader, writer io.Writer, workspace string) (acpSession, error) {
	result, err := acpCall(reader, writer, 2, "session/new", map[string]any{
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = m.exchangeProcess(ctx, process.Spec{Path: path, Args: antigravityACPArguments(false), Dir: directory,
		Env: process.Environment()}, func(input io.Reader, output io.Writer) error {
		reader := bufio.NewReaderSize(input, domain.MaxBody)
		if err := acpInitialize(reader, output); err != nil {
			return err
		}
		session, err := acpNewSession(reader, output, directory)
		if err != nil {
			var failure *acpError
			if errors.As(err, &failure) && failure.Code == -32000 && strings.EqualFold(failure.Message, "Authentication required") {
				return nil
			}
			return err
		}
		authenticated = true
		if session.Models != nil {
			for _, model := range session.Models.Available {
				models = append(models, model.ID)
			}
		} else {
			for _, option := range session.ConfigOptions {
				if option.Category == "model" {
					for _, model := range option.Options {
						models = append(models, model.Value)
					}
				}
			}
		}
		if _, err := acpCall(reader, output, 3, "session/close", map[string]string{"sessionId": session.SessionID}, nil); err != nil {
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
	gemini := filepath.Join(directory, "antigravity-gemini")
	relative, err := filepath.Rel(home, gemini)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return reply, domain.Fail(domain.Prerequisite, "Antigravity isolated invocation state must remain inside this computer's existing HOME")
	}
	agent := filepath.Join(gemini, "config", "agents", antigravityAgent)
	if err := secure.Dir(agent); err != nil {
		return reply, err
	}
	if err := secure.Create(filepath.Join(agent, "agent.md"), []byte(antigravityAgentDefinition(inv.Coding))); err != nil {
		return reply, err
	}
	if err := config.Write(filepath.Join(gemini, "antigravity-cli", "settings.json"), antigravitySettings(inv.Coding)); err != nil {
		return reply, err
	}
	workspace := directory
	if inv.Coding {
		if inv.Workspace == "" {
			return reply, domain.Fail(domain.Prerequisite, "Antigravity coding requires the authorized workspace")
		}
		workspace = inv.Workspace
	}
	args := append(antigravityACPArguments(inv.Coding), "--gemini-dir", filepath.ToSlash(relative), "--agent", antigravityAgent)
	environment := process.Environment()
	if inv.Coding && m.Config.SourcePath() != "" {
		environment = append(environment, config.WorkerConfigEnvironment+"="+m.Config.SourcePath())
	}
	ctx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()
	stdout, err := newInvocationOutput(filepath.Join(directory, "stdout.log"), 2<<20)
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
		reader := bufio.NewReaderSize(input, domain.MaxBody)
		if err := acpInitialize(reader, output); err != nil {
			return err
		}
		session, err := acpNewSession(reader, output, workspace)
		if err != nil {
			return err
		}
		if inv.Model != "" {
			if _, err := acpCall(reader, output, 3, "session/set_model", map[string]string{
				"sessionId": session.SessionID, "modelId": inv.Model,
			}, nil); err != nil {
				return err
			}
		}
		result, err := acpCall(reader, output, 4, "session/prompt", map[string]any{
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
		if err != nil {
			return err
		}
		var finished struct {
			Reason string `json:"stopReason"`
		}
		if err := json.Unmarshal(result, &finished); err != nil || finished.Reason != "end_turn" {
			return domain.Fail(domain.Provider, "Antigravity turn did not complete; retain native evidence without replay")
		}
		_, err = acpCall(reader, output, 5, "session/close", map[string]string{"sessionId": session.SessionID}, nil)
		return err
	})
	_, outErr := stdout.finish()
	_, diagErr := stderr.finish()
	if err := errors.Join(err, outErr, diagErr); err != nil {
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
