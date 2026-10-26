package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) PublishScreens(ctx context.Context, actor string, req domain.ScreensRequest) ([]domain.Screen, error) {
	return mutate(ctx, s, actor, "agent.screens", req.Command, req, func(tx *sql.Tx, a domain.Agent) ([]domain.Screen, error) {
		if err := domain.ValidateScreens(req.Screens); err != nil {
			return nil, err
		}
		screens := make([]domain.Screen, len(req.Screens))
		for i, screen := range req.Screens {
			screens[i] = screen
			screens[i].Applications = append([]string{}, screen.Applications...)
		}
		a.Screens = screens
		data, err := encode(a)
		if err != nil {
			return nil, err
		}
		// Observations share heartbeat's identity fence, not mode/work revisions.
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET data = ? WHERE id = ?", data, a.ID); err != nil {
			return nil, err
		}
		return screens, nil
	})
}

func (s *Store) Screens(ctx context.Context, actor, target string) ([]domain.Screen, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	if _, err := s.actor(ctx, tx, actor); err != nil {
		return nil, err
	}
	agent, err := activeIdentitySubject(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	screens := agent.Screens
	if screens == nil {
		screens = []domain.Screen{}
	}
	return screens, nil
}

func assistanceScreen(target domain.Agent, mode, screenID, applicationID string, desktopFallback bool) (domain.Screen, error) {
	if mode == "tmux" {
		if screenID != "" || applicationID != "" || desktopFallback {
			return domain.Screen{}, domain.Fail(domain.Invalid, "tmux assistance cannot select a screen, application or desktop fallback")
		}
		return domain.Screen{}, nil
	}
	if mode != "vnc" {
		return domain.Screen{}, domain.Fail(domain.Invalid, "assistance mode must be tmux or vnc")
	}
	if screenID == "" || len(target.Screens) == 0 {
		// Legacy unscoped VNC records cannot establish an actual screen capability.
		return domain.Screen{}, domain.Fail(domain.Prerequisite, "VNC requires a currently published screen and explicit screen-bound authorization")
	}
	if !domain.ValidScreenID(screenID) || (applicationID != "" && !domain.ValidScreenID(applicationID)) {
		return domain.Screen{}, domain.Fail(domain.Invalid, "VNC screen and application must be safe public identifiers")
	}
	for _, screen := range target.Screens {
		if screen.ID != screenID {
			continue
		}
		if screen.Scope != "screen" && screen.Scope != "desktop" {
			return domain.Screen{}, domain.Fail(domain.Prerequisite, "published display scope is not supported")
		}
		if desktopFallback != (screen.Scope == "desktop") {
			return domain.Screen{}, domain.Fail(domain.Denied, "whole-desktop sharing requires explicit desktop fallback authorization; screen-only scope cannot change")
		}
		if applicationID != "" && !slices.Contains(screen.Applications, applicationID) {
			return domain.Screen{}, domain.Fail(domain.Prerequisite, "application is not available on the selected screen")
		}
		return screen, nil
	}
	return domain.Screen{}, domain.Fail(domain.Prerequisite, "selected screen is no longer published")
}
