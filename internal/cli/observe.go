package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

type observationSource struct {
	app *app
	res *resources
}

func (s observationSource) Identity(ctx context.Context) (domain.AgentIdentity, error) {
	return api.Call[domain.AgentIdentity](ctx, s.res.client, "GET", "/v1/identity/status", nil)
}

func (s observationSource) Updates(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Update], error) {
	data, err := json.Marshal(filter)
	if err != nil {
		return domain.Page[domain.Update]{}, err
	}
	query := url.Values{"filter": {string(data)}, "since": {cursor}, "limit": {"100"}, "wait": {strconv.FormatBool(wait)}}
	return api.Call[domain.Page[domain.Update]](ctx, s.res.client, "GET", "/v1/updates?"+query.Encode(), nil)
}

func (s observationSource) Events(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Event], error) {
	data, err := json.Marshal(filter)
	if err != nil {
		return domain.Page[domain.Event]{}, err
	}
	query := url.Values{"filter": {string(data)}, "since": {cursor}, "limit": {"100"}, "wait": {strconv.FormatBool(wait)}}
	return api.Call[domain.Page[domain.Event]](ctx, s.res.client, "GET", "/v1/events?"+query.Encode(), nil)
}

func (s observationSource) Snapshot(ctx context.Context, q observe.Query) (json.RawMessage, error) {
	value, err := s.snapshot(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (s observationSource) snapshot(ctx context.Context, q observe.Query) (any, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	a, r := s.app, s.res
	limit := q.Limit
	if limit == 0 {
		limit = 50
	}
	page := []string{"--limit", strconv.Itoa(limit)}
	if q.Cursor != "" {
		page = append(page, "--cursor", q.Cursor)
	}
	switch q.Kind {
	case "version":
		return map[string]any{"version": domain.Version, "commit": domain.Commit, "api_version": domain.APIVersion, "schema_version": domain.SchemaVersion}, nil
	case "help":
		return map[string]any{"commands": commands("agent")}, nil
	case "status":
		me, err := api.Call[domain.Agent](ctx, r.client, "GET", "/v1/me", nil)
		if err != nil {
			return nil, err
		}
		local, err := a.localStatus(ctx, r)
		return map[string]any{"agent": me, "local": local}, err
	case "identity":
		return s.Identity(ctx)
	case "work":
		return a.localStatus(ctx, r)
	case "agent", "diagnostics", "operation":
		leaf := map[string]string{"agent": "show", "diagnostics": "diagnose", "operation": "operation"}[q.Kind]
		return a.agentCommand(ctx, r, []string{leaf, q.ID})
	case "environment":
		return a.agentCommand(ctx, r, []string{"environment", q.ID})
	case "announcementAudience":
		args := []string{"audience", "--scope", q.Scope}
		if q.Project != "" {
			args = append(args, "--project", q.Project)
		}
		return a.announcementCommand(ctx, r, args)
	case "agents":
		return a.agentCommand(ctx, r, []string{"list"})
	case "project":
		return a.projectCommand(ctx, r, []string{"show", q.ID})
	case "projects":
		return a.projectCommand(ctx, r, []string{"list"})
	case "tasks":
		args := append([]string{"list"}, page...)
		for _, option := range []struct{ name, value string }{{"project", q.Project}, {"assignee", q.Assignee}, {"status", q.Status}} {
			if option.value != "" {
				args = append(args, "--"+option.name, option.value)
			}
		}
		if q.Archived {
			args = append(args, "--include-archived")
		}
		return a.taskCommand(ctx, r, args)
	case "task", "taskHistory", "runs":
		leaf := map[string]string{"task": "show", "taskHistory": "history", "runs": "runs"}[q.Kind]
		return a.taskCommand(ctx, r, []string{leaf, q.ID})
	case "discussion":
		return a.discussionCommand(ctx, r, "discussion", append([]string{"list", q.ID}, page...))
	case "entry":
		return a.discussionCommand(ctx, r, "discussion", []string{"show", q.ID})
	case "mentions":
		return a.mentionCommand(ctx, r, append([]string{"list"}, page...))
	case "inbox":
		args := append([]string{"inbox"}, page...)
		if q.Urgent {
			args = append(args, "--urgent")
		}
		return a.messageCommand(ctx, r, args)
	case "messageHistory":
		args := append([]string{"list"}, page...)
		for _, option := range []struct{ name, value string }{{"folder", q.Folder}, {"state", q.State}, {"peer", q.Peer}} {
			if option.value != "" {
				args = append(args, "--"+option.name, option.value)
			}
		}
		return a.messageCommand(ctx, r, args)
	case "threadPage":
		return a.messageCommand(ctx, r, append([]string{"thread-page", q.ID}, page...))
	case "message", "thread", "messageDiagnostics":
		leaf := map[string]string{"message": "show", "thread": "thread", "messageDiagnostics": "diagnose"}[q.Kind]
		return a.messageCommand(ctx, r, []string{leaf, q.ID})
	case "note", "noteHistory":
		args := []string{"show", q.Scope}
		if q.Kind == "noteHistory" {
			args[0] = "history"
		}
		if q.ID != "" {
			args = append(args, q.ID)
		}
		return a.noteCommand(ctx, r, args)
	case "noteContext":
		args := []string{"context"}
		if q.Project != "" {
			args = append(args, "--project", q.Project)
		}
		return a.noteCommand(ctx, r, args)
	case "knowledge", "knowledgeItem", "knowledgeHistory":
		args := []string{"list"}
		if q.Kind != "knowledge" {
			args = []string{"show", q.ID}
			if q.Kind == "knowledgeHistory" {
				args[0] = "history"
			}
		} else if q.Archived {
			args = append(args, "--include-archived")
		}
		return a.localCommand(ctx, r, "knowledge", args)
	case "workspaces", "inventory", "inventoryRefresh", "preferences", "provider", "configuration", "screens", "upgrade":
		switch q.Kind {
		case "workspaces":
			return a.localCommand(ctx, r, "workspace", []string{"list"})
		case "inventory", "inventoryRefresh":
			leaf := "show"
			if q.Kind == "inventoryRefresh" {
				leaf = "refresh"
			}
			return a.localCommand(ctx, r, "inventory", []string{leaf})
		case "preferences":
			return a.localCommand(ctx, r, "preferences", []string{"show"})
		case "provider":
			return a.providerCommand(ctx, r, []string{"status"})
		case "configuration":
			return a.localCommand(ctx, r, "config", []string{"show"})
		case "screens":
			return a.screenCommand(ctx, r, []string{"list"})
		default:
			return a.upgradeCommand(ctx, r, []string{"status"})
		}
	case "sessions", "session":
		args := []string{"list"}
		if q.Kind == "session" {
			args = []string{"show", q.ID}
		}
		return a.sessionCommand(ctx, r, args)
	case "grants", "grant":
		args := []string{"list"}
		if q.Kind == "grant" {
			args = []string{"show", q.ID}
		}
		return a.assistanceCommand(ctx, r, args)
	case "devices":
		return a.deviceCommand(ctx, r, []string{"list"})
	case "guides":
		return a.guide(ctx, []string{"list"})
	case "guide":
		return a.guide(ctx, []string{"show", q.ID})
	case "events":
		return api.Call[domain.Page[domain.Event]](ctx, r.client, "GET", "/v1/events?"+url.Values{"project": {q.Project}, "task": {q.ID}, "since": {q.Cursor}, "limit": {strconv.Itoa(limit)}}.Encode(), nil)
	case "doctor":
		health, err := api.Call[json.RawMessage](ctx, r.client, "GET", "/healthz", nil)
		return map[string]any{"service": health}, err
	default:
		return nil, domain.Fail(domain.Invalid, "unsupported read-only observer query")
	}
}
