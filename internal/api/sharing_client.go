package api

import (
	"context"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func PrepareSharing(ctx context.Context, client *Client, target, mode, grantID string, seconds int64, reason string) (domain.SessionRequest, error) {
	request := domain.SessionRequest{TargetID: target, Mode: mode, Purpose: "sharing", GrantID: grantID,
		LifetimeSeconds: seconds, Reason: reason}
	if !slices.Contains([]string{"tmux", "vnc"}, mode) || seconds < 1 || seconds > domain.MaxSharingSeconds {
		return request, domain.Fail(domain.Invalid, "sharing requires tmux/vnc and a 1-86400 second lifetime")
	}
	if grantID != "" {
		grant, err := Call[domain.AssistanceGrant](ctx, client, "GET", Path("assistance", grantID), nil)
		if err != nil {
			return request, err
		}
		if !grant.Active || grant.Purpose != "sharing" || grant.RequesterID != client.Credential.AgentID ||
			grant.TargetID != target || (grant.Mode != mode && grant.Mode != "both") {
			return request, domain.Fail(domain.Denied, "sharing approval does not cover this requester/target/mode")
		}
		request.ProjectID, request.DesktopFallback = grant.ProjectID, grant.DesktopFallback
	}
	state, err := Call[domain.Sharing](ctx, client, "GET", Path("agents", target, "sharing"), nil)
	if err != nil {
		return request, err
	}
	request.SelectionRevision = state.SelectionRevision
	if mode == "tmux" {
		request.TerminalID = state.TerminalID
		request.DesktopFallback = false
		if request.TerminalID == "" {
			return request, domain.Fail(domain.Prerequisite, "target has not published an available selected terminal")
		}
	} else {
		request.ScreenID = state.ScreenID
		screens, err := Call[[]domain.Screen](ctx, client, "GET", Path("agents", target, "screens"), nil)
		if err != nil {
			return request, err
		}
		screen := slices.IndexFunc(screens, func(screen domain.Screen) bool { return screen.ID == request.ScreenID })
		if screen < 0 {
			return request, domain.Fail(domain.Prerequisite, "target has no available selected screen")
		}
		desktop := screens[screen].Scope == "desktop"
		if grantID != "" && desktop && !request.DesktopFallback {
			return request, domain.Fail(domain.Denied, "this approval does not permit the selected whole-desktop capture")
		}
		request.DesktopFallback = desktop
	}
	return request, nil
}
