package local

import (
	"context"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

type AgentOperationReceipt struct {
	Version         int                         `json:"version"`
	OperationID     string                      `json:"operation_id"`
	RequestHash     string                      `json:"request_hash"`
	Report          domain.AgentOperationReport `json:"report"`
	DeliveryState   string                      `json:"delivery_state"`
	DeliveryProblem domain.Code                 `json:"delivery_problem,omitempty"`
}

func AgentOperationResultKey(id string) string { return "agent-operation-result:" + id }

func (s *State) PendingAgentOperationResults(ctx context.Context, limit int) ([]AgentOperationReceipt, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.Fail(domain.Invalid, "agent operation result page must be between 1 and 100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM kv WHERE key LIKE 'agent-operation-result:%' AND json_extract(value, '$.delivery_state')='pending' ORDER BY key LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentOperationReceipt{}
	for rows.Next() {
		var data []byte
		var item AgentOperationReceipt
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		if item.Version != domain.AgentOperationVersion || !domain.ValidOperation(item.OperationID) || !domain.ValidHash(item.RequestHash) {
			return nil, domain.Fail(domain.Prerequisite, "unsupported retained agent operation result; preserve evidence for an authorized operator")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
