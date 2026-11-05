package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func lineRPC(reader *bufio.Scanner, writer io.Writer, id int, method string, params any) (json.RawMessage, error) {
	if err := json.NewEncoder(writer).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for range 32 {
		if !reader.Scan() {
			if err := reader.Err(); err != nil {
				return nil, err
			}
			return nil, io.ErrUnexpectedEOF
		}
		var response struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(reader.Bytes(), &response); err != nil {
			return nil, err
		}
		if response.ID == nil && response.Method != "" {
			continue
		}
		if response.ID == nil || *response.ID != id || response.Method != "" {
			return nil, domain.Fail(domain.Provider, "provider model catalog response did not match its request")
		}
		if len(response.Error) != 0 && string(response.Error) != "null" {
			return nil, domain.Fail(domain.Unavailable, "provider model catalog RPC failed")
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return nil, domain.Fail(domain.Provider, "provider model catalog response omitted its result")
		}
		return response.Result, nil
	}
	return nil, domain.Fail(domain.Provider, "too many unsolicited provider model catalog frames")
}

func (m *Manager) codexModels(ctx context.Context, path string) (models []string, err error) {
	models = []string{}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = m.statusProcess(ctx, path, []string{"app-server", "--listen", "stdio://"}, func(input io.Reader, output io.Writer) error {
		reader := bufio.NewScanner(input)
		reader.Buffer(make([]byte, 4096), 256<<10)
		if _, err := lineRPC(reader, output, 1, "initialize", map[string]any{
			"clientInfo": map[string]string{"name": "fdn", "title": "Foundation", "version": domain.Version},
		}); err != nil {
			return err
		}
		if err := json.NewEncoder(output).Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
			return err
		}
		cursor := ""
		seen := map[string]bool{}
		for page := range 16 {
			params := map[string]any{"limit": 100, "includeHidden": false}
			if cursor != "" {
				params["cursor"] = cursor
			}
			data, err := lineRPC(reader, output, page+2, "model/list", params)
			if err != nil {
				return err
			}
			var result struct {
				Data []struct {
					Model string `json:"model"`
				} `json:"data"`
				NextCursor string `json:"nextCursor"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return err
			}
			if result.Data == nil || len(result.Data)+len(models) > 1000 {
				return domain.Fail(domain.Provider, "invalid provider model catalog")
			}
			for _, model := range result.Data {
				if err := domain.CheckText(model.Model, "model ID", 256, true); err != nil {
					return err
				}
				models = append(models, model.Model)
			}
			if result.NextCursor == "" {
				return nil
			}
			if err := domain.CheckText(result.NextCursor, "model catalog cursor", 4096, true); err != nil {
				return err
			}
			if seen[result.NextCursor] {
				return domain.Fail(domain.Provider, "provider repeated its model catalog cursor")
			}
			cursor, seen[result.NextCursor] = result.NextCursor, true
		}
		return domain.Fail(domain.Provider, "provider model catalog exceeds its page budget")
	})
	return
}
