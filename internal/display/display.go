// Package display uses only existing local displays and owner-verified VNC backends.
package display

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

const LegacyDesktopID = "legacy-desktop"

const inventoryTimeout = 8 * time.Second

type rectangle struct{ X, Y, Width, Height int }

type available struct {
	profile config.Screen
	rect    rectangle
	public  domain.Screen
}

// StartupError distinguishes proven cleanup from an uncertain native process.
// Callers must retain their execution fence unless CleanupProven returns true.
type StartupError struct {
	Err   error
	clean bool
}

func (e *StartupError) Error() string       { return e.Err.Error() }
func (e *StartupError) Unwrap() error       { return e.Err }
func (e *StartupError) CleanupProven() bool { return e.clean }

func startupError(err error, clean bool) error {
	var uncertain *process.CleanupError
	return &StartupError{Err: err, clean: clean && !errors.As(err, &uncertain)}
}

// Inventory probes metadata and RFB version banners, never UI pixels or login.
func Inventory(ctx context.Context, c config.Worker) ([]domain.Screen, error) {
	found, err := inventory(ctx, c)
	result := make([]domain.Screen, 0, len(found))
	for _, item := range found {
		result = append(result, item.public)
	}
	return result, err
}

func inventory(ctx context.Context, c config.Worker) ([]available, error) {
	result := []available{}
	if err := contextError(ctx); err != nil {
		return result, err
	}
	if err := c.ValidateScreens(); err != nil {
		return result, err
	}
	inventoryCtx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()
	profiles := slices.Clone(c.Screens)
	if c.VNCAddress != "" && c.VNCVerified && len(profiles) < 16 {
		legacy := config.Screen{ID: LegacyDesktopID, Name: "Whole desktop (fallback)", Scope: "desktop", VNCAddress: c.VNCAddress,
			VNCVerified: true, Display: os.Getenv("DISPLAY"), XAuthority: os.Getenv("XAUTHORITY"), Primary: len(profiles) == 0}
		if legacy.Display != "" {
			profiles = append(profiles, legacy)
		}
	}
	var problems error
	for _, profile := range profiles {
		if err := contextError(inventoryCtx); err != nil {
			return result, errors.Join(problems, err)
		}
		if !profile.VNCVerified || profile.VNCAddress == "" {
			continue
		}
		conn, _, err := dialBackend(inventoryCtx, profile.VNCAddress)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return result, errors.Join(problems, err)
			}
			if domain.AsFault(err).Code == domain.Denied || domain.AsFault(err).Code == domain.Invalid {
				problems = errors.Join(problems, err)
			}
			continue
		}
		if err := conn.Close(); err != nil {
			problems = errors.Join(problems, err)
			continue
		}
		rect, primary, err := probe(inventoryCtx, profile)
		if err != nil {
			problems = errors.Join(problems, err)
			continue
		}
		if rect.Width == 0 || rect.Height == 0 {
			continue
		}
		apps := make([]string, 0, len(profile.Applications))
		for _, app := range profile.Applications {
			apps = append(apps, app.ID)
		}
		slices.Sort(apps)
		width, height := profile.Width, profile.Height
		if profile.ID == LegacyDesktopID {
			width, height = rect.Width, rect.Height
		}
		name := profile.Name
		if profile.Scope == "desktop" && profile.ID != LegacyDesktopID {
			const suffix = " (desktop fallback)"
			for len(name) > 128-len(suffix) {
				_, size := utf8.DecodeLastRuneInString(name)
				name = name[:len(name)-size]
			}
			name = strings.TrimSpace(name) + suffix
		}
		result = append(result, available{profile: profile, rect: rect, public: domain.Screen{
			ID: profile.ID, Name: name, Scope: profile.Scope, Width: width, Height: height,
			Primary: profile.Primary && primary, Applications: apps,
		}})
	}
	slices.SortFunc(result, func(a, b available) int { return strings.Compare(a.public.ID, b.public.ID) })
	return result, errors.Join(problems, contextError(inventoryCtx))
}

