package cli

import (
	"context"
	"flag"
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) deliveryCommand(ctx context.Context, r *resources, group string, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, group+" requires a documented leaf command")
	}
	leaf, tail := args[0], args[1:]
	if group == "repository" && (leaf == "remote" || leaf == "link" || leaf == "bundle") {
		return a.repositoryLocal(ctx, r, leaf, tail)
	}
	if group == "build" && leaf == "run" {
		return a.runSoftwareBuild(ctx, r, tail)
	}
	if group == "build" && leaf == "download" {
		return a.downloadSoftwareBuild(ctx, r, tail)
	}
	if group == "software" && (leaf == "plan" || leaf == "apply" || leaf == "status") {
		return a.softwarePlanCommand(ctx, r, leaf, tail)
	}
	switch group + " " + leaf {
	case "improvement policy":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[domain.ImprovementPolicy](ctx, r.client, "GET", "/v1/improvement-policy", nil)
	case "improvement usage":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[domain.MaintenanceUsage](ctx, r.client, "GET", "/v1/maintenance-usage", nil)
	case "improvement list", "repository list", "software list", "release list":
		fs := flag.NewFlagSet(group+" list", flag.ContinueOnError)
		project := fs.String("project", "", "owning project")
		state := fs.String("state", "", "improvement state")
		product := fs.String("product", "", "release product")
		limit := fs.Int("limit", 50, "page size (1-200)")
		cursor := fs.String("cursor", "", "actor/filter-bound cursor")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		query := url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}}
		if group == "release" {
			if *project != "" || *state != "" || *product == "" {
				return nil, domain.Fail(domain.Invalid, "release list requires --product and accepts no project/state")
			}
			query.Set("product", *product)
			return api.Call[domain.Page[domain.SoftwareRelease]](ctx, r.client, "GET", "/v1/software-releases?"+query.Encode(), nil)
		}
		if *product != "" || (group != "improvement" && *state != "") {
			return nil, domain.Fail(domain.Invalid, "unsupported delivery list filter")
		}
		query.Set("project", *project)
		switch group {
		case "improvement":
			query.Set("state", *state)
			return api.Call[domain.Page[domain.Improvement]](ctx, r.client, "GET", "/v1/improvements?"+query.Encode(), nil)
		case "repository":
			return api.Call[domain.Page[domain.HostedRepository]](ctx, r.client, "GET", "/v1/repositories?"+query.Encode(), nil)
		default:
			return api.Call[domain.Page[domain.SoftwareProduct]](ctx, r.client, "GET", "/v1/software-products?"+query.Encode(), nil)
		}
	case "improvement show", "repository show", "software show", "build show", "release show", "software control", "software deployment", "software maintenance":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		switch group + " " + leaf {
		case "improvement show":
			return api.Call[domain.Improvement](ctx, r.client, "GET", api.Path("improvements", tail[0]), nil)
		case "repository show":
			return api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", tail[0]), nil)
		case "software show":
			return api.Call[domain.SoftwareProduct](ctx, r.client, "GET", api.Path("software-products", tail[0]), nil)
		case "build show":
			return api.Call[domain.SoftwareBuild](ctx, r.client, "GET", api.Path("software-builds", tail[0]), nil)
		case "release show":
			return api.Call[domain.SoftwareRelease](ctx, r.client, "GET", api.Path("software-releases", tail[0]), nil)
		case "software control":
			return api.Call[domain.SoftwarePreflight](ctx, r.client, "GET", api.Path("software-preflights", tail[0]), nil)
		case "software maintenance":
			return api.Call[domain.SoftwareMaintenance](ctx, r.client, "GET", api.Path("software-maintenance", tail[0]), nil)
		default:
			return api.Call[domain.SoftwareDeployment](ctx, r.client, "GET", api.Path("software-deployments", tail[0]), nil)
		}
	case "software inventory":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[[]domain.SoftwareInventory](ctx, r.client, "GET", "/v1/software-inventory", nil)
	case "software consumers":
		fs := flag.NewFlagSet("software consumers", flag.ContinueOnError)
		project := fs.String("project", "", "consuming project")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		return api.Call[[]domain.SoftwareConsumer](ctx, r.client, "GET", "/v1/software-consumers?project="+url.QueryEscape(*project), nil)
	}
	var input any
	var command *domain.Command
	var path string
	switch group + " " + leaf {
	case "improvement configure":
		req := new(domain.ImprovementPolicyRequest)
		input, command, path = req, &req.Command, "/v1/improvement-policy"
	case "improvement create":
		req := new(domain.ImprovementRequest)
		input, command, path = req, &req.Command, "/v1/improvements"
	case "improvement decide":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		tail = rest
		req := new(domain.ImprovementDecision)
		input, command, path = req, &req.Command, api.Path("improvements", id, "decision")
	case "repository create":
		req := new(domain.RepositoryRequest)
		input, command, path = req, &req.Command, "/v1/repositories"
	case "repository push", "repository merge":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		tail = rest
		if leaf == "push" {
			req := new(domain.RepositoryPush)
			input, command, path = req, &req.Command, api.Path("repositories", id, "push")
		} else {
			req := new(domain.RepositoryMerge)
			input, command, path = req, &req.Command, api.Path("repositories", id, "merge")
		}
	case "software register":
		req := new(domain.SoftwareProductRequest)
		input, command, path = req, &req.Command, "/v1/software-products"
	case "software observe":
		req := new(domain.SoftwareObservationRequest)
		input, command, path = req, &req.Command, "/v1/software-observations"
	case "software bind-consumer":
		req := new(domain.SoftwareConsumerRequest)
		input, command, path = req, &req.Command, "/v1/software-consumers"
	case "software preflight":
		req := new(domain.SoftwarePreflightRequest)
		input, command, path = req, &req.Command, "/v1/software-preflights"
	case "build create":
		req := new(domain.SoftwareBuildRequest)
		input, command, path = req, &req.Command, "/v1/software-builds"
	case "build result":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		tail = rest
		req := new(domain.SoftwareBuildResult)
		input, command, path = req, &req.Command, api.Path("software-builds", id, "result")
	case "release create":
		req := new(domain.SoftwareReleaseRequest)
		input, command, path = req, &req.Command, "/v1/software-releases"
	case "release revoke":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		tail = rest
		req := new(domain.SoftwareReleaseChange)
		input, command, path = req, &req.Command, api.Path("software-releases", id, "revoke")
	default:
		return nil, domain.Fail(domain.Invalid, "unknown internal delivery command")
	}
	if err := a.input(tail, input); err != nil {
		return nil, err
	}
	if err := a.metadata(command); err != nil {
		return nil, err
	}
	return a.write(ctx, r, path, input, "")
}
