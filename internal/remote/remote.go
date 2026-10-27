package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/display"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func Dial(ctx context.Context, client *api.Client, sessionID, side string) (net.Conn, error) {
	if client.IsMessageClient() {
		return api.DialMessageRelay(ctx, client, sessionID, side)
	}
	url := "wss" + strings.TrimPrefix(client.BaseURL, "https") + api.Path("sessions", sessionID, "relay", side)
	if client.Local {
		url = "ws" + strings.TrimPrefix(client.BaseURL, "http") + api.Path("sessions", sessionID, "relay", side)
	}
	header := make(http.Header)
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client.HTTP, HTTPHeader: header, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return nil, domain.Fail(domain.Denied, "relay identity or current assistance authority was denied")
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
	Client          *api.Client
	State           *local.State
	Config          config.Worker
	ConfigPath      string // Overrides Config.SourcePath when the caller did not load it through config.LoadWorker.
	Opener          Opener
	Log             *slog.Logger
	wakeOnce        sync.Once
	wake            chan struct{}
	externalUpdates atomic.Bool
}

type Fence struct {
	Active        bool   `json:"active"`
	ID            string `json:"id"`
	CleanupProven bool   `json:"cleanup_proven,omitempty"`
	Generation    string `json:"generation,omitempty"`
}

func (m *Manager) Wake() {
	m.wakeOnce.Do(func() { m.wake = make(chan struct{}, 1) })
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) UseExternalUpdates() { m.externalUpdates.Store(true) }

func (m *Manager) watchUpdates(ctx context.Context) {
	cursor := ""
	delay := time.Second
	for ctx.Err() == nil {
		page, err := api.Call[domain.Page[domain.Update]](ctx, m.Client, "GET", "/v1/updates?wait=true&limit=100&since="+cursor, nil)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.Log.Warn("session update subscription blocked", "code", domain.AsFault(err).Code)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		relevant := cursor == ""
		cursor, delay = page.NextCursor, time.Second
		for _, update := range page.Items {
			switch update.Topic {
			case "session", "assistance", "project", "task", "agent", "agent-mode", "agent-operation":
				relevant = true
			}
		}
		if relevant {
			m.Wake()
		}
	}
}

func (m *Manager) Run(ctx context.Context) error {
	if m.Client == nil || m.State == nil {
		return domain.Fail(domain.Invalid, "session manager needs its enrolled client and local state")
	}
	if m.Log == nil {
		m.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	publicationDone := make(chan struct{})
	go func() { defer close(publicationDone); m.publishLoop(ctx) }()
	var watchDone chan struct{}
	if !m.externalUpdates.Load() {
		watchDone = make(chan struct{})
		go func() { defer close(watchDone); m.watchUpdates(ctx) }()
	}
	defer func() {
		cancel()
		<-publicationDone
		if watchDone != nil {
			<-watchDone
		}
	}()
	m.Wake()
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	var pending []domain.Session
	needsSync := true
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
			needsSync = true
			delay = time.Second
		case <-retry.C:
		}
		retryPending := false
		if err := m.retryCleanup(ctx); err != nil {
			retryPending = true
			if domain.AsFault(err).Code != domain.Conflict {
				m.Log.Warn("proved session cleanup retirement blocked", "code", domain.AsFault(err).Code)
			}
		}
		if needsSync {
			sessions, err := api.Call[[]domain.Session](ctx, m.Client, "GET", "/v1/sessions", nil)
			if err != nil {
				if ctx.Err() == nil {
					m.Log.Warn("session update synchronization blocked", "code", domain.AsFault(err).Code)
				}
				retryPending = true
			} else {
				pending = pending[:0]
				for _, session := range sessions {
					if session.TargetID == m.Client.Credential.AgentID && session.State == "consented" && session.OpenRequested {
						pending = append(pending, session)
					}
				}
				needsSync = false
			}
		}
		if !needsSync {
			for _, session := range pending {
				if session.ExpiresAt <= time.Now().Unix() {
					continue
				}
				if err := m.start(ctx, session); err != nil {
					retryPending = true
					if domain.AsFault(err).Code != domain.Conflict {
						m.Log.Warn("session startup/closure blocked", "session", session.ID, "code", domain.AsFault(err).Code)
					}
				} else {
					m.Wake()
					break
				}
			}
		}
		if retryPending {
			retry.Stop()
			select {
			case <-retry.C:
			default:
			}
			retry.Reset(delay)
			delay = min(delay*2, 30*time.Second)
		}
	}
}

