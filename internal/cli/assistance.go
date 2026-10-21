package cli

import (
	"context"
	"encoding/json"
	"flag"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) assistanceCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "assistance requires grant, list, show, revoke, or open")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.AssistanceGrant](ctx, r.client, "GET", "/v1/assistance", nil)
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.AssistanceGrant](ctx, r.client, "GET", api.Path("assistance", args[1]), nil)
	case "grant":
		var req domain.AssistanceGrantRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/assistance", req, "")
	case "revoke":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.AssistanceRevoke
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("assistance", id, "revoke"), req, api.Path("assistance", id))
	case "open":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		flags := flag.NewFlagSet("assistance open", flag.ContinueOnError)
		provider := flags.String("provider", "", "authentication provider (grant requirement wins)")
		ttl := flags.Int("ttl", 300, "live connection seconds, not grant lifetime")
		if err := parse(flags, tail); err != nil {
			return nil, err
		}
		if *ttl < 60 || *ttl > 900 {
			return nil, domain.Fail(domain.Invalid, "live assistance session must be 60-900 seconds")
		}
		if _, _, _, err := a.humanTerminal(); err != nil {
			return nil, err
		}
		grant, err := api.Call[domain.AssistanceGrant](ctx, r.client, "GET", api.Path("assistance", id), nil)
		if err != nil {
			return nil, err
		}
		if !grant.Active || grant.RequesterID != r.client.Credential.AgentID {
			return nil, domain.Fail(domain.Denied, "only the authorized requester may open this active assistance grant")
		}
		selected := *provider
		if selected == "" {
			selected = grant.Provider
		}
		if selected == "" && grant.Purpose == "authentication" {
			selected = r.config.Provider
		}
		if grant.Purpose == "terminal" && selected != "" {
			return nil, domain.Fail(domain.Invalid, "terminal assistance does not select an authentication provider")
		}
		if grant.Provider != "" && grant.Provider != selected {
			return nil, domain.Fail(domain.Denied, "provider is outside the grant")
		}
		key := "assistance-open:" + a.operation
		var proposal struct {
			GrantID string                `json:"grant_id"`
			Request domain.SessionRequest `json:"request"`
		}
		found, err := r.state.Get(ctx, key, &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			proposal.GrantID = id
			proposal.Request = domain.SessionRequest{Command: domain.Command{OperationID: "assist_" + domain.Hash(a.operation)}, GrantID: id, TargetID: grant.TargetID,
				ProjectID: grant.ProjectID, Mode: grant.Mode, Purpose: grant.Purpose, Provider: selected, Reason: "Explicit requester launch under retained Founder authorization", ExpiresAt: time.Now().UTC().Unix() + int64(*ttl)}
			if err := r.state.Put(ctx, key, proposal); err != nil {
				return nil, err
			}
		} else if proposal.GrantID != id || proposal.Request.Provider != selected || proposal.Request.Mode != grant.Mode || proposal.Request.Purpose != grant.Purpose {
			return nil, domain.Fail(domain.Conflict, "assistance launch operation belongs to a different exact grant/provider proposal")
		}
		data, err := a.write(ctx, r, "/v1/sessions", proposal.Request, "")
		if err != nil {
			return nil, err
		}
		var session domain.Session
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, err
		}
		return a.openSession(ctx, r, session.ID)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown assistance command")
	}
}
