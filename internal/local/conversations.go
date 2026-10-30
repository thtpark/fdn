package local

import (
	"context"
	"encoding/json"
)

func (s *State) PendingConversationActions(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM kv WHERE key LIKE 'conversation-action:%'
AND json_extract(value,'$.state') NOT IN ('done','stale') ORDER BY key LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		items = append(items, json.RawMessage(data))
	}
	return items, rows.Err()
}
