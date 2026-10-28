package cli

import (
	"context"
	"flag"
	"net/url"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) noteCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "note requires set, show, history or context")
	}
	switch args[0] {
	case "set":
		var req domain.NoteRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/notes", req, "")
	case "show", "history":
		tail := args[1:]
		if len(tail) < 1 || len(tail) > 2 {
			return nil, domain.Fail(domain.Invalid, "note show/history requires company, project PROJECT_ID or agent [AGENT_ID]")
		}
		scope, target := tail[0], ""
		if len(tail) == 2 {
			target = tail[1]
		} else if scope == domain.AgentNotes {
			target = r.client.Credential.AgentID
		}
		id, err := domain.NoteID(scope, target)
		if err != nil {
			return nil, err
		}
		if args[0] == "history" {
			return api.Call[[]domain.History](ctx, r.client, "GET", api.Path("history", id), nil)
		}
		query := url.Values{"scope": {scope}, "target": {target}}
		return api.Call[domain.Note](ctx, r.client, "GET", "/v1/notes?"+query.Encode(), nil)
	case "context":
		fs := flag.NewFlagSet("note context", flag.ContinueOnError)
		project := fs.String("project", "", "include this project's notes with company and own agent notes")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		query := url.Values{"project": {*project}}
		return api.Call[[]domain.Note](ctx, r.client, "GET", "/v1/note-context?"+query.Encode(), nil)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown note command")
	}
}
