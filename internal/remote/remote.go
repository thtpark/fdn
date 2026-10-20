package remote

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

func Dial(ctx context.Context, client *api.Client, sessionID, side string) (net.Conn, error) {
	url := "wss" + strings.TrimPrefix(client.BaseURL, "https") + api.Path("sessions", sessionID, "relay", side)
	header := http.Header{"Authorization": []string{"Bearer " + client.Credential.Token}}
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client.HTTP, HTTPHeader: header, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, domain.Fail(domain.Unavailable, "consented relay connection failed; recheck current identity, consent, open action, and expiry")
	}
	conn.SetReadLimit(64 << 10)
	return websocket.NetConn(ctx, conn, websocket.MessageBinary), nil
}

func Bridge(ctx context.Context, left, right io.ReadWriteCloser) error {
	done := make(chan error, 2)
	go func() { _, err := io.Copy(left, right); done <- err }()
	go func() { _, err := io.Copy(right, left); done <- err }()
	var first error
	remaining := 2
	select {
	case first = <-done:
		remaining--
	case <-ctx.Done():
		first = ctx.Err()
	}
	closeErr := errors.Join(left.Close(), right.Close())
	var second error
	for range remaining {
		second = errors.Join(second, <-done)
	}
	if errors.Is(first, net.ErrClosed) || errors.Is(first, io.EOF) {
		first = nil
	}
	if errors.Is(second, net.ErrClosed) || errors.Is(second, io.EOF) {
		second = nil
	}
	return errors.Join(first, second, closeErr)
}

type Opener interface {
	Open(context.Context, domain.Session, config.Worker) (io.ReadWriteCloser, error)
}

type Manager struct {
	Client *api.Client
	State  *local.State
	Config config.Worker
	Opener Opener
	Log    *slog.Logger
}

type Fence struct {
	Active bool   `json:"active"`
	ID     string `json:"id"`
}

func (m *Manager) Run(ctx context.Context) error {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			sessions, err := api.Call[[]domain.Session](ctx, m.Client, "GET", "/v1/sessions", nil)
			if err != nil {
				if ctx.Err() == nil {
					m.Log.Warn("session polling blocked", "code", domain.AsFault(err).Code)
				}
				continue
			}
			for _, session := range sessions {
				if session.TargetID != m.Client.Credential.AgentID || session.State != "consented" || !session.OpenRequested {
					continue
				}
				if err := m.start(ctx, session); err != nil {
					if domain.AsFault(err).Code != domain.Conflict {
						m.Log.Warn("session startup/closure blocked", "session", session.ID, "code", domain.AsFault(err).Code)
					}
				}
			}
		}
	}
}

func (m *Manager) start(ctx context.Context, session domain.Session) (err error) {
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	journals, err := m.State.Journals(ctx, true)
	if err != nil {
		return err
	}
	if len(journals) != 0 {
		return domain.Fail(domain.Prerequisite, "reconcile existing invocations before interactive access")
	}
	var hold struct {
		Active bool `json:"active"`
	}
	if _, err := m.State.Get(ctx, "maintenance", &hold); err != nil {
		return err
	}
	if hold.Active {
		return domain.Fail(domain.Prerequisite, "maintenance blocks sessions")
	}
	var fence Fence
	if _, err := m.State.Get(ctx, "session-fence", &fence); err != nil {
		return err
	}
	if fence.Active {
		return domain.Fail(domain.Prerequisite, "an earlier interactive session remains fenced")
	}
	if err := m.State.Put(ctx, "session-fence", Fence{Active: true, ID: session.ID}); err != nil {
		return err
	}
	sessionCtx, cancel := context.WithDeadline(ctx, time.Unix(session.ExpiresAt, 0))
	defer cancel()
	conn, err := Dial(sessionCtx, m.Client, session.ID, "target")
	if err != nil {
		return errors.Join(err, m.State.Put(context.WithoutCancel(ctx), "session-fence", Fence{}))
	}
	defer conn.Close()
	request := domain.SessionChange{Command: domain.Command{OperationID: session.ID + ".active", ExpectedRevision: session.Revision}, Action: "active"}
	if _, err := api.Call[domain.Session](sessionCtx, m.Client, "POST", api.Path("sessions", session.ID), request); err != nil {
		return errors.Join(err, m.State.Put(context.WithoutCancel(ctx), "session-fence", Fence{}))
	}
	endpoint, err := m.Opener.Open(sessionCtx, session, m.Config)
	if err != nil {
		return err
	}
	endpoint = &onceEndpoint{ReadWriteCloser: endpoint}
	bridgeErr := Bridge(sessionCtx, conn, endpoint)
	cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if endpointErr := endpoint.Close(); endpointErr != nil {
		return errors.Join(bridgeErr, endpointErr)
	}
	if err := m.State.Put(cleanupCtx, "session-fence", Fence{}); err != nil {
		return errors.Join(bridgeErr, err)
	}
	current, readErr := api.Call[domain.Session](cleanupCtx, m.Client, "GET", api.Path("sessions", session.ID), nil)
	if readErr != nil {
		return errors.Join(bridgeErr, readErr)
	}
	if current.State == "consented" || current.State == "active" {
		_, err = api.Call[domain.Session](cleanupCtx, m.Client, "POST", api.Path("sessions", session.ID), domain.SessionChange{Command: domain.Command{OperationID: session.ID + ".closed", ExpectedRevision: current.Revision}, Action: "close"})
	}

	if err != nil {
		return errors.Join(bridgeErr, err)
	}
	if bridgeErr != nil && !errors.Is(bridgeErr, context.Canceled) && !errors.Is(bridgeErr, context.DeadlineExceeded) {
		m.Log.Info("session transport ended", "session", session.ID, "code", domain.AsFault(bridgeErr).Code)
	}
	return nil
}

type onceEndpoint struct {
	io.ReadWriteCloser
	once sync.Once
	err  error
}

func (e *onceEndpoint) Close() error {
	e.once.Do(func() { e.err = e.ReadWriteCloser.Close() })
	return e.err
}
