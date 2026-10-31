package work

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type pushExecutor struct {
	checks, invocations atomic.Int64
	entered             chan struct{}
	release             chan struct{}
}

func (x *pushExecutor) Check(context.Context, string) (provider.Health, error) {
	x.checks.Add(1)
	return provider.Health{Provider: "codex", Version: "1.2.3", Path: "/synthetic/provider", Authentication: "authenticated"}, nil
}

func (x *pushExecutor) Invoke(ctx context.Context, _ provider.Invocation) (provider.Reply, error) {
	x.invocations.Add(1)
	if x.entered != nil {
		signalUpdate(x.entered)
	}
	if x.release != nil {
		select {
		case <-ctx.Done():
			return provider.Reply{}, ctx.Err()
		case <-x.release:
		}
	}
	return provider.Reply{Outcome: "responded", Summary: "Deterministic private response"}, nil
}

// This is an explicit, in-process HTTP business adapter, not a native transport
// or a capacity/latency qualification of a deployed Foundation.
type pushFixture struct {
	t           *testing.T
	mu          sync.Mutex
	engine      *Engine
	executor    *pushExecutor
	me          domain.Agent
	project     domain.Project
	task        domain.Task
	messages    map[string]domain.Message
	proposals   map[string][]byte
	results     map[string]domain.Message
	attempts    map[string]int
	counts      map[string]int
	updates     []domain.Update
	changed     chan struct{}
	waiting     chan struct{}
	firstMe     func()
	lostReadAck bool
	readEffects int
	readBodies  [][]byte
	heartbeat   chan *domain.WorkerHealth
	disconnect  bool
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	state, err := local.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	f := &pushFixture{
		t: t, executor: &pushExecutor{}, me: domain.Agent{ID: "agent_push", Active: true, Revision: 1, Mode: "ai"},
		project:  domain.Project{ID: "project_push", Epoch: 1, OwnerID: "agent_authority", Delegates: []string{"agent_push"}},
		messages: map[string]domain.Message{}, proposals: map[string][]byte{}, results: map[string]domain.Message{},
		attempts: map[string]int{}, counts: map[string]int{}, changed: make(chan struct{}), waiting: make(chan struct{}, 1),
		heartbeat: make(chan *domain.WorkerHealth, 32),
	}
	f.task = domain.Task{ID: "task_push", ProjectID: f.project.ID, AssigneeID: f.me.ID, Status: domain.Todo,
		SpecGeneration: 1, DiscussionGeneration: 2, ResultGeneration: 3, Readiness: &domain.Approval{ProjectEpoch: 1, SpecGeneration: 1},
		ActiveRun: &domain.ActiveRun{ID: "run_push", AgentID: f.me.ID, SpecGeneration: 1}}
	c := config.DefaultWorker(state.Root)
	c.PollSeconds, c.AutomaticProviderUpdates = 1, false
	client := &api.Client{BaseURL: "https://explicit-push-fixture.invalid", Credential: config.Credential{AgentID: f.me.ID},
		HTTP: &http.Client{Transport: f}}
	f.engine = New(c, "", client, state, f.executor)
	f.engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	inventory, err := state.RefreshInventory(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Put(context.Background(), "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.engine.State.Close() })
	return f
}

func (f *pushFixture) response(value any, fault *domain.Fault) (*http.Response, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(api.Envelope{OK: fault == nil, Data: data, Error: fault})
	if err != nil {
		return nil, err
	}
	status := http.StatusOK
	if fault != nil {
		status = domain.HTTPStatus(fault)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"X-Fdn-Api-Version": {strconv.Itoa(domain.APIVersion)}},
		Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *pushFixture) publish(update domain.Update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update.Sequence = int64(len(f.updates) + 1)
	f.updates = append(f.updates, update)
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *pushFixture) updatePage(request *http.Request) (*http.Response, error) {
	for {
		f.mu.Lock()
		if f.disconnect {
			f.mu.Unlock()
			return nil, errors.New("synthetic scoped-feed disconnect")
		}
		cursor := request.URL.Query().Get("since")
		sequence := len(f.updates)
		if cursor != "" {
			var err error
			sequence, err = strconv.Atoi(strings.TrimPrefix(cursor, "cursor_"))
			if err != nil || sequence > len(f.updates) {
				f.mu.Unlock()
				return f.response(nil, domain.Fail(domain.Invalid, "invalid fixture cursor"))
			}
		}
		page := domain.Page[domain.Update]{Items: append([]domain.Update{}, f.updates[sequence:]...), NextCursor: "cursor_" + strconv.Itoa(len(f.updates))}
		changed := f.changed
		if cursor == "" || len(page.Items) != 0 || request.URL.Query().Get("wait") != "true" {
			f.mu.Unlock()
			return f.response(page, nil)
		}
		f.mu.Unlock()
		signalUpdate(f.waiting)
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-changed:
		}
	}
}

func (f *pushFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	path := request.URL.Path
	f.counts[request.Method+" "+path]++
	hook := f.firstMe
	if path == "/v1/me" {
		f.firstMe = nil
	} else {
		hook = nil
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if path == "/v1/updates" {
		return f.updatePage(request)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Method == http.MethodGet {
		switch path {
		case "/v1/me":
			return f.response(f.me, nil)
		case "/v1/agents/agent_founder":
			return f.response(domain.Agent{ID: "agent_founder", Active: true, Founder: true}, nil)
		case "/v1/ownership":
			return f.response([]domain.Task{}, nil)
		case "/v1/note-context":
			notes := []domain.Note{{ID: "note_company", Scope: domain.CompanyNotes}}
			if project := request.URL.Query().Get("project"); project != "" {
				notes = append(notes, domain.Note{ID: "note_" + project, Scope: domain.ProjectNotes, TargetID: project})
			}
			notes = append(notes, domain.Note{ID: "note_" + f.me.ID, Scope: domain.AgentNotes, TargetID: f.me.ID})
			return f.response(notes, nil)
		case "/v1/sessions":
			return f.response([]domain.Session{}, nil)
		case "/v1/projects":
			return f.response([]domain.Project{}, nil)
		case "/v1/tasks":
			return f.response(domain.Page[domain.Task]{Items: []domain.Task{}}, nil)
		case "/v1/agent-operations":
			return f.response(domain.Page[domain.AgentOperation]{Items: []domain.AgentOperation{}}, nil)
		case "/v1/attention":
			return f.response(domain.Page[domain.Attention]{Items: []domain.Attention{}}, nil)
		case "/v1/messages":
			items := []domain.Message{}
			for _, message := range f.messages {
				if message.RecipientID == f.me.ID && message.State == "pending" && (request.URL.Query().Get("urgent") != "true" || message.Urgent) {
					items = append(items, message)
				}
			}
			return f.response(domain.Page[domain.Message]{Items: items}, nil)
		case api.Path("projects", f.project.ID):
			return f.response(f.project, nil)
		case api.Path("tasks", f.task.ID):
			return f.response(f.task, nil)
		case api.Path("tasks", f.task.ID, "entries"):
			return f.response(domain.Page[domain.Entry]{Items: []domain.Entry{}}, nil)
		}
		if strings.HasSuffix(path, "/thread-page") {
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/messages/"), "/thread-page")
			if source, found := f.messages[id]; found {
				return f.response(domain.Page[domain.Message]{Items: []domain.Message{source}}, nil)
			}
		}
		if message, found := f.messages[strings.TrimPrefix(path, "/v1/messages/")]; found {
			return f.response(message, nil)
		}
	}
	if request.Method == http.MethodPost && path == "/v1/heartbeat" {
		var body domain.HeartbeatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		select {
		case f.heartbeat <- body.Health:
		default:
		}
		return f.response(f.me, nil)
	}
	if request.Method == http.MethodPost && path == "/v1/agents/"+f.me.ID+"/environment" {
		var body domain.EnvironmentRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if err := domain.ValidateEnvironment(body.Observation); err != nil {
			return nil, err
		}
		return f.response(domain.ComputerEnvironment{BindingID: f.me.ID, DeviceName: f.me.Name,
			Generation: 1, ReceivedAt: time.Now().Unix(), Observation: body.Observation}, nil)
	}
	if request.Method == http.MethodPost && strings.HasPrefix(path, "/v1/messages") {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var command domain.Command
		if err := json.Unmarshal(body, &command); err != nil {
			return nil, err
		}
		f.attempts[command.OperationID]++
		if previous, found := f.proposals[command.OperationID]; found && !bytes.Equal(previous, body) {
			return f.response(nil, domain.Fail(domain.Conflict, "exact fixture message proposal changed"))
		}
		f.proposals[command.OperationID] = bytes.Clone(body)
		message, replay := f.results[command.OperationID]
		if strings.HasSuffix(path, "/read") {
			f.readBodies = append(f.readBodies, bytes.Clone(body))
		}
		if !replay {
			if path == "/v1/messages" {
				var input domain.MessageRequest
				if err := json.Unmarshal(body, &input); err != nil {
					return nil, err
				}
				message = domain.Message{ID: "message_reply_" + strconv.Itoa(len(f.results)), SenderID: f.me.ID, RecipientID: input.RecipientID,
					Body: input.Body, Kind: input.Kind, State: "pending", Revision: 1}
			} else {
				parts := strings.Split(path, "/")
				message = f.messages[parts[3]]
				if strings.HasSuffix(path, "/read") {
					message.ReadAt = time.Now().Unix()
					f.readEffects++
				} else {
					message.State, message.Revision = "handled", message.Revision+1
				}
			}
			f.results[command.OperationID] = message
			f.messages[message.ID] = message
		}
		if strings.HasSuffix(path, "/read") && f.lostReadAck {
			f.lostReadAck = false
			return nil, errors.New("synthetic lost explicit read acknowledgement")
		}
		return f.response(message, nil)
	}
	f.t.Errorf("unexpected push-fixture action: %s %s", request.Method, path)
	return f.response(nil, domain.Fail(domain.Invalid, "unexpected fixture selector"))
}

func eventuallyPush(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("push fixture condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startPushService(t *testing.T, e *Engine, sessions SessionRunner) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Serve(ctx, sessions) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error("push service stopped with error:", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("push service did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestUpdateRoutingSeparatesControlAndSessionDiscovery(t *testing.T) {
	for _, item := range []struct {
		topic, source string
		want          updateTargets
	}{
		{"agent-operation", "operation_one", updateOperations},
		{"agent-mode", "agent_push", updateWork | updateMode | updateSessions},
		{"agent-mode", "agent_other", updateWork | updateSessions},
		{"message", "message_one", updateWork | updateAuthentication},
		{"session", "session_one", updateWork | updateSessions},
		{"assistance", "grant_one", updateWork | updateSessions},
		{"project", "project_one", updateWork | updateSessions},
		{"task", "task_one", updateWork | updateSessions},
		{"run", "run_one", updateWork | updateSessions},
		{"attention", "entry_one", updateWork},
		{"comment", "entry_one", updateWork},
		{"worklog", "entry_one", updateWork},
		{"note", "note_company", updateWork},
		{"future-invalidation", "future_one", initialUpdateTargets},
	} {
		t.Run(item.topic+item.source, func(t *testing.T) {
			if got := updateRoutes(domain.Update{Topic: item.topic, SourceID: item.source}, "agent_push"); got != item.want {
				t.Fatal("wrong independent update routing", got, item.want)
			}
		})
	}
	cursor := "opaque+/=&?checkpoint"
	path, err := url.Parse(updatePath(cursor, true))
	if err != nil || path.Query().Get("since") != cursor || path.Query().Get("wait") != "true" {
		t.Fatal("opaque cursor was not safely encoded", path, err)
	}
}

func TestUpdateCursorAndPendingGenerationAreDurableAndNeverClobberNewWake(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	wake := make(chan struct{}, 1)
	if err := f.engine.receiveUpdates(ctx, domain.Page[domain.Update]{NextCursor: "cursor_0"}, true, wake, nil); err != nil {
		t.Fatal(err)
	}
	observed, err := f.engine.updateCheckpoint(ctx, nil)
	if err != nil || !observed.Work.pending() || !observed.Operations.pending() {
		t.Fatal("initial snapshot was not durably pending", observed, err)
	}
	if err := f.engine.receiveUpdates(ctx, domain.Page[domain.Update]{Items: []domain.Update{{Sequence: 1, Topic: "task", SourceID: f.task.ID}}, NextCursor: "cursor_1"}, false, wake, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.finishUpdateWork(ctx, observed, Status{State: "idle"}, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	root, c, client := f.engine.State.Root, f.engine.Config, f.engine.Client
	if err := f.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = New(c, "", client, state, f.executor)
	retained, err := f.engine.updateCheckpoint(ctx, nil)
	if err != nil || retained.Cursor != "cursor_1" || !retained.Work.pending() || retained.Work.Handled != observed.Work.Requested || !retained.Operations.pending() {
		t.Fatal("restart lost an update received while the serial controller was busy", retained, err)
	}
}

func TestUnchangedUpdateCursorDoesNotWriteAndFailedCheckpointDoesNotWake(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	wake := make(chan struct{}, 1)
	page := domain.Page[domain.Update]{NextCursor: "cursor_0"}
	if err := f.engine.receiveUpdates(ctx, page, true, wake, nil); err != nil {
		t.Fatal(err)
	}
	<-wake
	db, err := sql.Open("sqlite", filepath.Join(f.engine.State.Root, "local.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	version := func() int {
		var value int
		if err := db.QueryRowContext(ctx, "PRAGMA data_version").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := version()
	for range 3 {
		if err := f.engine.receiveUpdates(ctx, page, false, wake, nil); err != nil {
			t.Fatal(err)
		}
	}
	if version() != before {
		t.Fatal("unchanged empty reception rewrote the cursor")
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_update_checkpoint BEFORE UPDATE ON kv
WHEN NEW.key = 'daemon-updates' BEGIN SELECT RAISE(FAIL, 'synthetic durable checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = f.engine.receiveUpdates(ctx, domain.Page[domain.Update]{Items: []domain.Update{{Sequence: 1, Topic: "task"}}, NextCursor: "cursor_1"}, false, wake, nil)
	if err == nil {
		t.Fatal("failed durable cursor write was accepted")
	}
	select {
	case <-wake:
		t.Fatal("controller was signaled before the update and pending wake were durable")
	default:
	}
	checkpoint, err := f.engine.updateCheckpoint(ctx, nil)
	if err != nil || checkpoint.Cursor != "cursor_0" {
		t.Fatal("failed persistence advanced the cursor", checkpoint, err)
	}
}

func TestPushServiceDoesNotDiscoverOrAdvanceBusinessProgressWhileIdle(t *testing.T) {
	f := newPushFixture(t)
	stop := startPushService(t, f.engine, nil)
	eventuallyPush(t, func() bool {
		var status Status
		found, err := f.engine.State.Get(context.Background(), "work-status", &status)
		checkpoint, checkpointErr := f.engine.updateCheckpoint(context.Background(), nil)
		return err == nil && found && status.State == "idle" && checkpointErr == nil &&
			!checkpoint.Work.pending() && !checkpoint.Mode.pending() && !checkpoint.Operations.pending() && !checkpoint.Authentication.pending()
	})
	select {
	case <-f.waiting:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter an indefinite scoped update wait")
	}
	var before workerHealthState
	if _, err := f.engine.State.Get(context.Background(), workerHealthKey, &before); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	counts := make(map[string]int, len(f.counts))
	for key, value := range f.counts {
		counts[key] = value
	}
	f.mu.Unlock()
	time.Sleep(5200 * time.Millisecond)
	f.mu.Lock()
	for key, value := range counts {
		if strings.HasPrefix(key, "GET ") && f.counts[key] != value {
			t.Errorf("idle business discovery repeated: %s %d -> %d", key, value, f.counts[key])
		}
	}
	f.mu.Unlock()
	var after workerHealthState
	if _, err := f.engine.State.Get(context.Background(), workerHealthKey, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || f.executor.invocations.Load() != 0 || f.executor.checks.Load() != 0 {
		t.Fatal("an unchanged idle identity ran inference/provider checks or advanced health progress", before, after)
	}
	// Critical publication is independent of the indefinitely blocked feed.
	if err := f.engine.phaseHealth(context.Background(), "blocked", nil, "", 0, false); err != nil {
		t.Fatal(err)
	}
	for len(f.heartbeat) != 0 {
		<-f.heartbeat
	}
	signalUpdate(f.engine.statusWake)
	select {
	case <-f.heartbeat:
	case <-time.After(time.Second):
		t.Fatal("critical health publication waited for a business update")
	}
	stop()
}

func TestPushClosesInitialSnapshotRaceAndDoesNotDuplicateMessageInference(t *testing.T) {
	f := newPushFixture(t)
	f.firstMe = func() {
		f.mu.Lock()
		f.messages["message_race"] = domain.Message{ID: "message_race", SenderID: "agent_founder", RecipientID: f.me.ID, Kind: "direct",
			State: "pending", Revision: 1, Urgent: true, ResponseRequest: true, Body: "Private requested response"}
		f.mu.Unlock()
		f.publish(domain.Update{Topic: "message", SourceID: "message_race", Urgent: true})
	}
	stop := startPushService(t, f.engine, nil)
	eventuallyPush(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.messages["message_race"].State == "handled"
	})
	eventuallyPush(t, func() bool {
		checkpoint, err := f.engine.updateCheckpoint(context.Background(), nil)
		return err == nil && checkpoint.Cursor == "cursor_1" && !checkpoint.Work.pending()
	})
	stop()
	if f.executor.invocations.Load() != 1 || f.readEffects != 0 {
		t.Fatal("initial snapshot race lost/duplicated inference or invented a read receipt", f.executor.invocations.Load(), f.readEffects)
	}
}

type pushRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pushRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPushControlWhilePausedFencedAndBusyRetriesOnlyRetainedResult(t *testing.T) {
	f := newOperationFixture(t, "diagnostics", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.engine.beginHealth(ctx, f.engine.Config); err != nil {
		t.Fatal(err)
	}
	launch := Launch{Phase: "message", Message: &domain.Message{ID: "message_busy"}}
	if err := f.engine.phaseHealth(ctx, "invoking", &launch, "invocation_busy", 300, true); err != nil {
		t.Fatal(err)
	}
	var before workerHealthState
	if _, err := f.engine.State.Get(ctx, workerHealthKey, &before); err != nil {
		t.Fatal(err)
	}
	slot, err := secure.Acquire(filepath.Join(f.engine.Config.StateDir, "work.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Close()
	f.lostAck = true
	var scans atomic.Int64
	f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/agent-operations" {
			scans.Add(1)
		}
		return f.RoundTrip(request)
	})
	done := make(chan struct{})
	go func() { defer close(done); f.engine.operationLoop(ctx, nil) }()
	defer func() { cancel(); <-done }()
	if err := f.engine.requestUpdates(ctx, updateOperations, nil); err != nil {
		t.Fatal(err)
	}
	eventuallyPush(t, func() bool {
		checkpoint, err := f.engine.updateCheckpoint(ctx, nil)
		return err == nil && !checkpoint.Operations.pending()
	})
	var receipt local.AgentOperationReceipt
	if _, err := f.engine.State.Get(ctx, local.AgentOperationResultKey(f.operation.ID), &receipt); err != nil || receipt.DeliveryState != "pending" {
		t.Fatal("lost acknowledgement was not durably pending", receipt, err)
	}
	time.Sleep(5200 * time.Millisecond)
	if _, err := f.engine.State.Get(ctx, local.AgentOperationResultKey(f.operation.ID), &receipt); err != nil || receipt.DeliveryState != "delivered" {
		t.Fatal("actual due timer did not replay the retained exact result", receipt, err)
	}
	if scans.Load() != 1 || f.executor.checks.Load() != 1 || f.executor.invocations.Load() != 0 {
		t.Fatal("delivery retry rediscovered controls/reprobed or invoked inference", scans.Load(), f.executor.checks.Load(), f.executor.invocations.Load())
	}
	var after workerHealthState
	if _, err := f.engine.State.Get(ctx, workerHealthKey, &after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("background control overwrote in-flight phase/progress", after, err)
	}
}

func TestPushRestartReplaysPreparedExplicitReadWithoutInference(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	message := domain.Message{ID: "message_replay", SenderID: "agent_founder", RecipientID: f.me.ID, Kind: "direct", State: "pending", Revision: 1, ResponseRequest: true}
	f.messages[message.ID] = message
	launch := Launch{Phase: "message", Message: &message}
	contextData, _ := json.Marshal(launch)
	replyData, _ := json.Marshal(provider.Reply{Outcome: "responded", Summary: "Exact retained response", MessageRead: true})
	journal := local.Journal{ID: "invocation_replay", Kind: "message", State: "reply-prepared", Context: contextData, Reply: replyData, CreatedAt: time.Now().Unix()}
	if err := f.engine.State.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.receiveUpdates(ctx, domain.Page[domain.Update]{NextCursor: "cursor_0"}, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	f.lostReadAck = true
	if _, err := f.engine.Step(ctx, false); err == nil {
		t.Fatal("lost explicit read acknowledgement was hidden")
	}
	root, c, client := f.engine.State.Root, f.engine.Config, f.engine.Client
	if err := f.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = New(c, "", client, state, f.executor)
	f.engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := startPushService(t, f.engine, nil)
	eventuallyPush(t, func() bool {
		replayed, err := state.Journal(ctx, journal.ID)
		return err == nil && replayed.State == "done"
	})
	stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.executor.invocations.Load() != 0 || f.readEffects != 1 || len(f.readBodies) != 2 || !bytes.Equal(f.readBodies[0], f.readBodies[1]) ||
		f.attempts[journal.ID+".reply"] != 1 || f.attempts[journal.ID+".receipt"] != 1 {
		t.Fatal("restart repeated inference, read effect, reply or disposition", f.executor.invocations.Load(), f.readEffects, f.attempts)
	}
}

type pushSessionRunner struct {
	started  chan struct{}
	wakes    atomic.Int64
	external atomic.Bool
}

func (s *pushSessionRunner) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

func (s *pushSessionRunner) Wake()               { s.wakes.Add(1) }
func (s *pushSessionRunner) UseExternalUpdates() { s.external.Store(true) }

func TestPushSessionRecoveryIsIndependentAndDiscoveryUsesWakeHook(t *testing.T) {
	f := newPushFixture(t)
	bootstrap := make(chan struct{})
	f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/updates" && request.URL.Query().Get("since") == "" {
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-bootstrap:
			}
		}
		return f.RoundTrip(request)
	})
	sessions := &pushSessionRunner{started: make(chan struct{})}
	stop := startPushService(t, f.engine, sessions)
	select {
	case <-sessions.started:
		if !sessions.external.Load() {
			t.Fatal("session supervisor started its own subscription before external-update ownership was selected")
		}
	case <-time.After(time.Second):
		t.Fatal("local session cleanup waited for a Foundation feed connection")
	}
	if f.executor.invocations.Load() != 0 || sessions.wakes.Load() != 0 {
		t.Fatal("business work started before its initial watermark")
	}
	close(bootstrap)
	eventuallyPush(t, func() bool { return sessions.wakes.Load() != 0 })
	f.publish(domain.Update{Topic: "assistance", SourceID: "grant_new", Urgent: true})
	eventuallyPush(t, func() bool { return sessions.wakes.Load() >= 2 })
	f.publish(domain.Update{Topic: "session", SourceID: "session_new", Urgent: true})
	eventuallyPush(t, func() bool { return sessions.wakes.Load() >= 3 })
	stop()
}

func TestPushRestartReplaysExactReportOutboxWithoutProviderInvocation(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	var bodies [][]byte
	effects := 0
	f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != api.Path("tasks", f.task.ID, "report") {
			return f.RoundTrip(request)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, bytes.Clone(body))
		if len(bodies) == 1 {
			effects++
			return nil, errors.New("synthetic lost fenced report acknowledgement")
		}
		if !bytes.Equal(bodies[0], body) {
			return f.response(nil, domain.Fail(domain.Conflict, "report operation changed its exact retained body"))
		}
		return f.response(domain.ReportOutcome{Task: domain.Task{ID: f.task.ID, Status: domain.Completed},
			Run: domain.Run{ID: "run_report", State: "completed"}, Accepted: true}, nil)
	})
	journal := local.Journal{ID: "invocation_report", TaskID: f.task.ID, RunID: "run_report", Kind: domain.ExecutionRun, State: "report-prepared",
		CreatedAt: time.Now().Unix(), Report: &domain.ReportRequest{Command: domain.Command{OperationID: "invocation_report.report"},
			RunID: "run_report", SpecGeneration: 1, Outcome: "completed", Summary: "Exact retained deterministic result"}}
	if err := f.engine.queueReport(ctx, &journal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Step(ctx, false); err == nil {
		t.Fatal("lost report acknowledgement was hidden")
	}
	root, c, client := f.engine.State.Root, f.engine.Config, f.engine.Client
	if err := f.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = New(c, "", client, state, f.executor)
	f.engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := startPushService(t, f.engine, nil)
	eventuallyPush(t, func() bool {
		replayed, err := state.Journal(ctx, journal.ID)
		return err == nil && replayed.State == "done"
	})
	stop()
	outbox, err := state.Outbox(ctx)
	if err != nil || len(outbox) != 0 {
		t.Fatal("acknowledged exact report remained in the outbox", outbox, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if effects != 1 || len(bodies) != 2 || f.executor.invocations.Load() != 0 {
		t.Fatal("restart repeated inference or failed exact outbox reconciliation", effects, len(bodies), f.executor.invocations.Load())
	}
}

func TestPushAuthenticationControlsDrainWhileBusyWithoutChangingHealth(t *testing.T) {
	f := newPushFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.engine.Config.Mode = "manual"
	if err := f.engine.beginHealth(ctx, f.engine.Config); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.phaseHealth(ctx, "invoking", &Launch{Phase: "message", Message: &domain.Message{ID: "message_busy"}}, "invocation_busy", 300, true); err != nil {
		t.Fatal(err)
	}
	var before workerHealthState
	if _, err := f.engine.State.Get(ctx, workerHealthKey, &before); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"message_refresh_one", "message_refresh_two"} {
		f.messages[id] = domain.Message{ID: id, Kind: "authentication-refresh", SenderID: "agent_founder", RecipientID: f.me.ID,
			State: "pending", Revision: 1, Urgent: true, ExpiresAt: time.Now().Add(time.Minute).Unix(),
			AuthenticationRefresh: &domain.AuthenticationRefresh{Provider: "codex"}}
	}
	if err := f.engine.State.Put(ctx, "session-fence", struct {
		Active bool `json:"active"`
	}{true}); err != nil {
		t.Fatal(err)
	}
	slot, err := secure.Acquire(filepath.Join(f.engine.State.Root, "work.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Close()
	done := make(chan struct{})
	go func() { defer close(done); f.engine.operationLoop(ctx, nil) }()
	defer func() { cancel(); <-done }()
	if err := f.engine.requestUpdates(ctx, updateAuthentication, nil); err != nil {
		t.Fatal(err)
	}
	eventuallyPush(t, func() bool {
		checkpoint, err := f.engine.updateCheckpoint(ctx, nil)
		return err == nil && !checkpoint.Authentication.pending()
	})
	var after workerHealthState
	if _, err := f.engine.State.Get(ctx, workerHealthKey, &after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("background sign-in control overwrote active work health", after, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messages["message_refresh_one"].State != "handled" || f.messages["message_refresh_two"].State != "handled" ||
		f.executor.checks.Load() != 2 || f.executor.invocations.Load() != 0 || f.readEffects != 0 || f.counts["GET /v1/agent-operations"] != 0 {
		t.Fatal("coalesced protected controls were stranded, inferred on, or incorrectly acknowledged", f.messages, f.counts)
	}
}

func TestHealthTransitionsPublishWhileUpdateWaitAndProviderRemainBlocked(t *testing.T) {
	f := newPushFixture(t)
	f.executor.entered = make(chan struct{}, 1)
	f.executor.release = make(chan struct{})
	message := domain.Message{ID: "message_health", SenderID: "agent_founder", RecipientID: f.me.ID, Kind: "direct",
		State: "pending", Revision: 1, ResponseRequest: true}
	f.messages[message.ID] = message
	stop := startPushService(t, f.engine, nil)
	select {
	case <-f.executor.entered:
	case <-time.After(time.Second):
		t.Fatal("synthetic provider did not enter its blocked invocation")
	}
	select {
	case <-f.waiting:
	case <-time.After(time.Second):
		t.Fatal("discovery did not block independently on scoped updates")
	}
	awaitHealth := func(phase string) *domain.WorkerHealth {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for {
			select {
			case observation := <-f.heartbeat:
				if observation != nil && observation.Phase == phase {
					return observation
				}
			case <-deadline.C:
				t.Fatalf("meaningful health transition %q waited for liveness heartbeat or discovery", phase)
			}
		}
	}
	busy := awaitHealth("invoking")
	if busy.ActionID != message.ID || busy.StepFinishedAt != 0 || busy.DeadlineAt <= busy.ObservedAt {
		t.Fatal("in-flight publication did not preserve current health clocks", busy)
	}
	close(f.executor.release)
	idle := awaitHealth("idle")
	if idle.LastActionID != message.ID || idle.StepFinishedAt == 0 || f.executor.invocations.Load() != 1 {
		t.Fatal("completion transition lost progress or repeated inference", idle)
	}
	f.mu.Lock()
	if f.counts["GET /v1/updates"] != 2 {
		t.Error("health publication unblocked or restarted discovery", f.counts)
	}
	f.mu.Unlock()
	stop()
}
