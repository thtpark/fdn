package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
)

type managerFixture struct {
	manager       *Manager
	mu            sync.Mutex
	session       domain.Session
	screens       []domain.Screen
	posts         []domain.ScreensRequest
	ops           map[string][]domain.Screen
	lostAck       bool
	closeFailure  bool
	closeCount    int
	otherRequests []string
	opened        atomic.Int64
	relays        []*websocket.Conn
}

func envelope(w http.ResponseWriter, data any, fault error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
	if fault != nil {
		w.WriteHeader(domain.HTTPStatus(fault))
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": domain.AsFault(fault)})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func newManagerFixture(t *testing.T) *managerFixture {
	t.Helper()
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	c.Mode = "manual"
	state, err := local.Open(context.Background(), c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	fixture := &managerFixture{screens: []domain.Screen{}, ops: map[string][]domain.Screen{}, session: domain.Session{
		ID: "session_fixture", TargetID: "agent_fixture", State: "consented", OpenRequested: true,
		Mode: "vnc", ScreenID: "right", ExpiresAt: time.Now().Unix() + 60, Revision: 1,
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serveHTTP(w, r) }))
	t.Cleanup(func() {
		fixture.mu.Lock()
		for _, relay := range fixture.relays {
			relay.CloseNow()
		}
		fixture.mu.Unlock()
		server.Close()
	})
	fixture.manager = &Manager{Client: &api.Client{BaseURL: server.URL, HTTP: server.Client(), Local: true, Credential: config.Credential{AgentID: "agent_fixture"}},
		State: state, Config: c, Opener: Native{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return fixture
}

func (f *managerFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/updates" {
		if r.URL.Query().Get("since") == "" {
			envelope(w, domain.Page[domain.Update]{Items: []domain.Update{}, NextCursor: "fixture"}, nil)
		} else {
			<-r.Context().Done()
		}
		return
	}
	if r.URL.Path == "/v1/sessions/session_fixture/relay/target" {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.relays = append(f.relays, conn)
		f.mu.Unlock()
		defer conn.CloseNow()
		_, _ = io.Copy(io.Discard, websocket.NetConn(r.Context(), conn, websocket.MessageBinary))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/agents/agent_fixture/screens":
		if r.Method == "GET" {
			envelope(w, f.screens, nil)
			return
		}
		var request domain.ScreensRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			envelope(w, nil, domain.Fail(domain.Invalid, "invalid publication"))
			return
		}
		f.posts = append(f.posts, request)
		if existing, ok := f.ops[request.OperationID]; ok {
			if !reflect.DeepEqual(existing, request.Screens) {
				envelope(w, nil, domain.Fail(domain.Conflict, "changed immutable screen publication"))
				return
			}
			envelope(w, existing, nil)
			return
		}
		f.ops[request.OperationID] = request.Screens
		f.screens = request.Screens
		if f.lostAck {
			f.lostAck = false
			envelope(w, nil, domain.Fail(domain.Unavailable, "fixture lost publication acknowledgement"))
			return
		}
		envelope(w, f.screens, nil)
	case "/v1/sessions":
		envelope(w, []domain.Session{f.session}, nil)
	case "/v1/sessions/session_fixture":
		if r.Method == "GET" {
			envelope(w, f.session, nil)
			return
		}
		var request domain.SessionChange
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			envelope(w, nil, domain.Fail(domain.Invalid, "invalid session change"))
			return
		}
		if request.ExpectedRevision != f.session.Revision {
			envelope(w, nil, domain.Fail(domain.Conflict, "stale session"))
			return
		}
		switch request.Action {
		case "active":
			f.session.State = "active"
			f.opened.Add(1)
		case "close":
			f.closeCount++
			f.session.State = "closed"
		default:
			envelope(w, nil, domain.Fail(domain.Invalid, "unexpected session action"))
			return
		}
		f.session.Revision++
		if request.Action == "close" && f.closeFailure {
			f.closeFailure = false
			envelope(w, nil, domain.Fail(domain.Unavailable, "fixture lost close acknowledgement"))
			return
		}
		envelope(w, f.session, nil)
	default:
		f.otherRequests = append(f.otherRequests, r.Method+" "+r.URL.Path)
		envelope(w, nil, domain.Fail(domain.NotFound, "unexpected fixture request"))
	}
}

func (f *managerFixture) snapshot() (domain.Session, []domain.ScreensRequest, []domain.Screen) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session, append([]domain.ScreensRequest{}, f.posts...), append([]domain.Screen{}, f.screens...)
}

