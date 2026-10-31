package cli

import (
	"context"
	"flag"
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) publicationCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "publication requires create, change, list, show, versions or version")
	}
	switch args[0] {
	case "create":
		var req domain.PublicationRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/publications", req, "")
	case "change":
		id, tail, err := idArgs(args[1:])
		if err != nil {
			return nil, err
		}
		var req domain.PublicationChange
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("publications", id, "change"), req, api.Path("publications", id))
	case "show", "version":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		if args[0] == "version" {
			return api.Call[domain.PublicationVersion](ctx, r.client, "GET", api.Path("publication-versions", args[1]), nil)
		}
		return api.Call[domain.Publication](ctx, r.client, "GET", api.Path("publications", args[1]), nil)
	case "list", "versions":
		tail, path := args[1:], "/v1/publications"
		if args[0] == "versions" {
			id, rest, err := idArgs(tail)
			if err != nil {
				return nil, err
			}
			path, tail = api.Path("publications", id, "versions"), rest
		}
		fs := flag.NewFlagSet("publication "+args[0], flag.ContinueOnError)
		cursor := fs.String("cursor", "", "actor/filter-bound cursor")
		limit := fs.Int("limit", 50, "page size (1-200)")
		kind, scope, target, archived := "", "", "", false
		if args[0] == "list" {
			fs.StringVar(&kind, "kind", "", "file, diary, skill or idea")
			fs.StringVar(&scope, "scope", "", "company, project or agent")
			fs.StringVar(&target, "target", "", "explicit project/agent scope target")
			fs.BoolVar(&archived, "include-archived", false, "include archived publications")
		}
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		query := url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}}
		if args[0] == "list" {
			query.Set("kind", kind)
			query.Set("scope", scope)
			query.Set("target", target)
			query.Set("include_archived", strconv.FormatBool(archived))
		}
		if args[0] == "versions" {
			return api.Call[domain.Page[domain.PublicationVersion]](ctx, r.client, "GET", path+"?"+query.Encode(), nil)
		}
		return api.Call[domain.Page[domain.Publication]](ctx, r.client, "GET", path+"?"+query.Encode(), nil)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown publication command")
	}
}
