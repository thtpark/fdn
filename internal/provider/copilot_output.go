package provider

import (
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

func copilotFinalResponse(output []byte) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var response []byte
	var apiCallID, phase string
	var nextChunk, chunkCount int
	var complete, hasTools bool
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if complete {
			return nil, domain.Fail(domain.Provider, "Copilot emitted events after its final result")
		}
		var event struct {
			Type     string          `json:"type"`
			AgentID  string          `json:"agentId"`
			Data     json.RawMessage `json:"data"`
			ExitCode *int            `json:"exitCode"`
			IsError  bool            `json:"isError"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return nil, domain.Fail(domain.Provider, "Copilot did not emit valid machine-output events")
		}
		if event.AgentID != "" {
			continue
		}
		switch event.Type {
		case "assistant.message":
			var message struct {
				APICallID        string            `json:"apiCallId"`
				Content          string            `json:"content"`
				Phase            string            `json:"phase"`
				ParentToolCallID string            `json:"parentToolCallId"`
				ChunkIndex       *int              `json:"chunkIndex"`
				ChunkCount       *int              `json:"chunkCount"`
				ToolRequests     []json.RawMessage `json:"toolRequests"`
			}
			if err := json.Unmarshal(event.Data, &message); err != nil || bytes.Equal(bytes.TrimSpace(event.Data), []byte("null")) {
				return nil, domain.Fail(domain.Provider, "Copilot emitted an invalid assistant message")
			}
			if message.ParentToolCallID != "" {
				continue
			}
			index, count := 0, 1
			if message.ChunkIndex != nil || message.ChunkCount != nil {
				if message.ChunkIndex == nil || message.ChunkCount == nil {
					return nil, domain.Fail(domain.Provider, "Copilot omitted assistant-message chunk metadata")
				}
				index, count = *message.ChunkIndex, *message.ChunkCount
			}
			if index < 0 || count < 1 || index >= count || (count > 1 && message.APICallID == "") {
				return nil, domain.Fail(domain.Provider, "Copilot emitted invalid assistant-message chunks")
			}
			if index == 0 {
				if nextChunk != chunkCount {
					return nil, domain.Fail(domain.Provider, "Copilot replaced an incomplete assistant message")
				}
				apiCallID, nextChunk, chunkCount = message.APICallID, 0, count
			} else if index != nextChunk || count != chunkCount || message.APICallID != apiCallID {
				return nil, domain.Fail(domain.Provider, "Copilot assistant-message chunks do not match")
			}
			response, phase, hasTools = []byte(message.Content), message.Phase, len(message.ToolRequests) != 0
			nextChunk++
		case "result":
			if event.ExitCode == nil || *event.ExitCode != 0 || event.IsError {
				return nil, domain.Fail(domain.Provider, "Copilot machine output did not confirm successful completion")
			}
			complete = true
		case "error", "session.error":
			return nil, domain.Fail(domain.Provider, "Copilot machine output reported a failed invocation")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, domain.Fail(domain.Provider, "Copilot machine output exceeds its event size limit")
	}
	if !complete || nextChunk == 0 || nextChunk != chunkCount || hasTools || len(bytes.TrimSpace(response)) == 0 {
		return nil, domain.Fail(domain.Provider, "Copilot omitted a complete final assistant response")
	}
	if phase != "" && phase != "final_answer" {
		return nil, domain.Fail(domain.Provider, "Copilot did not finish in a final-answer phase")
	}
	if len(response) > 256<<10 {
		return nil, domain.Fail(domain.Provider, "Copilot final response exceeds its size limit")
	}
	return response, nil
}
