package mobile

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/remote"
)

type humanSession struct {
	id        uint64
	session   domain.Session
	conn      net.Conn
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	bootstrap string
	result    domain.Session
	err       error
}

type humanOpen struct {
	GrantID       string `json:"grant_id"`
	HelpMessageID string `json:"help_message_id"`
	Provider      string `json:"provider,omitempty"`
	AssetsDir     string `json:"assets_dir,omitempty"`
}

type humanProposal struct {
	GrantID   string                `json:"grant_id"`
	Request   domain.SessionRequest `json:"request"`
	SessionID string                `json:"session_id,omitempty"`
}

func (r *Runtime) openHuman(ctx context.Context, q Request) (any, error) {
	if !domain.ValidOperation(q.OperationID) {
		return nil, domain.Fail(domain.Invalid, "human session requires an exact operation")
	}
	f, err := r.active()
	if err != nil {
		return nil, err
	}
	return r.openHumanIn(ctx, q, f)
}

func (r *Runtime) openHumanIn(ctx context.Context, q Request, f *foreground) (any, error) {
	me, err := api.Call[domain.Agent](ctx, f.client, "GET", "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	if !me.Active || !me.Founder || me.Mode != "manual" {
		return nil, domain.Fail(domain.Denied, "human assistance requires this active Founder's manual mode")
	}
	var input humanOpen
	if err := decode(q.Body, &input); err != nil {
		return nil, err
	}
	grant, err := api.Call[domain.AssistanceGrant](ctx, f.client, "GET", api.Path("assistance", input.GrantID), nil)
	if err != nil {
		return nil, err
	}
	if !grant.Active || grant.RequesterID != me.ID || (grant.Provider != "" && grant.Provider != input.Provider) {
		return nil, domain.Fail(domain.Denied, "human session is outside the current retained grant")
	}
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.RLock()
	previous := r.human
	r.mu.RUnlock()
	if previous != nil {
		select {
		case <-previous.done:
		default:
			return nil, domain.Fail(domain.Conflict, "close the existing protected human viewer first")
		}
	}
	key := "assistance-open:" + q.OperationID
	proposal := humanProposal{}
	found, err := f.state.Get(ctx, key, &proposal)
	if err != nil {
		return nil, err
	}
	if !found {
		task := ""
		if input.HelpMessageID != "" {
			help, err := api.Call[domain.Message](ctx, f.client, "GET", api.Path("messages", input.HelpMessageID), nil)
			if err != nil {
				return nil, err
			}
			if help.Help == nil {
				return nil, domain.Fail(domain.Denied, "help message has no typed assistance scope")
			}
			task = help.Help.TaskID
		}
		proposal = humanProposal{GrantID: grant.ID, Request: domain.SessionRequest{
			Command:  domain.Command{OperationID: "assist_" + domain.Hash(q.OperationID)},
			TargetID: grant.TargetID, GrantID: grant.ID, ProjectID: grant.ProjectID, TaskID: task,
			Mode: grant.Mode, Purpose: grant.Purpose, Provider: input.Provider, ScreenID: grant.ScreenID,
			ApplicationID: grant.ApplicationID, DesktopFallback: grant.DesktopFallback,
			HelpMessageID: input.HelpMessageID, Reason: "Explicit protected mobile human assistance",
			ExpiresAt: time.Now().Unix() + 300,
		}}
		if err := f.state.Put(ctx, key, proposal); err != nil {
			return nil, err
		}
	} else if proposal.GrantID != grant.ID || proposal.Request.HelpMessageID != input.HelpMessageID ||
		proposal.Request.Provider != input.Provider {
		return nil, domain.Fail(domain.Conflict, "retained assistance operation belongs to another exact scope")
	}
	session, err := api.Call[domain.Session](ctx, f.client, "POST", "/v1/sessions", proposal.Request)
	if err != nil {
		return nil, err
	}
	proposal.SessionID = session.ID
	if err := f.state.Put(ctx, key, proposal); err != nil {
		return nil, err
	}
	session, err = api.Call[domain.Session](ctx, f.client, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: q.OperationID + ".open", ExpectedRevision: session.Revision}, Action: "open",
	})
	if err != nil {
		return nil, err
	}
	live, cancel := context.WithDeadline(f.context, time.Unix(session.ExpiresAt, 0))
	h := &humanSession{session: session, cancel: cancel, done: make(chan struct{})}
	if session.Mode == "tmux" {
		h.conn, err = remote.Dial(live, f.client, session.ID, "requester")
		if err != nil {
			cancel()
			return nil, err
		}
	} else if session.Mode != "vnc" || input.AssetsDir == "" {
		cancel()
		return nil, domain.Fail(domain.Invalid, "VNC requires bundled trusted viewer assets")
	}
	r.mu.Lock()
	r.nextHuman++
	h.id, r.human = r.nextHuman, h
	r.mu.Unlock()
	go func() {
		var viewerErr error
		if session.Mode == "vnc" {
			viewerErr = remote.AttachBrowserVNC(live, f.client, session.ID,
				remote.BrowserOptions{Address: "127.0.0.1:0", AssetsDir: input.AssetsDir}, humanBootstrap{h})
		} else {
			<-live.Done()
		}
		cancel()
		if h.conn != nil {
			viewerErr = errors.Join(viewerErr, h.conn.Close())
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		current, closeErr := api.Call[domain.Session](cleanup, f.client, "GET", api.Path("sessions", session.ID), nil)
		if closeErr == nil && !domain.TerminalSessionState(current.State) {
			current, closeErr = api.Call[domain.Session](cleanup, f.client, "POST", api.Path("sessions", session.ID), domain.SessionChange{
				Command: domain.Command{OperationID: q.OperationID + ".close", ExpectedRevision: current.Revision}, Action: "close",
			})
		}
		if errors.Is(viewerErr, context.Canceled) || errors.Is(viewerErr, context.DeadlineExceeded) || errors.Is(viewerErr, net.ErrClosed) {
			viewerErr = nil
		}
		h.mu.Lock()
		h.result, h.err = current, errors.Join(viewerErr, closeErr)
		h.bootstrap = ""
		h.mu.Unlock()
		close(h.done)
	}()
	return map[string]any{"human_id": h.id, "session": session}, nil
}