func (m *Manager) start(ctx context.Context, session domain.Session) (err error) {
	c, err := m.currentConfig()
	if err != nil {
		return err
	}
	slot, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
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
	if err := m.State.Put(ctx, "session-fence", Fence{Active: true, ID: session.ID, Generation: domain.NewID("fence")}); err != nil {
		return err
	}
	sessionCtx, cancel := context.WithDeadline(ctx, time.Unix(session.ExpiresAt, 0))
	defer cancel()
	conn, err := Dial(sessionCtx, m.Client, session.ID, "target")
	if err != nil {
		return errors.Join(err, m.finish(ctx, session.ID))
	}
	defer conn.Close()
	request := domain.SessionChange{Command: domain.Command{OperationID: session.ID + ".active", ExpectedRevision: session.Revision}, Action: "active"}
	if _, err := api.Call[domain.Session](sessionCtx, m.Client, "POST", api.Path("sessions", session.ID), request); err != nil {
		return errors.Join(err, m.finish(ctx, session.ID))
	}
	c, err = m.currentConfig()
	if err != nil {
		return errors.Join(err, m.finish(ctx, session.ID))
	}
	opener := m.Opener
	if opener == nil {
		opener = Native{}
	}
	endpoint, err := opener.Open(sessionCtx, session, c)
	if err != nil {
		var proof interface{ CleanupProven() bool }
		var uncertain *process.CleanupError
		clean := errors.As(err, &proof) && proof.CleanupProven() && !errors.As(err, &uncertain)
		if endpoint != nil {
			cleanupErr := endpoint.Close()
			clean = clean && cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		if clean {
			return errors.Join(err, m.finish(ctx, session.ID))
		}
		return err
	}
	if endpoint == nil {
		return domain.Fail(domain.Prerequisite, "native endpoint returned no cleanup proof; session remains fenced")
	}
	endpoint = &onceEndpoint{ReadWriteCloser: endpoint}
	if session.Mode == "vnc" {
		current, configErr := m.currentConfig()
		if configErr != nil || screenConfigFingerprint(current) != screenConfigFingerprint(c) {
			problem := errors.Join(configErr, domain.Fail(domain.Prerequisite, "screen configuration changed during native session preparation"))
			if cleanupErr := endpoint.Close(); cleanupErr != nil {
				return errors.Join(problem, cleanupErr)
			}
			return errors.Join(problem, m.finish(ctx, session.ID))
		}
		guardDone := m.watchScreenConfig(sessionCtx, cancel, c)
		defer func() { cancel(); <-guardDone }()
	}
	bridgeErr := Bridge(sessionCtx, conn, endpoint)
	if endpointErr := endpoint.Close(); endpointErr != nil {
		return errors.Join(bridgeErr, endpointErr)
	}
	var uncertain *process.CleanupError
	if errors.As(bridgeErr, &uncertain) {
		return bridgeErr
	}
	if err := m.finish(ctx, session.ID); err != nil {
		return errors.Join(bridgeErr, err)
	}
	if bridgeErr != nil && !errors.Is(bridgeErr, context.Canceled) && !errors.Is(bridgeErr, context.DeadlineExceeded) && m.Log != nil {
		m.Log.Info("session transport ended", "session", session.ID, "code", domain.AsFault(bridgeErr).Code)
	}
	return nil
}

// finish is called only after proving that this attempt has no remaining native
// endpoint. Persist that proof before a possibly lost remote retirement response.
func (m *Manager) finish(ctx context.Context, id string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var fence Fence
	if _, err := m.State.Get(cleanupCtx, "session-fence", &fence); err != nil {
		return err
	}
	if !fence.Active || fence.ID != id {
		return domain.Fail(domain.Prerequisite, "session fence changed; unrelated recovery evidence was retained")
	}
	fence.CleanupProven = true
	if err := m.State.Put(cleanupCtx, "session-fence", fence); err != nil {
		return err
	}
	current, err := api.Call[domain.Session](cleanupCtx, m.Client, "GET", api.Path("sessions", id), nil)
	if err != nil && domain.AsFault(err).Code != domain.NotFound {
		return err
	}
	if err == nil && (current.State == "consented" || current.State == "active") {
		if _, err := api.Call[domain.Session](cleanupCtx, m.Client, "POST", api.Path("sessions", id), domain.SessionChange{
			Command: domain.Command{OperationID: id + ".closed", ExpectedRevision: current.Revision}, Action: "close",
		}); err != nil {
			return err
		}
	}
	var final Fence
	if _, err := m.State.Get(cleanupCtx, "session-fence", &final); err != nil {
		return err
	}
	if final != fence {
		return domain.Fail(domain.Prerequisite, "session fence changed during retirement; recovery evidence was retained")
	}
	return m.State.Put(cleanupCtx, "session-fence", Fence{})
}

func (m *Manager) retryCleanup(ctx context.Context) (err error) {
	var fence Fence
	if _, err := m.State.Get(ctx, "session-fence", &fence); err != nil {
		return err
	}
	if !fence.Active || !fence.CleanupProven || !domain.ValidOperation(fence.ID) {
		return nil
	}
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	var current Fence
	if _, err := m.State.Get(ctx, "session-fence", &current); err != nil {
		return err
	}
	if current != fence {
		return domain.Fail(domain.Prerequisite, "session cleanup fence changed")
	}
	return m.finish(ctx, fence.ID)
}

func (m *Manager) currentConfig() (config.Worker, error) {
	c := m.Config
	path := m.ConfigPath
	if path == "" {
		path = c.SourcePath()
	}
	if path != "" {
		var err error
		c, err = config.LoadWorker(path)
		if err != nil {
			return config.Worker{}, err
		}
	} else if err := c.Validate(); err != nil {
		return config.Worker{}, err
	}
	if c.StateDir != m.Config.StateDir || c.CredentialFile != m.Config.CredentialFile || c.IdentitySocket != m.Config.IdentitySocket ||
		c.URL != m.Config.URL || c.CAFile != m.Config.CAFile || c.ServerName != m.Config.ServerName {
		return config.Worker{}, domain.Fail(domain.Prerequisite, "worker identity or state configuration changed; restart rather than borrowing a different identity")
	}
	return c, nil
}

func screenConfigFingerprint(c config.Worker) string {
	encoded, _ := json.Marshal(struct {
		Screens  []config.Screen
		Address  string
		Verified bool
	}{c.Screens, c.VNCAddress, c.VNCVerified})
	return domain.Hash(string(encoded))
}

func (m *Manager) watchScreenConfig(ctx context.Context, cancel context.CancelFunc, initial config.Worker) <-chan struct{} {
	done := make(chan struct{})
	fingerprint := screenConfigFingerprint(initial)
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := m.currentConfig()
				if err != nil || screenConfigFingerprint(current) != fingerprint {
					cancel()
					return
				}
			}
		}
	}()
	return done
}

