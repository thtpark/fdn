package foundation

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

type storedResult struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id,omitempty"`
	TaskID   string          `json:"task_id,omitempty"`
	Accepted bool            `json:"accepted,omitempty"`
	Stale    bool            `json:"stale,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
}

func resultReceipt(value any) ([]byte, error) {
	var ref storedResult
	switch v := value.(type) {
	case domain.Agent:
		ref = storedResult{Kind: "agents", ID: v.ID}
	case domain.Invitation:
		ref = storedResult{Kind: "invitations", ID: v.ID}
	case domain.Project:
		ref = storedResult{Kind: "projects", ID: v.ID}
	case domain.Task:
		ref = storedResult{Kind: "tasks", ID: v.ID}
	case domain.Run:
		ref = storedResult{Kind: "runs", ID: v.ID}
	case domain.Entry:
		ref = storedResult{Kind: "entries", ID: v.ID}
	case domain.Message:
		ref = storedResult{Kind: "messages", ID: v.ID}
	case domain.Session:
		ref = storedResult{Kind: "sessions", ID: v.ID}
	case domain.AssistanceGrant:
		ref = storedResult{Kind: "assistance_grants", ID: v.ID}
	case domain.ReportOutcome:
		ref = storedResult{Kind: "report", ID: v.Run.ID, TaskID: v.Task.ID, Accepted: v.Accepted, Stale: v.Stale}
	case domain.Attention, map[string]bool:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		ref = storedResult{Kind: "value", Value: data}
	default:
		return nil, domain.Fail(domain.Internal, "operation result has no bounded receipt contract")
	}
	return json.Marshal(ref)
}

func replayResult[T any](ctx context.Context, tx *sql.Tx, data []byte) (T, error) {
	var zero T
	var ref storedResult
	if err := json.Unmarshal(data, &ref); err != nil {
		return zero, err
	}
	var value any
	var err error
	switch ref.Kind {
	case "agents":
		value, err = get[domain.Agent](ctx, tx, ref.Kind, ref.ID)
	case "invitations":
		value, err = get[domain.Invitation](ctx, tx, ref.Kind, ref.ID)
	case "projects":
		value, err = get[domain.Project](ctx, tx, ref.Kind, ref.ID)
	case "tasks":
		value, err = get[domain.Task](ctx, tx, ref.Kind, ref.ID)
	case "runs":
		value, err = get[domain.Run](ctx, tx, ref.Kind, ref.ID)
	case "entries":
		value, err = get[domain.Entry](ctx, tx, ref.Kind, ref.ID)
	case "messages":
		value, err = get[domain.Message](ctx, tx, ref.Kind, ref.ID)
	case "sessions":
		value, err = get[domain.Session](ctx, tx, ref.Kind, ref.ID)
	case "assistance_grants":
		value, err = get[domain.AssistanceGrant](ctx, tx, ref.Kind, ref.ID)
	case "value":
		err = json.Unmarshal(ref.Value, &zero)
		return zero, err
	case "report":
		task, taskErr := get[domain.Task](ctx, tx, "tasks", ref.TaskID)
		if taskErr != nil {
			return zero, taskErr
		}
		run, runErr := get[domain.Run](ctx, tx, "runs", ref.ID)
		if runErr != nil {
			return zero, runErr
		}
		stale := ref.Stale || task.SpecGeneration != run.SpecGeneration
		value = domain.ReportOutcome{Accepted: ref.Accepted && !stale, Stale: stale, Task: task, Run: run}
	default:
		return zero, domain.Fail(domain.Internal, "unknown operation receipt")
	}
	if err != nil {
		return zero, err
	}
	result, ok := value.(T)
	if !ok {
		return zero, domain.Fail(domain.Internal, "operation receipt has an incompatible response type")
	}
	return result, nil
}
