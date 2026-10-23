package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/work"
)

func (a *app) agent(ctx context.Context, args []string) (value any, err error) {
	group := args[0]
	tail := args[1:]
	if group == "config" && len(tail) > 0 && tail[0] == "init" {
		return a.initWorker(tail[1:])
	}
	if group == "connect" {
		return a.connect(ctx, tail)
	}
	if group == "work" && len(tail) == 1 && tail[0] == "prompt" {
		prompt, err := work.BasePrompt()
		return map[string]string{"prompt": prompt}, err
	}
	if group == "service" && len(tail) > 0 && tail[0] != "status" {
		return a.nativeService(ctx, tail, "agent")
	}
	localOnly := slices.Contains([]string{"config", "knowledge", "inventory", "workspace", "provider", "preferences", "service", "screen"}, group) ||
		(group == "mode" && len(tail) > 0 && tail[0] == "show") ||
		(group == "work" && len(tail) > 0 && tail[0] == "status")
	r, err := a.load(ctx, !localOnly)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, r.close()) }()
	switch group {
	case "screen":
		return a.screenCommand(ctx, r, tail)
	case "config", "mode", "knowledge", "inventory", "workspace", "provider", "preferences", "service", "work":
		return a.localCommand(ctx, r, group, tail)
	case "status":
		if len(tail) != 0 {
			return nil, domain.Fail(domain.Invalid, "status has no leaf flags")
		}
		me, err := api.Call[domain.Agent](ctx, r.client, "GET", "/v1/me", nil)
		if err != nil {
			return nil, err
		}
		status, err := a.localStatus(ctx, r)
		return map[string]any{"agent": me, "local": status}, err
	case "doctor":
		if len(tail) != 0 {
			return nil, domain.Fail(domain.Invalid, "doctor has no leaf flags")
		}
		health, err := api.Call[json.RawMessage](ctx, r.client, "GET", "/healthz", nil)
		if err != nil {
			return nil, err
		}
		me, err := api.Call[domain.Agent](ctx, r.client, "GET", "/v1/me", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"configuration": "valid", "tls_and_authentication": "verified", "service": health, "agent_id": me.ID}, nil
	case "invitation":
		return nil, domain.Fail(domain.Prerequisite, "bearer invitations are retired; Founder must register the target's public identity")
	case "agent":
		return a.agentCommand(ctx, r, tail)
	case "project":
		return a.projectCommand(ctx, r, tail)
	case "task":
		return a.taskCommand(ctx, r, tail)
	case "comment", "worklog", "discussion":
		return a.discussionCommand(ctx, r, group, tail)
	case "mention":
		return a.mentionCommand(ctx, r, tail)
	case "message":
		return a.messageCommand(ctx, r, tail)
	case "session":
		return a.sessionCommand(ctx, r, tail)
	case "assistance":
		return a.assistanceCommand(ctx, r, tail)
	case "watch":
		return a.watchCommand(ctx, r, tail)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown agent-cli command")
	}
}

func requireArgs(args []string, count int) error {
	if len(args) != count {
		return domain.Fail(domain.Invalid, "unexpected or missing command arguments")
	}
	return nil
}

func (a *app) agentCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "agent requires list, show, diagnose, register, recover, identity, revoke, mode or request-sign-in")
	}
	if args[0] == "register" || args[0] == "recover" || args[0] == "identity" {
		return a.identityCommand(ctx, r, args)
	}
	if args[0] == "list" {
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.Agent](ctx, r.client, "GET", "/v1/agents", nil)
	}
	if args[0] == "show" {
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.Agent](ctx, r.client, "GET", api.Path("agents", args[1]), nil)
	}
	if args[0] == "diagnose" {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		if err := requireArgs(tail, 0); err != nil {
			return nil, err
		}
		return api.Call[domain.AgentDiagnostics](ctx, r.client, "GET", api.Path("agents", id, "diagnostics"), nil)
	}
	if args[0] == "request-sign-in" {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.AuthenticationRefreshRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("agents", id, "authentication"), req, "")
	}
	if args[0] == "mode" {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.ModeRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("agents", id, "mode"), req, api.Path("agents", id))
	}
	if args[0] == "revoke" {
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		req := domain.AgentRequest{}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("agents", args[1], "revoke"), req, "")
	}
	return nil, domain.Fail(domain.Invalid, "unknown agent command")
}

func (a *app) projectCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "project command required")
	}
	switch args[0] {
	case "list":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.Project](ctx, r.client, "GET", "/v1/projects", nil)
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.Project](ctx, r.client, "GET", api.Path("projects", args[1]), nil)
	case "create":
		var req domain.ProjectRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/projects", req, "")
	case "delegate":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.GrantRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("projects", id, "delegates"), req, api.Path("projects", id))
	case "change":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.ProjectChange
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("projects", id, "change"), req, api.Path("projects", id))
	case "authorize":
		var req domain.GrantRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/project-creation-grants", req, "")
	default:
		return nil, domain.Fail(domain.Invalid, "unknown project command")
	}
}