type screensPublication struct {
	Fingerprint  string          `json:"fingerprint"`
	OperationID  string          `json:"operation_id"`
	Screens      []domain.Screen `json:"screens"`
	Acknowledged bool            `json:"acknowledged"`
}

func publicScreens(screens []domain.Screen) ([]domain.Screen, string) {
	result := make([]domain.Screen, 0, len(screens))
	for _, screen := range screens {
		screen.Applications = append([]string{}, screen.Applications...)
		slices.Sort(screen.Applications)
		result = append(result, screen)
	}
	slices.SortFunc(result, func(a, b domain.Screen) int { return strings.Compare(a.ID, b.ID) })
	data, _ := json.Marshal(result)
	return result, domain.Hash(string(data))
}

// PublishScreens is also usable without an inference engine or automatic mode.
func (m *Manager) PublishScreens(ctx context.Context) error {
	if m.Client == nil || m.State == nil {
		return domain.Fail(domain.Invalid, "screen publication needs its enrolled client and local state")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c, readErr := m.currentConfig()
	screens := []domain.Screen{}
	var inventoryErr error
	if readErr == nil {
		screens, inventoryErr = display.Inventory(ctx, c)
	}
	return errors.Join(readErr, inventoryErr, m.syncScreens(ctx, screens, true))
}

func (m *Manager) syncScreens(ctx context.Context, screens []domain.Screen, verifyRemote bool) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := secure.Acquire(filepath.Join(m.Config.StateDir, "screens.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	screens, fingerprint := publicScreens(screens)
	if err := domain.ValidateScreens(screens); err != nil {
		return err
	}
	var previous screensPublication
	found, err := m.State.Get(ctx, "screens-publication", &previous)
	if err != nil {
		return err
	}
	path := api.Path("agents", m.Client.Credential.AgentID, "screens")
	publish := func(proposal *screensPublication) error {
		request := domain.ScreensRequest{Command: domain.Command{OperationID: proposal.OperationID}, Screens: proposal.Screens}
		if err := m.Client.Request(ctx, "POST", path, request, nil); err != nil {
			return err
		}
		proposal.Acknowledged = true
		return m.State.Put(ctx, "screens-publication", proposal)
	}
	if found && !previous.Acknowledged {
		if err := publish(&previous); err != nil {
			return err
		}
	}
	if found && previous.Fingerprint == fingerprint {
		if !verifyRemote {
			return nil
		}
		remote, err := api.Call[[]domain.Screen](ctx, m.Client, "GET", path, nil)
		if err != nil {
			return err
		}
		_, current := publicScreens(remote)
		if current == fingerprint {
			return nil
		}
	}
	proposal := screensPublication{Fingerprint: fingerprint, OperationID: domain.NewID("screens"), Screens: screens}
	if err := m.State.Put(ctx, "screens-publication", proposal); err != nil {
		return err
	}
	return publish(&proposal)
}

func (m *Manager) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastConfig string
	var lastProbe, retryAt time.Time
	var observed []domain.Screen
	var observationErr error
	backoff, first := time.Second, true
	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		verifyRemote := first
		c, readErr := m.currentConfig()
		fingerprint := screenConfigFingerprint(c)
		if readErr != nil {
			fingerprint = "invalid-" + string(domain.AsFault(readErr).Code)
		}
		if fingerprint != lastConfig || now.Sub(lastProbe) >= 30*time.Second {
			verifyRemote = true
			observed, observationErr = []domain.Screen{}, readErr
			if readErr == nil {
				observed, observationErr = display.Inventory(ctx, c)
			}
			lastConfig, lastProbe, retryAt = fingerprint, time.Now(), time.Time{}
		}
		if !now.Before(retryAt) {
			err := errors.Join(observationErr, m.syncScreens(ctx, observed, verifyRemote))
			if err != nil {
				if ctx.Err() == nil {
					m.Log.Warn("screen capability publication blocked", "code", domain.AsFault(err).Code)
				}
				backoff = min(backoff*2, 30*time.Second)
			} else {
				first, backoff = false, time.Second
			}
			retryAt = time.Now().Add(backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
