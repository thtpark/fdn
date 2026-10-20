package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

func rpc(reader *bufio.Reader, writer io.Writer, id int, method string, params any) (json.RawMessage, error) {
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n%s", len(request), request); err != nil {
		return nil, err
	}
	for range 32 {
		length := -1
		headerBytes := 0
		for {
			lineBytes, err := reader.ReadSlice('\n')
			if err != nil {
				if errors.Is(err, bufio.ErrBufferFull) {
					return nil, domain.Fail(domain.Provider, "oversized provider RPC header")
				}
				return nil, err
			}
			line := string(lineBytes)
			headerBytes += len(line)
			if headerBytes > 2048 {
				return nil, domain.Fail(domain.Provider, "oversized provider RPC header")
			}
			if line == "\r\n" {
				break
			}
			key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r\n"), ":")
			if !ok {
				return nil, domain.Fail(domain.Provider, "malformed provider RPC header")
			}
			if strings.EqualFold(key, "Content-Length") {
				if length != -1 {
					return nil, domain.Fail(domain.Provider, "repeated provider RPC length")
				}
				length, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, domain.Fail(domain.Provider, "invalid provider RPC length")
				}
			}
		}
		if length < 1 || length > 256<<10 {
			return nil, domain.Fail(domain.Provider, "provider RPC frame exceeds its limit")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, err
		}
		if response.JSONRPC != "2.0" {
			return nil, domain.Fail(domain.Provider, "unsupported provider RPC protocol")
		}
		if response.ID == nil && response.Method != "" {
			continue
		}
		if response.ID == nil || *response.ID != id || response.Method != "" {
			return nil, domain.Fail(domain.Provider, "provider RPC response did not match its request")
		}
		if len(response.Error) != 0 {
			return nil, domain.Fail(domain.Unavailable, "provider authentication/catalog RPC failed")
		}
		if len(response.Result) == 0 {
			return nil, domain.Fail(domain.Provider, "provider RPC response omitted its result")
		}
		return response.Result, nil
	}
	return nil, domain.Fail(domain.Provider, "too many unsolicited provider RPC frames")
}

func (m *Manager) copilotAuthentication(ctx context.Context, path string) (authenticated bool, models []string, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	diagnostic := process.NewBuffer(8192)
	go func() {
		runErr := m.Runner.Run(ctx, process.Spec{Path: path, Args: []string{"--headless", "--stdio", "--no-auto-update", "--log-level", "none"}, Env: process.Environment(),
			Stdin: stdinReader, Stdout: stdoutWriter, Stderr: diagnostic})
		stdinReader.CloseWithError(runErr)
		stdoutWriter.CloseWithError(runErr)
		done <- runErr
	}()
	defer func() {
		cancel()
		stdinWriter.Close()
		stdoutReader.Close()
		runErr := <-done
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			err = errors.Join(err, domain.Fail(domain.Unavailable, "provider status process failed"))
		}
	}()
	reader := bufio.NewReaderSize(stdoutReader, 4096)
	connected, err := rpc(reader, stdinWriter, 1, "connect", map[string]any{"clientInfo": map[string]string{"editorName": "fdn", "editorVersion": domain.Version}})
	if err != nil {
		return false, nil, err
	}
	var protocol struct {
		Version int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(connected, &protocol); err != nil {
		return false, nil, err
	}
	if protocol.Version < 3 || protocol.Version > 20 {
		return false, nil, domain.Fail(domain.Prerequisite, "unsupported Copilot SDK protocol version")
	}
	status, err := rpc(reader, stdinWriter, 2, "auth.getStatus", map[string]any{})
	if err != nil {
		return false, nil, err
	}
	var auth struct {
		Authenticated *bool `json:"isAuthenticated"`
	}
	if err := json.Unmarshal(status, &auth); err != nil {
		return false, nil, err
	}
	if auth.Authenticated == nil {
		return false, nil, domain.Fail(domain.Provider, "provider omitted authentication state")
	}
	if !*auth.Authenticated {
		return false, []string{}, nil
	}
	catalog, err := rpc(reader, stdinWriter, 3, "models.list", map[string]any{})
	if err != nil {
		return false, nil, err
	}
	var result struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(catalog, &result); err != nil {
		return false, nil, err
	}
	if len(result.Models) == 0 || len(result.Models) > 1000 {
		return false, nil, domain.Fail(domain.Provider, "invalid provider model catalog")
	}
	models = []string{}
	for _, model := range result.Models {
		if err := domain.CheckText(model.ID, "model ID", 256, true); err != nil {
			return false, nil, err
		}
		models = append(models, model.ID)
	}
	return true, models, nil
}
