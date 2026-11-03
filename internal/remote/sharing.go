package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/display"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type sharingPublication struct {
	Fingerprint  string            `json:"fingerprint"`
	OperationID  string            `json:"operation_id"`
	Terminals    []domain.Terminal `json:"terminals"`
	Acknowledged bool              `json:"acknowledged"`
}

type sharingInputRecord struct {
	State      string          `json:"state"`
	Session    domain.Session  `json:"session"`
	Terminal   domain.Terminal `json:"terminal"`
	Fence      Fence           `json:"fence"`
	OwnerPID   int             `json:"owner_pid"`
	OwnerStart string          `json:"owner_start"`
}

type sharedConnection struct {
	cancel  context.CancelFunc
	done    chan struct{}
	input   atomic.Bool
	inputMu sync.RWMutex
}

func (connection *sharedConnection) inputAllowed() bool {
	connection.inputMu.RLock()
	defer connection.inputMu.RUnlock()
	return connection.input.Load()
}

func (m *Manager) PublishSharing(ctx context.Context) (err error) {
	backend := newNativeSharingBackend(ctx)
	defer func() { err = errors.Join(err, backend.Close()) }()
	return m.publishSharing(ctx, backend)
}

func sharingInputKey(id string) string { return "sharing-input:" + id }

func (m *Manager) publishSharing(ctx context.Context, backend SharingBackend) error {
	c, err := m.currentConfig()
	if err != nil {
		return err
	}
	terminals, inventoryErr := backend.Inventory(ctx, c, m.State)
	if terminals == nil {
		terminals = []domain.Terminal{}
	}
	if err := domain.ValidateTerminals(terminals); err != nil {
		return err
	}
	encoded, err := json.Marshal(terminals)
	if err != nil {
		return err
	}
	fingerprint := domain.Hash(string(encoded))
	var previous sharingPublication
	found, err := m.State.Get(ctx, "sharing-publication", &previous)
	if err != nil {
		return err
	}
	publish := func(proposal *sharingPublication) error {
		_, err := api.Call[domain.Sharing](ctx, m.Client, "POST", api.Path("agents", m.Client.Credential.AgentID, "sharing"),
			domain.SharingPublishRequest{Command: domain.Command{OperationID: proposal.OperationID}, Terminals: proposal.Terminals})
		if err != nil {
			return err
		}
		proposal.Acknowledged = true
		return m.State.Put(ctx, "sharing-publication", proposal)
	}
	if found && !previous.Acknowledged {
		if err := publish(&previous); err != nil {
			return errors.Join(inventoryErr, err)
		}
	}
	if found && previous.Fingerprint == fingerprint {
		return inventoryErr
	}
	proposal := sharingPublication{Fingerprint: fingerprint, OperationID: domain.NewID("sharing"), Terminals: terminals}
	if err := m.State.Put(ctx, "sharing-publication", proposal); err != nil {
		return err
	}
	return errors.Join(inventoryErr, publish(&proposal))
}