type humanBootstrap struct{ session *humanSession }

func (w humanBootstrap) Write(data []byte) (int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "http://127.0.0.1:") && strings.Contains(line, "/bootstrap?key=") && len(line) < 4096 {
			w.session.mu.Lock()
			w.session.bootstrap = line
			w.session.mu.Unlock()
		}
	}
	return len(data), nil
}

func (r *Runtime) humanSession(id uint64) (*humanSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.human == nil || r.human.id != id || id == 0 {
		return nil, domain.Fail(domain.Denied, "protected viewer is not owned by this runtime")
	}
	return r.human, nil
}

// HumanRead/HumanWrite are binary-only app-private capabilities. Terminal bytes
// never enter JSON dispatch, observer events, proposal records or diagnostics.
func (r *Runtime) HumanRead(id uint64, buffer []byte) (int, error) {
	h, err := r.humanSession(id)
	if err != nil {
		return 0, err
	}
	if h.conn == nil || len(buffer) == 0 || len(buffer) > 64<<10 {
		return 0, domain.Fail(domain.Invalid, "bounded terminal stream required")
	}
	return h.conn.Read(buffer)
}

func (r *Runtime) HumanWrite(id uint64, data []byte) (int, error) {
	h, err := r.humanSession(id)
	if err != nil {
		return 0, err
	}
	if h.conn == nil || len(data) == 0 || len(data) > 64<<10 {
		return 0, domain.Fail(domain.Invalid, "bounded terminal input required")
	}
	return h.conn.Write(data)
}

func (r *Runtime) HumanURL(id uint64) (string, error) {
	h, err := r.humanSession(id)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bootstrap, nil
}

func (r *Runtime) CloseHuman(id uint64) error {
	h, err := r.humanSession(id)
	if err != nil {
		return err
	}
	h.cancel()
	if h.conn != nil {
		if err := h.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}

func (r *Runtime) stopHuman() error {
	r.mu.RLock()
	h := r.human
	r.mu.RUnlock()
	if h == nil {
		return nil
	}
	h.cancel()
	var err error
	if h.conn != nil {
		err = h.conn.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(err, h.err)
}

func (r *Runtime) waitHuman(ctx context.Context, id uint64) (domain.Session, error) {
	h, err := r.humanSession(id)
	if err != nil {
		return domain.Session{}, err
	}
	select {
	case <-ctx.Done():
		return domain.Session{}, ctx.Err()
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.result, h.err
	}
}

var _ io.Writer = humanBootstrap{}
