package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

type sharingOpenProposal struct {
	Request   domain.SessionRequest `json:"request"`
	SessionID string                `json:"session_id"`
}

func (a *app) sharingCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "share requires show, sessions, publish, terminal, select, approve, revoke, request, open, stream, input or release-input")
	}
	switch args[0] {
	case "status":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		if !domain.ValidOperation(args[1]) {
			return nil, domain.Fail(domain.Invalid, "sharing status requires the exact original operation ID")
		}
		var proposal sharingOpenProposal
		found, err := r.state.Get(ctx, "sharing-open:"+args[1], &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.Fail(domain.NotFound, "no retained sharing launch exists for this operation")
		}
		if proposal.SessionID == "" {
			var receipt cliProposal
			found, err := r.state.Get(ctx, "cli-proposal:"+proposal.Request.OperationID, &receipt)
			if err != nil {
				return nil, err
			}
			if !found || len(receipt.Result) == 0 {
				return nil, domain.Fail(domain.Prerequisite, "sharing creation outcome is unknown; preserve and reconcile the same exact operation")
			}
			var session domain.Session
			if err := json.Unmarshal(receipt.Result, &session); err != nil {
				return nil, err
			}
			proposal.SessionID = session.ID
		}
		return api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", proposal.SessionID), nil)
	case "help":
		var request domain.AssistanceHelpRequest
		if err := a.input(args[1:], &request); err != nil {
			return nil, err
		}
		if request.Purpose != "" && request.Purpose != "sharing" {
			return nil, domain.Fail(domain.Invalid, "share help is for the currently selected unified sharing resource")
		}
		request.Purpose = "sharing"
		if err := a.metadata(&request.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/assistance/requests", request, "")
	case "show", "sessions":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		path := api.Path("agents", args[1], "sharing")
		if args[0] == "sessions" {
			return api.Call[[]domain.Session](ctx, r.client, "GET", path+"/sessions", nil)
		}
		return api.Call[domain.Sharing](ctx, r.client, "GET", path, nil)
	case "publish":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		manager := remote.Manager{Config: r.config, State: r.state, Client: r.client}
		if err := manager.PublishSharing(ctx); err != nil {
			return nil, err
		}
		return api.Call[domain.Sharing](ctx, r.client, "GET", api.Path("agents", r.client.Credential.AgentID, "sharing"), nil)
	case "terminal":
		var request struct {
			Name         string `json:"name"`
			WorkspaceRef string `json:"workspace_ref"`
			Protected    bool   `json:"protected"`
		}
		if err := a.input(args[1:], &request); err != nil {
			return nil, err
		}
		terminal, err := remote.RequestSharingTerminal(ctx, r.state, a.operation, request.Name, request.WorkspaceRef, request.Protected)
		if err != nil {
			return nil, err
		}
		manager := remote.Manager{Config: r.config, State: r.state, Client: r.client}
		if err := manager.PublishSharing(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"state": "published", "terminal": terminal}, nil
	case "select":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var request domain.SharingSelectRequest
		if err := a.input(tail, &request); err != nil {
			return nil, err
		}
		if err := a.metadata(&request.Command); err != nil {
			return nil, err
		}
		path := api.Path("agents", id, "sharing")
		return a.write(ctx, r, path+"/select", request, path)
	case "approve":
		var request domain.AssistanceGrantRequest
		if err := a.input(args[1:], &request); err != nil {
			return nil, err
		}
		if request.Purpose != "" && request.Purpose != "sharing" {
			return nil, domain.Fail(domain.Invalid, "share approve issues only bounded sharing permission")
		}
		request.Purpose = "sharing"
		if err := a.metadata(&request.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/assistance", request, "")
	case "revoke":
		return a.assistanceCommand(ctx, r, append([]string{"revoke"}, args[1:]...))
	case "input", "release-input":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var request domain.SessionChange
		if err := a.input(tail, &request); err != nil {
			return nil, err
		}
		action := "request-input"
		if args[0] == "release-input" {
			action = "revoke-input"
		}
		if request.Action != "" && request.Action != action {
			return nil, domain.Fail(domain.Invalid, "sharing input command does not accept a different session action")
		}
		request.Action = action
		if err := a.metadata(&request.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("sessions", id), request, api.Path("sessions", id))
	case "request", "open":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		flags := flag.NewFlagSet("share "+args[0], flag.ContinueOnError)
		mode := flags.String("mode", "tmux", "selected terminal or screen: tmux/vnc")
		grant := flags.String("grant", "", "fixed-expiry Founder approval; Founder viewing needs none")
		lifetime := flags.Int64("ttl", domain.MaxSharingSeconds, "session lifetime in seconds, always capped by delegate approval")
		viewer := flags.String("viewer", "browser", "browser/native for VNC")
		listen := flags.String("listen", "127.0.0.1:0", "local VNC viewer address")
		assets := flags.String("assets-dir", "/usr/share/novnc", "trusted installed noVNC assets")
		if err := parse(flags, tail); err != nil {
			return nil, err
		}
		if *viewer != "browser" && *viewer != "native" {
			return nil, domain.Fail(domain.Invalid, "VNC viewer must be browser or native")
		}
		if args[0] == "open" {
			if _, _, _, err := a.humanTerminal(); err != nil {
				return nil, err
			}
		}
		key := "sharing-open:" + a.operation
		var proposal sharingOpenProposal
		found, err := r.state.Get(ctx, key, &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			request, err := api.PrepareSharing(ctx, r.client, id, *mode, *grant, *lifetime, "Explicit selected-resource sharing through Foundation")
			if err != nil {
				return nil, err
			}
			request.Command.OperationID = "sharing_" + domain.Hash(a.operation)
			proposal.Request = request
			if err := r.state.Put(ctx, key, proposal); err != nil {
				return nil, err
			}
		} else if proposal.Request.TargetID != id || proposal.Request.Mode != *mode || proposal.Request.GrantID != *grant || proposal.Request.LifetimeSeconds != *lifetime {
			return nil, domain.Fail(domain.Conflict, "sharing operation belongs to another exact target/mode/approval/lifetime")
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
		if args[0] == "request" {
			return session, nil
		}
		return a.openSharingViewer(ctx, r, session.ID, *viewer, remote.BrowserOptions{Address: *listen, AssetsDir: *assets})
	case "stream":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		if a.json {
			return nil, domain.Fail(domain.Invalid, "sharing relay bytes cannot be captured as JSON")
		}
		session, err := api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", args[1]), nil)
		if err != nil {
			return nil, err
		}
		if !session.Sharing() {
			return nil, domain.Fail(domain.Denied, "programmatic relay supports only approved ordinary sharing, never protected assistance")
		}
		selection, err := api.Call[domain.Sharing](ctx, r.client, "GET", api.Path("agents", session.TargetID, "sharing"), nil)
		if err != nil {
			return nil, err
		}
		protected := selection.Protected
		for _, terminal := range selection.Terminals {
			protected = protected || (terminal.ID == session.TerminalID && terminal.Protected)
		}
		if protected {
			return nil, domain.Fail(domain.Denied, "protected UI requires the human Founder viewer, not a programmatic byte stream")
		}
		if !session.OpenRequested {
			request := domain.SessionChange{Action: "open"}
			if err := a.metadata(&request.Command); err != nil {
				return nil, err
			}
			if _, err := a.write(ctx, r, api.Path("sessions", session.ID), request, api.Path("sessions", session.ID)); err != nil {
				return nil, err
			}
		}
		if _, err := api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", session.ID, "access"), nil); err != nil {
			return nil, err
		}
		input, ok := a.in.(*os.File)
		if !ok {
			return nil, domain.Fail(domain.Prerequisite, "binary sharing needs native streaming stdin")
		}
		a.nativeOutput = true
		return nil, remote.StreamSharing(ctx, r.client, session.ID, input, a.out)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown sharing command")
	}
}

func (a *app) openSharingViewer(ctx context.Context, r *resources, id, viewer string, browser remote.BrowserOptions) (any, error) {
	input, output, diagnostic, err := a.humanTerminal()
	if err != nil {
		return nil, err
	}
	request := domain.SessionChange{Action: "open"}
	if err := a.metadata(&request.Command); err != nil {
		return nil, err
	}
	if _, err := a.write(ctx, r, api.Path("sessions", id), request, api.Path("sessions", id)); err != nil {
		return nil, err
	}
	started := time.Now()
	session, err := api.Call[domain.Session](ctx, r.client, "GET", api.Path("sessions", id), nil)
	if err != nil {
		return nil, err
	}
	deadline, err := session.SharingDeadline(started)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	a.nativeOutput = true
	for {
		detached := true
		if session.Mode == "tmux" {
			detached, err = remote.AttachSharingTerminal(ctx, r.client, id, input, output)
		} else if viewer == "native" {
			err = remote.AttachVNC(ctx, r.client, id, diagnostic)
		} else {
			err = remote.AttachBrowserVNC(ctx, r.client, id, browser, diagnostic)
		}
		current, readErr := api.Call[domain.Session](context.WithoutCancel(ctx), r.client, "GET", api.Path("sessions", id), nil)
		if readErr != nil {
			return nil, errors.Join(err, readErr)
		}
		if !detached && ctx.Err() == nil && (current.State == "active" || current.State == "consented") {
			session = current
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if current.State == "active" || current.State == "consented" {
			current, readErr = api.Call[domain.Session](context.WithoutCancel(ctx), r.client, "POST", api.Path("sessions", id), domain.SessionChange{
				Command: domain.Command{OperationID: a.operation + ".sharing-closed", ExpectedRevision: current.Revision}, Action: "close"})
		}
		if current.State == "expired" || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		return current, errors.Join(err, readErr)
	}
}