func (m *Manager) runSharing(ctx context.Context, backend SharingBackend) {
	active := map[string]*sharedConnection{}
	changed := make(chan struct{}, 1)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		filter, _ := json.Marshal(domain.UpdateFilter{Topics: []string{"session", "sharing", "assistance", "agent", "task"}})
		cursor, delay := "", time.Second
		for ctx.Err() == nil {
			page, err := api.Call[domain.Page[domain.Update]](ctx, m.Client, "GET",
				"/v1/updates?wait=true&limit=100&filter="+url.QueryEscape(string(filter))+"&since="+url.QueryEscape(cursor), nil)
			if err == nil {
				cursor, delay = page.NextCursor, time.Second
				select {
				case changed <- struct{}{}:
				default:
				}
				continue
			}
			if domain.AsFault(err).Code == domain.Conflict {
				cursor = ""
			}
			if ctx.Err() == nil {
				m.Log.Warn("sharing authority reception blocked", "code", domain.AsFault(err).Code)
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(30*time.Second, delay*2)
		}
	}()
	defer func() {
		for _, connection := range active {
			connection.cancel()
		}
		for _, connection := range active {
			<-connection.done
		}
		<-watchDone
		if err := backend.Close(); err != nil {
			m.Log.Warn("sharing native cleanup remains unproven", "code", domain.AsFault(err).Code)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	reconsider := true
	lastPublicationError := domain.Code("")
	for ctx.Err() == nil {
		if err := m.recoverSharingInput(ctx, backend); err != nil && domain.AsFault(err).Code != domain.Conflict {
			m.Log.Warn("sharing input cleanup remains fenced", "code", domain.AsFault(err).Code)
		}
		if err := m.publishSharing(ctx, backend); err != nil {
			code := domain.AsFault(err).Code
			if code != lastPublicationError {
				m.Log.Warn("sharing capability publication blocked", "code", code)
				lastPublicationError = code
			}
		} else {
			lastPublicationError = ""
		}
		for id, connection := range active {
			select {
			case <-connection.done:
				delete(active, id)
				reconsider = true
			default:
			}
		}
		if reconsider {
			started := time.Now()
			sessions, err := api.Call[[]domain.Session](ctx, m.Client, "GET", "/v1/sessions", nil)
			if err != nil {
				if ctx.Err() == nil {
					m.Log.Warn("sharing session synchronization blocked", "code", domain.AsFault(err).Code)
				}
			} else {
				reconsider = false
				for _, session := range sessions {
					if !session.Sharing() || session.TargetID != m.Client.Credential.AgentID || !session.OpenRequested ||
						!slices.Contains([]string{"consented", "active"}, session.State) || active[session.ID] != nil {
						continue
					}
					deadline, err := session.SharingDeadline(started)
					if err != nil {
						continue
					}
					if len(active) >= 32 {
						m.Log.Warn("sharing viewer bound reached", "code", domain.Prerequisite)
						continue
					}
					streamCtx, cancel := context.WithDeadline(ctx, deadline)
					connection := &sharedConnection{cancel: cancel, done: make(chan struct{})}
					active[session.ID] = connection
					go func(session domain.Session) {
						defer close(connection.done)
						defer cancel()
						if err := m.serveSharing(streamCtx, backend, session, connection); err != nil && ctx.Err() == nil {
							m.Log.Warn("sharing stream ended", "session", session.ID, "code", domain.AsFault(err).Code)
							if streamCtx.Err() == nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) &&
								!errors.Is(err, net.ErrClosed) && domain.AsFault(err).Code != domain.Unavailable && domain.AsFault(err).Code != domain.Conflict {
								cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
								current, readErr := api.Call[domain.Session](cleanupCtx, m.Client, "GET", api.Path("sessions", session.ID), nil)
								if readErr == nil && current.SelectionRevision == session.SelectionRevision &&
									slices.Contains([]string{"consented", "active"}, current.State) {
									_, readErr = m.sharingChange(cleanupCtx, current, "fail")
								}
								stop()
								if readErr != nil {
									m.Log.Warn("sharing failure report pending", "session", session.ID, "code", domain.AsFault(readErr).Code)
								}
							}
						}
					}(session)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			reconsider = true
		case <-ticker.C:
		}
	}
}

func (m *Manager) sharingChange(ctx context.Context, session domain.Session, action string) (domain.Session, error) {
	key := "sharing-change:" + session.ID + ":" + action
	var request domain.SessionChange
	found, err := m.State.Get(ctx, key, &request)
	if err != nil {
		return session, err
	}
	if !found {
		request = domain.SessionChange{Command: domain.Command{OperationID: domain.Hash(key), ExpectedRevision: session.Revision}, Action: action}
		if err := m.State.Put(ctx, key, request); err != nil {
			return session, err
		}
	}
	return api.Call[domain.Session](ctx, m.Client, "POST", api.Path("sessions", session.ID), request)
}

func (m *Manager) serveSharing(ctx context.Context, backend SharingBackend, session domain.Session, connection *sharedConnection) (result error) {
	c, err := m.currentConfig()
	if err != nil {
		return err
	}
	state, err := api.Call[domain.Sharing](ctx, m.Client, "GET", api.Path("agents", session.TargetID, "sharing"), nil)
	if err != nil {
		return err
	}
	if state.SelectionRevision != session.SelectionRevision {
		return domain.Fail(domain.Conflict, "sharing selection changed before native connection")
	}
	var terminal domain.Terminal
	for _, candidate := range state.Terminals {
		if candidate.ID == session.TerminalID {
			terminal = candidate
		}
	}
	var endpoint io.ReadWriteCloser
	var password []byte
	if session.Mode == "tmux" {
		endpoint, err = backend.Open(ctx, c, m.State, session, terminal)
	} else {
		nativeSession := session
		deadline, clockErr := session.SharingDeadline(time.Now())
		if clockErr != nil {
			return clockErr
		}
		nativeSession.ExpiresAt = deadline.Unix()
		endpoint, err = display.Open(ctx, c, nativeSession)
		for _, screen := range c.Screens {
			if err != nil {
				break
			}
			if screen.ID == session.ScreenID && screen.VNCPasswordFile != "" {
				password, err = secure.Read(screen.VNCPasswordFile, 8)
				if err != nil {
					break
				}
				if len(password) == 0 {
					err = domain.Fail(domain.Prerequisite, "VNC sharing requires a nonempty private backend password")
				}
			}
		}
	}
	if err != nil {
		if endpoint != nil {
			err = errors.Join(err, endpoint.Close())
		}
		return err
	}
	if endpoint == nil {
		return domain.Fail(domain.Prerequisite, "sharing backend returned no owned endpoint")
	}
	endpoint = &onceEndpoint{ReadWriteCloser: endpoint}
	defer func() { result = errors.Join(result, endpoint.Close()) }()
	if session.Mode == "vnc" {
		guardDone := m.watchScreenConfig(ctx, connection.cancel, c)
		defer func() { connection.cancel(); <-guardDone }()
	}
	if session.State == "consented" {
		session, err = m.sharingChange(ctx, session, "active")
		if err != nil {
			return err
		}
	}
	conn, err := Dial(ctx, m.Client, session.ID, "target")
	if err != nil {
		return err
	}
	defer conn.Close()
	controlCtx, cancel := context.WithCancel(ctx)
	controlDone := make(chan error, 1)
	go func() {
		err := m.sharingControl(controlCtx, backend, c, session, terminal, endpoint, connection)
		if err != nil {
			connection.cancel()
		}
		controlDone <- err
	}()
	defer func() {
		cancel()
		connection.input.Store(false)
		result = errors.Join(result, <-controlDone)
	}()
	if session.Mode == "tmux" {
		return bridgeSharedTerminal(ctx, conn, endpoint, connection.inputAllowed)
	}
	if len(password) != 0 {
		return BridgeAuthenticatedVNC(ctx, conn, endpoint, password, connection.inputAllowed)
	}
	requester, err := api.Call[domain.Agent](ctx, m.Client, "GET", api.Path("agents", session.RequesterID), nil)
	if err != nil {
		return err
	}
	if !requester.Founder {
		return domain.Fail(domain.Prerequisite, "delegate VNC needs target-owned private backend authentication; credentials cannot be handed to the delegate")
	}
	return BridgeSharedVNC(ctx, conn, endpoint, connection.inputAllowed)
}

func bridgeSharedTerminal(ctx context.Context, viewer, terminal io.ReadWriteCloser, inputAllowed func() bool) error {
	inReader, inWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer inWriter.Close()
		buffer := make([]byte, 4096)
		for {
			n, err := viewer.Read(buffer)
			if n > 0 && inputAllowed() {
				if _, writeErr := inWriter.Write(buffer[:n]); writeErr != nil {
					done <- writeErr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err := Bridge(ctx, structReadWriteCloser{Reader: inReader, Writer: viewer, Closer: viewer}, terminal)
	closeErr := inReader.Close()
	return errors.Join(err, closeErr, <-done)
}

type structReadWriteCloser struct {
	io.Reader
	io.Writer
	io.Closer
}

func (m *Manager) sharingControl(ctx context.Context, backend SharingBackend, c config.Worker, session domain.Session, terminal domain.Terminal, endpoint io.ReadWriteCloser, connection *sharedConnection) (result error) {
	var slot *secure.Lock
	var lease sharingInputRecord
	prepared := false
	defer func() {
		connection.input.Store(false)
		if slot == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		var cleanupErr error
		if prepared {
			lease.State = "closing"
			cleanupErr = m.State.Put(cleanupCtx, sharingInputKey(lease.Session.ID), lease)
			cleanupErr = errors.Join(cleanupErr, endpoint.Close(), backend.EndInput(cleanupCtx, c, m.State, lease.Session))
			if cleanupErr == nil {
				cleanupErr = m.finish(cleanupCtx, lease.Session.ID)
			}
		}
		result = errors.Join(result, cleanupErr, slot.Close())
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		current, err := api.Call[domain.Session](ctx, m.Client, "GET", api.Path("sessions", session.ID), nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !slices.Contains([]string{"consented", "active"}, current.State) || current.InputRequestedBy == "" {
			if slot != nil {
				return nil
			}
		} else if slot == nil {
			if current.SelectionRevision != session.SelectionRevision || current.InputLeaseID == "" {
				return domain.Fail(domain.Conflict, "input request no longer matches this exact selected resource")
			}
			inputLease, readErr := api.Call[domain.Session](ctx, m.Client, "GET", api.Path("sessions", current.InputLeaseID), nil)
			if readErr != nil {
				return readErr
			}
			slot, err = secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
			if err != nil && domain.AsFault(err).Code != domain.Conflict {
				return err
			}
			if err == nil {
				journals, journalErr := m.State.Journals(ctx, true)
				var fence Fence
				_, fenceErr := m.State.Get(ctx, "session-fence", &fence)
				var maintenance struct {
					Active bool `json:"active"`
				}
				_, maintenanceErr := m.State.Get(ctx, "maintenance", &maintenance)
				if journalErr != nil || fenceErr != nil || maintenanceErr != nil || len(journals) != 0 || fence.Active || maintenance.Active {
					closeErr := slot.Close()
					slot = nil
					if failure := errors.Join(journalErr, fenceErr, maintenanceErr, closeErr); failure != nil {
						return failure
					}
				} else {
					owner, err := inspectProcess(os.Getpid())
					if err != nil || owner == nil {
						return errors.Join(err, domain.Fail(domain.Prerequisite, "input handoff has no native owner identity"))
					}
					lease = sharingInputRecord{State: "prepared", Session: inputLease, Terminal: terminal,
						Fence:    Fence{Active: true, ID: inputLease.ID, Generation: domain.NewID("handoff")},
						OwnerPID: owner.PID, OwnerStart: owner.Started}
					if err := m.State.Put(ctx, sharingInputKey(inputLease.ID), lease); err != nil {
						return err
					}
					if err := m.State.Put(ctx, "session-fence", lease.Fence); err != nil {
						return err
					}
					prepared = true
					if err := m.acknowledgeSharingInput(ctx, connection, &lease); err != nil {
						return err
					}
				}

			}
		} else {
			connection.input.Store(current.InputGranted && current.InputRequestedBy != "")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (m *Manager) acknowledgeSharingInput(ctx context.Context, connection *sharedConnection, lease *sharingInputRecord) error {
	// Input arriving during acknowledgement must observe its completed result,
	// not race the server-visible grant against durable native readiness.
	connection.inputMu.Lock()
	defer connection.inputMu.Unlock()
	granted, err := m.sharingChange(ctx, lease.Session, "grant-input")
	if err != nil {
		return err
	}
	lease.State, lease.Session = "granted", granted
	if err := m.State.Put(ctx, sharingInputKey(granted.ID), *lease); err != nil {
		return err
	}
	connection.input.Store(granted.InputGranted)
	return nil
}
