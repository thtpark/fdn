//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

type sharingBackendFixture struct {
	mu        sync.Mutex
	peers     []net.Conn
	input     chan string
	opens     atomic.Int64
	inputEnds atomic.Int64
}

func (backend *sharingBackendFixture) Inventory(context.Context, config.Worker, *local.State) ([]domain.Terminal, error) {
	return []domain.Terminal{
		{ID: "shell", Name: "Help terminal", Generation: "native_fixture", Kind: "shell", CanInput: true},
		{ID: "other", Name: "Other terminal", Generation: "native_other", Kind: "shell", CanInput: true},
	}, nil
}
func (backend *sharingBackendFixture) Open(_ context.Context, _ config.Worker, _ *local.State, session domain.Session, _ domain.Terminal) (io.ReadWriteCloser, error) {
	view, producer := net.Pipe()
	backend.opens.Add(1)
	backend.mu.Lock()
	backend.peers = append(backend.peers, producer)
	backend.mu.Unlock()
	go func() {
		defer producer.Close()
		if _, err := io.WriteString(producer, "FOUNDATION_VIEW:"+session.TerminalID+"\n"); err != nil {
			return
		}
		buffer := make([]byte, 4096)
		for {
			n, err := producer.Read(buffer)
			if n > 0 {
				backend.input <- string(buffer[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return view, nil
}
func (backend *sharingBackendFixture) EndInput(context.Context, config.Worker, *local.State, domain.Session) error {
	backend.inputEnds.Add(1)
	return nil
}
func (backend *sharingBackendFixture) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	var result error
	for _, peer := range backend.peers {
		result = errors.Join(result, peer.Close())
	}
	return result
}

func startSharingManager(t *testing.T, h *harness, engine *work.Engine) (*sharingBackendFixture, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	backend := &sharingBackendFixture{input: make(chan string, 16)}
	var logs bytes.Buffer
	manager := &remote.Manager{Client: h.worker, State: engine.State, Config: engine.Config, Sharing: backend,
		Log: slog.New(slog.NewTextHandler(&logs, nil))}
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
			if t.Failed() {
				t.Log(logs.String())
			}
		case <-time.After(20 * time.Second):
			t.Error("sharing manager did not join owned viewers")
		}
	})
	awaitSharing(t, func() bool {
		record, err := api.Call[domain.Sharing](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "sharing"), nil)
		return err == nil && record.TerminalID == "shell"
	})
	return backend, ctx
}

func awaitSharing(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected current sharing state was not observed")
}

