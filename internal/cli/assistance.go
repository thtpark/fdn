package cli

import (
	"context"
	"encoding/json"
	"flag"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

type assistanceOpenProposal struct {
	GrantID   string                `json:"grant_id"`
	Request   domain.SessionRequest `json:"request"`
	SessionID string                `json:"session_id,omitempty"`
}

func (a *app) assistanceCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "assistance requires request, grant, list, show, revoke, open, or status")
	}
	switch args[0] {
	case "status":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		if !domain.ValidOperation(args[1]) {
			return nil, domain.Fail(domain.Invalid, "invalid assistance operation ID")
		}
		var proposal assistanceOpenProposal
		found, err := r.state.Get(ctx, "assistance-open:"+args[1], &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.Fail(domain.NotFound, "no retained assistance launch exists for this operation")
		}
		if proposal.SessionID == "" {
			var receipt cliProposal
			found, err := r.state.Get(ctx, "cli-proposal:"+proposal.Request.OperationID, &receipt)
			if err != nil {
				return nil, err
			}
			if !found || len(receipt.Result) == 0 {
				return nil, domain.Fail(domain.Prerequisite, "session creation outcome is unknown; preserve the exact launch operation, never create a replacement automatically")
			}
			var session domain.Session
			if err := json.Unmarshal(receipt.Result, &session); err != nil {
				return nil, err
			}
			proposal.SessionID = session.ID
		}
		if proposal.SessionID == "" {
			return nil, domain.Fail(domain.Prerequisite, "retained assistance receipt has no acknowledged session reference")
		}
		return api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", proposal.SessionID), nil)
	case "request":
		var req domain.AssistanceHelpRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		if req.Mode == "" {
			req.Mode = "tmux"
		}
		if req.Mode == "vnc" {
			if err := a.publishScreens(ctx, r); err != nil {
				return nil, err
			}
		}
		return a.write(ctx, r, "/v1/assistance/requests", req, "")
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
		viewer := flags.String("viewer", "browser", "browser or native for VNC")
		listen := flags.String("listen", "127.0.0.1:0", "VNC browser loopback listener; forward the same port over SSH")
		assets := flags.String("assets-dir", "/usr/share/novnc", "absolute trusted installed noVNC assets directory")
		if err := parse(flags, tail); err != nil {
			return nil, err
		}
		if *viewer != "browser" && *viewer != "native" {
			return nil, domain.Fail(domain.Invalid, "VNC viewer must be browser or native")
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
		var proposal assistanceOpenProposal
		found, err := r.state.Get(ctx, key, &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			proposal.GrantID = id
			proposal.Request = domain.SessionRequest{Command: domain.Command{OperationID: "assist_" + domain.Hash(a.operation)}, GrantID: id, TargetID: grant.TargetID,
				ProjectID: grant.ProjectID, Mode: grant.Mode, Purpose: grant.Purpose, Provider: selected, Reason: "Explicit requester launch under retained Founder authorization", ExpiresAt: time.Now().UTC().Unix() + int64(*ttl),
				ScreenID: grant.ScreenID, ApplicationID: grant.ApplicationID, DesktopFallback: grant.DesktopFallback}
			if err := r.state.Put(ctx, key, proposal); err != nil {
				return nil, err
			}
		} else if proposal.GrantID != id || proposal.Request.Provider != selected || proposal.Request.Mode != grant.Mode || proposal.Request.Purpose != grant.Purpose ||
			proposal.Request.ScreenID != grant.ScreenID || proposal.Request.ApplicationID != grant.ApplicationID || proposal.Request.DesktopFallback != grant.DesktopFallback {
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
		proposal.SessionID = session.ID
		if err := r.state.Put(ctx, key, proposal); err != nil {
			return nil, err
		}
		return a.openSessionViewer(ctx, r, session.ID, *viewer, remote.BrowserOptions{Address: *listen, AssetsDir: *assets})
	default:
		return nil, domain.Fail(domain.Invalid, "unknown assistance command")
	}
}