func TestManualManagerPublishesHeadlessInventoryWithoutModeOrRevisionWrites(t *testing.T) {
	f := newManagerFixture(t)
	before, _ := json.Marshal(f.manager.Config)
	f.mu.Lock()
	f.session.OpenRequested = false
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.manager.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, requests, _ := f.snapshot()
		if len(requests) != 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("manual manager did not publish at startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(1200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, requests, screens := f.snapshot()
	if len(requests) != 1 || len(requests[0].Screens) != 0 || requests[0].Screens == nil || len(screens) != 0 || requests[0].ExpectedRevision != 0 {
		t.Fatal("empty headless inventory was fabricated or repeatedly published", requests)
	}
	after, _ := json.Marshal(f.manager.Config)
	f.mu.Lock()
	otherRequests := len(f.otherRequests)
	f.mu.Unlock()
	if string(before) != string(after) || f.manager.Config.Mode != "manual" || otherRequests != 0 || f.opened.Load() != 0 {
		t.Fatal("inventory publication changed mode/revision or launched native/inference work")
	}
}

func TestScreenPublicationLostAcknowledgementReusesDurableProposal(t *testing.T) {
	f := newManagerFixture(t)
	f.lostAck = true
	if err := f.manager.PublishScreens(context.Background()); err == nil {
		t.Fatal("lost acknowledgement was hidden")
	}
	restarted := &Manager{Client: f.manager.Client, State: f.manager.State, Config: f.manager.Config,
		ConfigPath: f.manager.ConfigPath, Opener: f.manager.Opener, Log: f.manager.Log}
	if err := restarted.PublishScreens(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, requests, _ := f.snapshot()
	if len(requests) != 2 || requests[0].OperationID != requests[1].OperationID || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("lost acknowledgement changed the immutable operation", requests)
	}
	var saved screensPublication
	if ok, err := f.manager.State.Get(context.Background(), "screens-publication", &saved); err != nil || !ok || !saved.Acknowledged {
		t.Fatal("publication receipt was not durable", saved, err)
	}
	if err := restarted.PublishScreens(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, requests, _ = f.snapshot()
	if len(requests) != 2 {
		t.Fatal("unchanged acknowledged inventory was published again")
	}
}

func TestPublicationChangesAndRevisitingOldInventoryUseFreshOperations(t *testing.T) {
	f := newManagerFixture(t)
	ctx := context.Background()
	one := []domain.Screen{{ID: "right", Name: "Right screen", Scope: "screen", Width: 1280, Height: 720, Applications: []string{"browser"}}}
	if err := f.manager.syncScreens(ctx, one, false); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.syncScreens(ctx, []domain.Screen{}, false); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.syncScreens(ctx, one, false); err != nil {
		t.Fatal(err)
	}
	_, requests, current := f.snapshot()
	if len(requests) != 3 || requests[0].OperationID == requests[2].OperationID || !reflect.DeepEqual(current, one) {
		t.Fatal("hash-only replay failed to reapply a revisited inventory", requests, current)
	}
	f.mu.Lock()
	f.screens = []domain.Screen{}
	f.mu.Unlock()
	if err := f.manager.syncScreens(ctx, one, true); err != nil {
		t.Fatal(err)
	}
	_, requests, current = f.snapshot()
	if len(requests) != 4 || !reflect.DeepEqual(current, one) {
		t.Fatal("startup did not reconcile a lost server-side capability observation", requests, current)
	}
}

func TestPendingPublicationIsReconciledBeforeDifferentObservation(t *testing.T) {
	f := newManagerFixture(t)
	f.lostAck = true
	one := []domain.Screen{{ID: "right", Name: "Right screen", Scope: "screen", Width: 1280, Height: 720, Applications: []string{}}}
	if err := f.manager.syncScreens(context.Background(), one, false); err == nil {
		t.Fatal("lost receipt did not remain pending")
	}
	if err := f.manager.syncScreens(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	_, requests, current := f.snapshot()
	if len(requests) != 3 || requests[0].OperationID != requests[1].OperationID || requests[1].OperationID == requests[2].OperationID || len(current) != 0 {
		t.Fatal("new observation bypassed immutable uncertain publication", requests, current)
	}
}

func managerRFB(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			io.WriteString(conn, "RFB 003.008\n")
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().String()
}

func TestManagerReloadsActualProfilesFromLoadedSourceWithoutRestart(t *testing.T) {
	f := newManagerFixture(t)
	root := filepath.Dir(f.manager.Config.StateDir)
	home, bin := filepath.Join(root, "home"), filepath.Join(root, "bin")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "xrandr"), []byte("#!/bin/sh\nprintf 'Screen 0: minimum 8 x 8, current 1280 x 720, maximum 32768 x 32768\\nEXISTING connected primary 1280x720+0+0 (normal)\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(f.manager.Config.StateDir, "agent.json")
	if err := config.Write(path, f.manager.Config); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.Config = loaded
	if err := f.manager.PublishScreens(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := loaded
	changed.Screens = []config.Screen{{ID: "actual", Name: "Existing screen", Scope: "screen", Width: 1280, Height: 720,
		Display: ":47.0", VNCAddress: managerRFB(t), VNCVerified: true}}
	if err := config.Write(path, changed); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.PublishScreens(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, requests, screens := f.snapshot()
	if len(requests) != 2 || len(screens) != 1 || screens[0].ID != "actual" || f.manager.Config.Mode != "manual" || len(f.manager.Config.Screens) != 0 {
		t.Fatal("manager used/mutated stale startup configuration", screens)
	}
	changed.Screens[0].VNCVerified = false
	if err := config.Write(path, changed); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.PublishScreens(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, requests, screens = f.snapshot()
	if len(requests) != 3 || len(screens) != 0 {
		t.Fatal("disabled current profile retained a published capability", screens)
	}
	f.mu.Lock()
	f.session.OpenRequested = false
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.manager.Run(ctx) }()
	defer func() { cancel(); <-done }()
	// Let the supervisor observe the disabled profile before changing its source.
	time.Sleep(1100 * time.Millisecond)
	changed.Screens[0].VNCVerified = true
	if err := config.Write(path, changed); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, requests, screens = f.snapshot()
		if len(requests) == 4 && len(screens) == 1 && screens[0].ID == "actual" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("running manual supervisor did not publish changed current screen configuration")
		}
		time.Sleep(10 * time.Millisecond)
	}
	changed.CredentialFile = filepath.Join(root, "different-identity")
	if err := config.Write(path, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.currentConfig(); err == nil {
		t.Fatal("live reload borrowed a different identity configuration")
	}
}

type openerFunc func(context.Context, domain.Session, config.Worker) (io.ReadWriteCloser, error)

func (f openerFunc) Open(ctx context.Context, session domain.Session, c config.Worker) (io.ReadWriteCloser, error) {
	return f(ctx, session, c)
}

func TestNoProcessVNCStartupFailureRetiresOnlyOwnedFence(t *testing.T) {
	f := newManagerFixture(t)
	session, _, _ := f.snapshot()
	err := f.manager.start(context.Background(), session)
	if err == nil {
		t.Fatal("headless VNC failure was hidden")
	}
	var fence Fence
	if _, err := f.manager.State.Get(context.Background(), "session-fence", &fence); err != nil || fence.Active {
		t.Fatal("proven no-process failure retained unusable fence", fence, err)
	}
	current, _, _ := f.snapshot()
	if current.State != "closed" || f.opened.Load() != 1 {
		t.Fatal("failed native startup did not retire the active remote session", current)
	}
}

func TestUnknownStartupAndCleanupFailuresRemainFenced(t *testing.T) {
	for _, item := range []string{"open-error", "nil-endpoint", "close-error", "mixed-proof"} {
		t.Run(item, func(t *testing.T) {
			f := newManagerFixture(t)
			f.manager.Opener = openerFunc(func(ctx context.Context, session domain.Session, c config.Worker) (io.ReadWriteCloser, error) {
				switch item {
				case "mixed-proof":
					_, safeErr := (Native{}).Open(ctx, session, c)
					return nil, errors.Join(safeErr, &process.CleanupError{Err: errors.New("another owned helper has uncertain cleanup")})
				case "open-error":
					return nil, errors.New("no process cleanup proof")
				case "close-error":
					left, right := net.Pipe()
					right.Close()
					return &uncertainEndpoint{ReadWriteCloser: left}, nil
				default:
					return nil, nil
				}
			})
			session, _, _ := f.snapshot()
			if err := f.manager.start(context.Background(), session); err == nil {
				t.Fatal("uncertain endpoint reported success")
			}
			var fence Fence
			if _, err := f.manager.State.Get(context.Background(), "session-fence", &fence); err != nil || !fence.Active || fence.CleanupProven || fence.ID != session.ID {
				t.Fatal("uncertain native process lost its fence", fence, err)
			}
			if err := f.manager.retryCleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
			var after Fence
			f.manager.State.Get(context.Background(), "session-fence", &after)
			current, _, _ := f.snapshot()
			if after != fence || current.State != "active" {
				t.Fatal("retry auto-cleared uncertain cleanup evidence", after, current)
			}
		})
	}
}

type uncertainEndpoint struct{ io.ReadWriteCloser }

func (e *uncertainEndpoint) Close() error {
	return errors.Join(e.ReadWriteCloser.Close(), errors.New("owned helper cleanup uncertain"))
}

func TestProvenCleanupRetirementLostAckIsRetriedWithoutOpening(t *testing.T) {
	f := newManagerFixture(t)
	f.closeFailure = true
	session, _, _ := f.snapshot()
	if err := f.manager.start(context.Background(), session); err == nil {
		t.Fatal("native failure/lost close acknowledgement hidden")
	}
	var fence Fence
	if _, err := f.manager.State.Get(context.Background(), "session-fence", &fence); err != nil || !fence.Active || !fence.CleanupProven {
		t.Fatal("safe cleanup proof was not retained for retirement retry", fence, err)
	}
	if err := f.manager.retryCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.manager.State.Get(context.Background(), "session-fence", &fence)
	f.mu.Lock()
	closeCount := f.closeCount
	f.mu.Unlock()
	if fence.Active || f.opened.Load() != 1 || closeCount != 1 {
		t.Fatal("retirement replay reopened endpoint or retained a resolved fence", fence)
	}
}

type readStartedEndpoint struct {
	io.ReadWriteCloser
	started chan struct{}
	once    sync.Once
}

func (e *readStartedEndpoint) Read(p []byte) (int, error) {
	e.once.Do(func() { close(e.started) })
	return e.ReadWriteCloser.Read(p)
}

func TestRelevantConfigurationChangeStopsVNCButNotHeadlessTmux(t *testing.T) {
	for _, mode := range []string{"vnc", "tmux"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagerFixture(t)
			path := filepath.Join(f.manager.Config.StateDir, "agent.json")
			if err := config.Write(path, f.manager.Config); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.LoadWorker(path)
			if err != nil {
				t.Fatal(err)
			}
			f.manager.Config = loaded
			f.mu.Lock()
			f.session.Mode = mode
			if mode == "tmux" {
				f.session.ScreenID = ""
			}
			f.mu.Unlock()
			left, right := net.Pipe()
			defer right.Close()
			defer left.Close()
			started := make(chan struct{})
			f.manager.Opener = openerFunc(func(context.Context, domain.Session, config.Worker) (io.ReadWriteCloser, error) {
				return &readStartedEndpoint{ReadWriteCloser: left, started: started}, nil
			})
			session, _, _ := f.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var runErr error
			go func() { runErr = f.manager.start(ctx, session); close(done) }()
			defer func() { cancel(); <-done }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("fixture native relay did not become active")
			}
			changed := loaded
			changed.Screens = []config.Screen{{ID: "new", Name: "New profile", Scope: "screen", Display: ":47.0", Width: 1280, Height: 720}}
			if err := config.Write(path, changed); err != nil {
				t.Fatal(err)
			}
			if mode == "tmux" {
				select {
				case <-done:
					t.Fatal("unrelated screen configuration stopped headless tmux")
				case <-time.After(1200 * time.Millisecond):
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("changed screen configuration did not stop active VNC")
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			var fence Fence
			if _, err := f.manager.State.Get(context.Background(), "session-fence", &fence); err != nil || fence.Active {
				t.Fatal("configuration-stop cleanup retained a proved-clean fence", fence, err)
			}
			current, _, _ := f.snapshot()
			if current.State != "closed" || f.manager.Config.Mode != "manual" {
				t.Fatal("configuration stop did not retire the session or changed global mode", current)
			}
		})
	}
}

func TestExistingFenceAndInvocationJournalsAreNeverAutomaticallyCleared(t *testing.T) {
	for _, journal := range []bool{false, true} {
		t.Run(strconv.FormatBool(journal), func(t *testing.T) {
			f := newManagerFixture(t)
			ctx := context.Background()
			old := Fence{Active: true, ID: "uncertain_old_session"}
			if journal {
				if err := f.manager.State.SaveJournal(ctx, local.Journal{ID: "uncertain_invocation", State: "running", PID: 123, CreatedAt: 1}); err != nil {
					t.Fatal(err)
				}
			} else if err := f.manager.State.Put(ctx, "session-fence", old); err != nil {
				t.Fatal(err)
			}
			session, _, _ := f.snapshot()
			if err := f.manager.start(ctx, session); err == nil || !strings.Contains(err.Error(), "reconcile") && !strings.Contains(err.Error(), "fenced") {
				t.Fatal("uncertain pre-existing state did not block admission", err)
			}
			if f.opened.Load() != 0 {
				t.Fatal("pre-existing fence started a remote endpoint")
			}
			if journal {
				journals, err := f.manager.State.Journals(ctx, true)
				if err != nil || len(journals) != 1 || journals[0].PID != 123 || journals[0].State != "running" {
					t.Fatal("unrelated invocation evidence changed", journals, err)
				}
			} else {
				var after Fence
				f.manager.State.Get(ctx, "session-fence", &after)
				if after != old {
					t.Fatal("unrelated session fence changed", after)
				}
			}
		})
	}
}