func openSharing(t *testing.T, h *harness, client *api.Client, mode, grant string) domain.Session {
	t.Helper()
	ctx := context.Background()
	request, err := api.PrepareSharing(ctx, client, h.workerAgent.ID, mode, grant, 120, "Explicit integration viewer through Foundation")
	if err != nil {
		t.Fatal(err)
	}
	request.Command = command()
	session, err := api.Call[domain.Session](ctx, client, "POST", "/v1/sessions", request)
	if err != nil {
		t.Fatal(err)
	}
	session, err = api.Call[domain.Session](ctx, client, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func readSharingMarker(t *testing.T, connection net.Conn, terminal string) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	want := "FOUNDATION_VIEW:" + terminal + "\n"
	buffer := make([]byte, len(want))
	if _, err := io.ReadFull(connection, buffer); err != nil || string(buffer) != want {
		t.Fatal("Foundation did not relay the selected terminal", string(buffer), err)
	}
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestSharingViewsContinueDuringExecutionAndInputUsesTheSameLogicalSession(t *testing.T) {
	h := newHarness(t)
	x := &executor{}
	engine := newEngine(t, h, x)
	task := readyForEngine(t, h, engine)
	skipHealthOnboarding(t, engine)
	if _, err := engine.Step(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	x.block, x.entered = make(chan struct{}), make(chan struct{})
	executed := make(chan error, 1)
	go func() { _, err := engine.Step(context.Background(), false); executed <- err }()
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("execution did not start")
	}
	released := false
	defer func() {
		if !released {
			close(x.block)
		}
	}()
	backend, ctx := startSharingManager(t, h, engine)
	session := openSharing(t, h, h.founder, "tmux", "")
	view, err := remote.Dial(ctx, h.founder, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	readSharingMarker(t, view, "shell")
	if _, err := view.Write([]byte("VIEW_ONLY_MUST_NOT_TYPE")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-backend.input:
		t.Fatal("passive viewer sent input", input)
	case <-time.After(150 * time.Millisecond):
	}
	current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || current.ActiveRun == nil || current.Status != domain.Running {
		t.Fatal("viewing interrupted execution", current, err)
	}
	select {
	case err := <-executed:
		t.Fatal("viewing stopped the provider", err)
	default:
	}
	currentSession, err := api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: currentSession.Revision}, Action: "request-input"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	currentSession, err = api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
	if err != nil || currentSession.InputGranted {
		t.Fatal("input bypassed an ongoing execution", currentSession, err)
	}
	close(x.block)
	released = true
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	awaitSharing(t, func() bool {
		currentSession, err = api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
		return err == nil && currentSession.InputGranted
	})
	lock, err := secure.Acquire(filepath.Join(engine.Config.StateDir, "work.lock"))
	if err == nil {
		lock.Close()
		t.Fatal("input did not hold the existing execution slot")
	}
	if _, err := view.Write([]byte("EXPLICITLY_GRANTED")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-backend.input:
		if input != "EXPLICITLY_GRANTED" {
			t.Fatal("wrong input", input)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("target-acknowledged input was not relayed")
	}
	_, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: currentSession.Revision}, Action: "revoke-input"})
	if err != nil {
		t.Fatal(err)
	}
	view.Close()
	awaitSharing(t, func() bool {
		currentSession, err = api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
		var fence remote.Fence
		_, readErr := engine.State.Get(ctx, "session-fence", &fence)
		return err == nil && readErr == nil && currentSession.State == "active" && !currentSession.InputGranted && currentSession.InputLeaseID == "" && !fence.Active
	})
	if currentSession.ID != session.ID || currentSession.ExpiresAt != session.ExpiresAt {
		t.Fatal("input cleanup replaced or renewed the viewer")
	}
	second, err := remote.Dial(ctx, h.founder, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	readSharingMarker(t, second, "shell")
	if backend.inputEnds.Load() != 1 {
		t.Fatal("input cleanup was skipped or repeated", backend.inputEnds.Load())
	}
	if _, err := second.Write([]byte("AFTER_RELEASE_MUST_NOT_TYPE")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-backend.input:
		t.Fatal("input survived handoff release", input)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSharingSelectionSwitchAndDelegateRevocationUseFoundationRelay(t *testing.T) {
	h := newHarness(t)
	engine := newEngine(t, h, &executor{})
	backend, ctx := startSharingManager(t, h, engine)
	grant, err := api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", "/v1/assistance", domain.AssistanceGrantRequest{
		Command: command(), RequesterID: h.otherAgent.ID, TargetID: h.workerAgent.ID, Mode: "both", Purpose: "sharing", DurationSeconds: 120, Reason: "One fixed selected-resource approval"})
	if err != nil {
		t.Fatal(err)
	}
	session := openSharing(t, h, h.other, "tmux", grant.ID)
	view, err := remote.Dial(ctx, h.other, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	readSharingMarker(t, view, "shell")
	selection, err := api.Call[domain.Sharing](ctx, h.founder, "GET", api.Path("agents", h.workerAgent.ID, "sharing"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.Call[domain.Sharing](ctx, h.other, "POST", api.Path("agents", h.workerAgent.ID, "sharing", "select"), domain.SharingSelectRequest{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: selection.Revision}, Mode: "tmux", ResourceID: "other", Reason: "Delegate cannot select"})
	if err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("delegate selected an unapproved terminal", err)
	}
	_, err = api.Call[domain.Sharing](ctx, h.founder, "POST", api.Path("agents", h.workerAgent.ID, "sharing", "select"), domain.SharingSelectRequest{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: selection.Revision}, Mode: "tmux", ResourceID: "other", Reason: "Founder asks the target to show another terminal"})
	if err != nil {
		t.Fatal(err)
	}
	view.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := view.Read(make([]byte, 1)); err == nil {
		t.Fatal("old selected terminal stream stayed active")
	}
	view.Close()
	reconnected, err := remote.Dial(ctx, h.other, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	readSharingMarker(t, reconnected, "other")
	if backend.opens.Load() < 2 {
		t.Fatal("switch did not prepare the newly selected endpoint")
	}
	_, err = api.Call[domain.AssistanceGrant](ctx, h.founder, "POST", api.Path("assistance", grant.ID, "revoke"), domain.AssistanceRevoke{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: grant.Revision}, Reason: "Founder revokes the approval"})
	if err != nil {
		t.Fatal(err)
	}
	reconnected.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reconnected.Read(make([]byte, 1)); err == nil {
		t.Fatal("revocation left the delegate stream open")
	}
	_, err = remote.Dial(ctx, h.other, session.ID, "requester")
	if err == nil {
		t.Fatal("revoked approval reopened")
	}
}