func (a *app) taskCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "task command required")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("task list", flag.ContinueOnError)
		project := fs.String("project", "", "project filter")
		assignee := fs.String("assignee", "", "assignee filter")
		status := fs.String("status", "", "status filter")
		limit := fs.Int("limit", 50, "page size")
		cursor := fs.String("cursor", "", "opaque cursor")
		archived := fs.Bool("include-archived", false, "include archived tasks")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		query := url.Values{"project": {*project}, "assignee": {*assignee}, "status": {*status}, "limit": {strconv.Itoa(*limit)}, "cursor": {*cursor}, "include_archived": {strconv.FormatBool(*archived)}}
		return api.Call[domain.Page[domain.Task]](ctx, r.client, "GET", "/v1/tasks?"+query.Encode(), nil)
	case "show", "history", "runs":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		path := api.Path("tasks", args[1])
		if args[0] == "history" {
			path = api.Path("history", args[1])
		}
		if args[0] == "runs" {
			path = api.Path("tasks", args[1], "runs")
		}
		return api.Call[json.RawMessage](ctx, r.client, "GET", path, nil)
	case "create":
		var req domain.TaskRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/tasks", req, "")
	case "change":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.TaskChange
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("tasks", id, "change"), req, api.Path("tasks", id))
	default:
		return nil, domain.Fail(domain.Invalid, "unknown task command")
	}
}

func (a *app) discussionCommand(ctx context.Context, r *resources, group string, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "discussion leaf command required")
	}
	if group == "discussion" && args[0] == "list" {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		fs := flag.NewFlagSet("discussion list", flag.ContinueOnError)
		cursor := fs.String("cursor", "", "opaque cursor")
		limit := fs.Int("limit", 50, "page size")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		return api.Call[domain.Page[domain.Entry]](ctx, r.client, "GET", api.Path("tasks", id, "entries")+"?"+url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}}.Encode(), nil)
	}
	if (group == "comment" && (args[0] == "add" || args[0] == "edit")) || (group == "worklog" && args[0] == "add") {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.EntryRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if req.Kind != "" && req.Kind != group {
			return nil, domain.Fail(domain.Invalid, "entry kind disagrees with the command")
		}
		req.Kind = group
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		if args[0] == "edit" {
			return a.write(ctx, r, api.Path("entries", id), req, "")
		}
		return a.write(ctx, r, api.Path("tasks", id, "entries"), req, "")
	}
	return nil, domain.Fail(domain.Invalid, "unknown discussion command")
}

func (a *app) mentionCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "mention command required")
	}
	if args[0] == "list" {
		fs := flag.NewFlagSet("mention list", flag.ContinueOnError)
		cursor := fs.String("cursor", "", "opaque cursor")
		limit := fs.Int("limit", 50, "page size")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		return api.Call[domain.Page[domain.Attention]](ctx, r.client, "GET", "/v1/attention?"+url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}}.Encode(), nil)
	}
	if args[0] == "acknowledge" {
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.AttentionRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("attention", id), req, "")
	}
	return nil, domain.Fail(domain.Invalid, "unknown mention command")
}

func (a *app) messageCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "message command required")
	}
	switch args[0] {
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.Message](ctx, r.client, "GET", api.Path("messages", args[1]), nil)
	case "diagnose":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		if err := requireArgs(tail, 0); err != nil {
			return nil, err
		}
		return api.Call[domain.MessageDiagnostics](ctx, r.client, "GET", api.Path("messages", id, "diagnostics"), nil)
	case "inbox":
		fs := flag.NewFlagSet("message inbox", flag.ContinueOnError)
		cursor := fs.String("cursor", "", "cursor")
		limit := fs.Int("limit", 50, "page size")
		urgent := fs.Bool("urgent", false, "urgent only")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		return api.Call[domain.Page[domain.Message]](ctx, r.client, "GET", "/v1/messages?"+url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}, "urgent": {strconv.FormatBool(*urgent)}}.Encode(), nil)
	case "send":
		var req domain.MessageRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if req.Origin == "" {
			req.Origin = "human"
		}
		if req.Kind == "" {
			req.Kind = "direct"
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/messages", req, "")
	case "receipt":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.MessageReceipt
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("messages", id, "receipt"), req, api.Path("messages", id))
	default:
		return nil, domain.Fail(domain.Invalid, "unknown message command")
	}
}

func (a *app) watchCommand(ctx context.Context, r *resources, args []string) (any, error) {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	project := fs.String("project", "", "project")
	task := fs.String("task", "", "task")
	since := fs.String("since", "", "opaque checkpoint")
	once := fs.Bool("once", false, "one page")
	limit := fs.Int("limit", 100, "page size")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	query := url.Values{"project": {*project}, "task": {*task}, "limit": {strconv.Itoa(*limit)}}
	key := "cli-watch:" + domain.Hash(query.Encode())
	cursor := *since
	if cursor == "" && !*once {
		if _, err := r.state.Get(ctx, key, &cursor); err != nil {
			return nil, err
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		query.Set("since", cursor)
		page, err := api.Call[domain.Page[domain.Event]](ctx, r.client, "GET", "/v1/events?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if *once {
			return page, nil
		}
		if len(page.Items) > 0 {
			if a.emit(page, nil) != 0 {
				return nil, domain.Fail(domain.Unavailable, "watch output failed; checkpoint was not advanced")
			}
		}
		if err := r.state.Put(ctx, key, page.NextCursor); err != nil {
			return nil, err
		}
		cursor = page.NextCursor
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
