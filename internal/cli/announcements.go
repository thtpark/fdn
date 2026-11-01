package cli

import (
	"context"
	"flag"
	"net/url"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) announcementCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "announcement requires audience or send")
	}
	switch args[0] {
	case "audience":
		flags := flag.NewFlagSet("announcement audience", flag.ContinueOnError)
		scope := flags.String("scope", "global", "global or project")
		project := flags.String("project", "", "exact active project")
		if err := parse(flags, args[1:]); err != nil {
			return nil, err
		}
		query := url.Values{"scope": {*scope}}
		if *project != "" {
			query.Set("project", *project)
		}
		return api.Call[domain.AnnouncementAudience](ctx, r.client, "GET", "/v1/announcements/audience?"+query.Encode(), nil)
	case "send":
		var req domain.AnnouncementRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := domain.ValidateAnnouncement(req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/announcements", req, "")
	default:
		return nil, domain.Fail(domain.Invalid, "unknown announcement command")
	}
}
