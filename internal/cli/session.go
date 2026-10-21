package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

func (a *app) sessionCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "session command required")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.Session](ctx, r.client, "GET", "/v1/sessions", nil)
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", args[1]), nil)
	case "request":
		var req domain.SessionRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/sessions", req, "")
	case "change":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.SessionChange
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("sessions", id), req, api.Path("sessions", id))
	case "open":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		fs := flag.NewFlagSet("session open", flag.ContinueOnError)
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		return a.openSession(ctx, r, id)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown session command")
	}
}

func (a *app) openSession(ctx context.Context, r *resources, id string) (any, error) {
	input, output, diagnostic, err := a.humanTerminal()
	if err != nil {
		return nil, err
	}
	req := domain.SessionChange{Action: "open"}
	if err := a.metadata(&req.Command); err != nil {
		return nil, err
	}
	data, err := a.write(ctx, r, api.Path("sessions", id), req, api.Path("sessions", id))
	if err != nil {
		return nil, err
	}
	var session domain.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	if session.State != "consented" && session.State != "active" {
		return nil, domain.Fail(domain.Prerequisite, "open action recorded; target consent is still pending; delivery is not consent")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(session.ExpiresAt, 0))
	defer cancel()
	if session.Mode == "tmux" {
		err = remote.AttachTerminal(ctx, r.client, id, input, output)
	} else {
		err = remote.AttachVNC(ctx, r.client, id, diagnostic)
	}
	current, readErr := api.Call[domain.Session](context.WithoutCancel(ctx), r.client, "GET", api.Path("sessions", id), nil)
	if readErr != nil {
		return nil, readErr
	}
	if current.State == "closed" || current.State == "revoked" || current.State == "expired" || current.State == "denied" {
		err = nil
	}
	if current.State == "active" || current.State == "consented" {
		closeReq := domain.SessionChange{Command: domain.Command{OperationID: a.operation + ".closed", ExpectedRevision: current.Revision}, Action: "close"}
		current, readErr = api.Call[domain.Session](context.WithoutCancel(ctx), r.client, "POST", api.Path("sessions", id), closeReq)
		if readErr != nil {
			var fault *domain.Fault
			if !errors.As(readErr, &fault) || fault.Code != domain.Conflict {
				return nil, readErr
			}
			current, readErr = api.Call[domain.Session](context.WithoutCancel(ctx), r.client, "GET", api.Path("sessions", id), nil)
			if readErr != nil {
				return nil, readErr
			}
			if current.State != "closed" && current.State != "revoked" && current.State != "expired" {
				return nil, domain.Fail(domain.Conflict, "session changed before closure; recheck its current state")
			}
			err = nil
		}
	}
	if ctx.Err() != nil && time.Now().Unix() >= session.ExpiresAt {
		err = nil
		current.State = "expired"
	}
	return current, err
}
