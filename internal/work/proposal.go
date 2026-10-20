package work

import (
	"context"
	"encoding/json"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

type proposal struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
	Result json.RawMessage `json:"result,omitempty"`
}

func semantic(data []byte) (string, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	delete(value, "expected_revision")
	canonical, err := json.Marshal(value)
	return string(canonical), err
}

func commit[T any](ctx context.Context, e *Engine, method, path string, input any) (T, error) {
	var zero T
	data, err := json.Marshal(input)
	if err != nil {
		return zero, err
	}
	var command domain.Command
	if err := json.Unmarshal(data, &command); err != nil {
		return zero, err
	}
	if !domain.ValidOperation(command.OperationID) {
		return zero, domain.Fail(domain.Invalid, "durable proposal requires an operation ID")
	}
	key := "proposal:" + command.OperationID
	p := proposal{}
	found, err := e.State.Get(ctx, key, &p)
	if err != nil {
		return zero, err
	}
	if found {
		left, err := semantic(p.Body)
		if err != nil {
			return zero, err
		}
		right, err := semantic(data)
		if err != nil {
			return zero, err
		}
		if p.Method != method || p.Path != path || left != right {
			return zero, domain.Fail(domain.Conflict, "prepared operation was reused for a different semantic decision")
		}
	} else {
		p = proposal{Method: method, Path: path, Body: data}
		if err := e.State.Put(ctx, key, p); err != nil {
			return zero, err
		}
	}
	if len(p.Result) != 0 {
		err := json.Unmarshal(p.Result, &zero)
		return zero, err
	}
	raw, err := api.Call[json.RawMessage](ctx, e.Client, method, path, p.Body)
	if err != nil {
		return zero, err
	}
	p.Result = raw
	if err := e.State.Put(ctx, key, p); err != nil {
		return zero, err
	}
	err = json.Unmarshal(raw, &zero)
	return zero, err
}

func (e *Engine) changePrepared(ctx context.Context, t domain.Task, operation, kind, reason string, set func(*domain.TaskChange)) (domain.Task, error) {
	req := domain.TaskChange{Command: domain.Command{OperationID: operation, ExpectedRevision: t.Revision}, Kind: kind, Reason: reason}
	if set != nil {
		set(&req)
	}
	return commit[domain.Task](ctx, e, "POST", api.Path("tasks", t.ID, "change"), req)
}