func selected(ctx context.Context, c config.Worker, screenID, applicationID string) (available, *config.ScreenApplication, error) {
	if !config.ValidScreenID(screenID) || (applicationID != "" && !config.ValidScreenID(applicationID)) {
		return available{}, nil, domain.Fail(domain.Invalid, "select an explicit published screen and configured application ID")
	}
	found, err := inventory(ctx, c)
	var cleanup *process.CleanupError
	if errors.As(err, &cleanup) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return available{}, nil, err
	}
	for _, item := range found {
		if item.public.ID != screenID {
			continue
		}
		if applicationID == "" {
			return item, nil, nil
		}
		for _, app := range item.profile.Applications {
			if app.ID == applicationID {
				return item, &app, nil
			}
		}
		return available{}, nil, domain.Fail(domain.Denied, "application is not configured for the selected screen")
	}
	if err != nil {
		return available{}, nil, err
	}
	return available{}, nil, domain.Fail(domain.Prerequisite, "selected screen has no ready existing local display and verified loopback VNC backend")
}

// Launch starts only the selected local profile, tied to the caller's context.
// Replays reuse an identical still-owned application; another owner is not adopted.
func Launch(ctx context.Context, c config.Worker, screenID, applicationID string) error {
	if applicationID == "" {
		return domain.Fail(domain.Invalid, "an explicit configured application ID is required")
	}
	item, app, err := selected(ctx, c, screenID, applicationID)
	if err != nil {
		return err
	}
	_, err = launch(ctx, c, item, *app, "local")
	return err
}

// RunApplication keeps a foreground application's ownership until exit or
// cancellation and joins cleanup before returning. Callers must retain their
// execution fence on ambiguous cleanup errors.
func RunApplication(ctx context.Context, c config.Worker, screenID, applicationID string) error {
	if applicationID == "" {
		return domain.Fail(domain.Invalid, "an explicit configured application ID is required")
	}
	item, app, err := selected(ctx, c, screenID, applicationID)
	if err != nil {
		return err
	}
	owned, err := launch(ctx, c, item, *app, domain.NewID("foreground"))
	if err != nil {
		return err
	}
	var cause error
	select {
	case <-ctx.Done():
		cause = ctx.Err()
	case <-owned.done:
		cause = owned.waitErr
	}
	cleanupErr := owned.Close()
	<-owned.cleanupDone
	if ctx.Err() != nil {
		cause = ctx.Err()
	}
	return errors.Join(cause, cleanupErr)
}

// Open prepares an explicitly opened, unexpired session. Desktop scope is never
// substituted for a selected screen, even when the caller permits fallback.
func Open(ctx context.Context, c config.Worker, session domain.Session) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, startupError(err, true)
	}
	if session.Mode != "vnc" || !domain.ValidOperation(session.ID) || !session.OpenRequested ||
		(session.State != "consented" && session.State != "active") || session.ExpiresAt <= time.Now().Unix() {
		return nil, startupError(domain.Fail(domain.Denied, "VNC requires an explicitly opened, authorized and unexpired screen session"), true)
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(session.ExpiresAt, 0))
	opened := false
	defer func() {
		if !opened {
			cancel()
		}
	}()
	item, app, err := selected(ctx, c, session.ScreenID, session.ApplicationID)
	if err != nil {
		return nil, startupError(err, true)
	}
	if session.DesktopFallback != (item.public.Scope == "desktop") {
		return nil, startupError(domain.Fail(domain.Denied, "whole-desktop capture needs the explicit desktop fallback scope; it is not screen isolation"), true)
	}
	conn, banner, err := dialBackend(ctx, item.profile.VNCAddress)
	if err != nil {
		return nil, startupError(err, true)
	}
	var appProcess *applicationProcess
	if app != nil {
		appProcess, err = launch(ctx, c, item, *app, session.ID)
		if err != nil {
			return nil, errors.Join(err, conn.Close())
		}
	}
	endpoint := &screenEndpoint{Conn: conn, prefix: strings.NewReader(string(banner)), application: appProcess, closed: make(chan struct{}), cancel: cancel}
	go func() {
		select {
		case <-ctx.Done():
			endpoint.Close()
		case <-endpoint.closed:
		}
	}()
	opened = true
	return endpoint, nil
}

type screenEndpoint struct {
	net.Conn
	prefix      *strings.Reader
	application *applicationProcess
	closed      chan struct{}
	cancel      context.CancelFunc
	once        sync.Once
	err         error
}

func (e *screenEndpoint) Read(p []byte) (int, error) {
	if e.prefix.Len() != 0 {
		return e.prefix.Read(p)
	}
	return e.Conn.Read(p)
}

func (e *screenEndpoint) Close() error {
	e.once.Do(func() {
		e.cancel()
		e.err = e.Conn.Close()
		if e.application != nil {
			e.err = errors.Join(e.err, e.application.Close())
		}
		close(e.closed)
	})
	return e.err
}
