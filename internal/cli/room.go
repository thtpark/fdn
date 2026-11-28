package cli

import (
	"context"
	"flag"
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (a *app) roomCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "room requires list, show, create, change, messages, message, send, read, message-read, deliveries or receipt")
	}
	leaf, tail := args[0], args[1:]
	switch leaf {
	case "list":
		fs := flag.NewFlagSet("room list", flag.ContinueOnError)
		kind := fs.String("kind", "", "room kind filter (direct, group or project)")
		project := fs.String("project", "", "project ID filter")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		query := url.Values{}
		if *kind != "" {
			query.Set("kind", *kind)
		}
		if *project != "" {
			query.Set("project", *project)
		}
		path := "/v1/rooms"
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		return api.Call[[]domain.MessageRoom](ctx, r.client, "GET", path, nil)
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.MessageRoom](ctx, r.client, "GET", api.Path("rooms", tail[0]), nil)
	case "create":
		var req domain.RoomCreateRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/rooms", &req, "")
	case "change":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		var req domain.RoomChangeRequest
		if err := a.input(rest, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("rooms", id, "change"), &req, "")
	case "messages":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		fs := flag.NewFlagSet("room messages", flag.ContinueOnError)
		limit := fs.Int("limit", 50, "page size (1-200)")
		cursor := fs.String("cursor", "", "signed room message cursor")
		if err := parse(fs, rest); err != nil {
			return nil, err
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *cursor != "" {
			query.Set("cursor", *cursor)
		}
		return api.Call[domain.Page[domain.RoomMessage]](ctx, r.client, "GET", api.Path("rooms", id, "messages")+"?"+query.Encode(), nil)
	case "message":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.RoomMessage](ctx, r.client, "GET", api.Path("room-messages", tail[0]), nil)
	case "send":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		var req domain.RoomMessageRequest
		if err := a.input(rest, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("rooms", id, "messages"), &req, "")
	case "read":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		var req domain.RoomReadRequest
		if len(rest) != 0 {
			if err := a.input(rest, &req); err != nil {
				return nil, err
			}
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("rooms", id, "read"), &req, "")
	case "message-read":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		req := domain.MessageReadRequest{}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("room-messages", tail[0], "read"), &req, "")
	case "deliveries":
		fs := flag.NewFlagSet("room deliveries", flag.ContinueOnError)
		limit := fs.Int("limit", 50, "page size (1-200)")
		cursor := fs.String("cursor", "", "cursor")
		urgent := fs.Bool("urgent", false, "urgent deliveries only")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *cursor != "" {
			query.Set("cursor", *cursor)
		}
		if *urgent {
			query.Set("urgent", "true")
		}
		return api.Call[domain.Page[domain.RoomDelivery]](ctx, r.client, "GET", "/v1/room-deliveries?"+query.Encode(), nil)
	case "receipt":
		id, rest, err := idArgs(tail)
		if err != nil {
			return nil, err
		}
		var req domain.RoomDeliveryReceipt
		if err := a.input(rest, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("room-deliveries", id, "receipt"), &req, "")
	default:
		return nil, domain.Fail(domain.Invalid, "unknown room command")
	}
}
