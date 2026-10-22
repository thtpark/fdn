package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

type permissionFixture struct {
	t             *testing.T
	engine        *Engine
	state         *local.State
	c             config.Worker
	now           int64
	agents        []domain.Agent
	project       domain.Project
	task          domain.Task
	inbox         []domain.Message
	messages      map[string]domain.Message
	bodies        map[string][]byte
	attempts      []domain.MessageRequest
	attemptBodies [][]byte
	lostAcks      map[string]int
	failures      map[string]*domain.Fault
	requests      int
	beforeRequest func(*http.Request)
}

func newPermissionFixture(t *testing.T) *permissionFixture {
	t.Helper()
	state, err := local.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	f := &permissionFixture{t: t, state: state, now: 1800000000,
		agents: []domain.Agent{
			{ID: "agent_founder", Name: "Founder", Active: true, Founder: true},
			{ID: "agent_delegate", Name: "Current delegate", Active: true},
			{ID: "agent_replacement", Name: "Replacement delegate", Active: true},
			{ID: "agent_target", Name: "Target fixture", Active: true},
		},
		project:  domain.Project{ID: "project_fixture", Delegates: []string{"agent_delegate"}, Epoch: 1},
		messages: map[string]domain.Message{}, bodies: map[string][]byte{}, lostAcks: map[string]int{}, failures: map[string]*domain.Fault{},
	}
	f.task = domain.Task{ID: "task_fixture", ProjectID: f.project.ID, AssigneeID: "agent_target", SpecGeneration: 2, Status: domain.Todo}
	f.c = config.DefaultWorker(state.Root)
	f.c.Mode = "manual"
	f.c.ProviderPaths["codex"] = filepath.Join(state.Root, "private-provider-config")
	client := &api.Client{BaseURL: "https://fixture.invalid", Credential: config.Credential{AgentID: "agent_target"}, HTTP: &http.Client{Transport: f}}
	f.engine = New(f.c, "", client, state, nil)
	f.engine.Now = func() time.Time { return time.Unix(f.now, 0) }
	t.Cleanup(func() {
		if err := f.state.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *permissionFixture) response(value any, fault *domain.Fault) (*http.Response, error) {
	f.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	envelope := api.Envelope{OK: fault == nil, Data: data, Error: fault}
	body, err := json.Marshal(envelope)
	if err != nil {
		f.t.Fatal(err)
	}
	status := http.StatusOK
	if fault != nil {
		status = domain.HTTPStatus(fault)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"X-Fdn-Api-Version": []string{strconv.Itoa(domain.APIVersion)}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *permissionFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	f.t.Helper()
	f.requests++
	if f.beforeRequest != nil {
		f.beforeRequest(request)
	}
	path := request.URL.Path
	if request.Method == "GET" {
		switch {
		case path == "/v1/agents":
			return f.response(f.agents, nil)
		case strings.HasPrefix(path, "/v1/agents/"):
			for _, agent := range f.agents {
				if path == api.Path("agents", agent.ID) {
					return f.response(agent, nil)
				}
			}
			return f.response(nil, domain.Fail(domain.NotFound, "requester missing"))
		case path == api.Path("projects", f.project.ID):
			return f.response(f.project, nil)
		case path == api.Path("tasks", f.task.ID):
			return f.response(f.task, nil)
		case path == "/v1/messages":
			return f.response(domain.Page[domain.Message]{Items: f.inbox}, nil)
		}
	}
	if request.Method != "POST" || path != "/v1/messages" {
		f.t.Fatalf("unexpected permission-help side effect: %s %s", request.Method, path)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	var input domain.MessageRequest
	if err := json.Unmarshal(body, &input); err != nil {
		f.t.Fatal(err)
	}
	f.attempts = append(f.attempts, input)
	f.attemptBodies = append(f.attemptBodies, bytes.Clone(body))
	if fault := f.failures[input.RecipientID]; fault != nil {
		return f.response(nil, fault)
	}
	message, found := f.messages[input.OperationID]
	if found {
		if !bytes.Equal(body, f.bodies[input.OperationID]) {
			return f.response(nil, domain.Fail(domain.Conflict, "immutable message proposal changed"))
		}
	} else {
		if input.ExpiresAt <= f.now || input.ExpiresAt > f.now+domain.AuthenticationRequestTTL {
			return f.response(nil, domain.Fail(domain.Invalid, "invalid fixture deadline"))
		}
		message = domain.Message{ID: domain.NewID("message"), SenderID: "agent_target", RecipientID: input.RecipientID, Body: input.Body,
			Kind: input.Kind, Origin: input.Origin, Urgent: input.Urgent, ResponseRequest: input.ResponseRequest, State: "pending", CreatedAt: f.now, ExpiresAt: input.ExpiresAt, Revision: 1}
		f.messages[input.OperationID], f.bodies[input.OperationID] = message, bytes.Clone(body)
	}
	if f.lostAcks[input.RecipientID] > 0 {
		f.lostAcks[input.RecipientID]--
		return nil, errors.New("synthetic lost acknowledgement with private diagnostic /private/provider/token")
	}
	return f.response(message, nil)
}

func (f *permissionFixture) reopen(t *testing.T) {
	t.Helper()
	root := f.state.Root
	if err := f.state.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.state, err = local.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = New(f.c, "", f.engine.Client, f.state, nil)
	f.engine.Now = func() time.Time { return time.Unix(f.now, 0) }
}

func (f *permissionFixture) stored(t *testing.T) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	if _, err := f.state.Get(context.Background(), permissionKey, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func permissionDelivery(t *testing.T, request PermissionRequest, recipient string) AuthenticationDelivery {
	t.Helper()
	for _, delivery := range request.Deliveries {
		if delivery.RecipientID == recipient {
			return delivery
		}
	}
	t.Fatalf("missing recipient %s in %+v", recipient, request.Deliveries)
	return AuthenticationDelivery{}
}

func TestPermissionRecipientsRequireActiveFounderAndCurrentDelegates(t *testing.T) {
	for _, unavailable := range []string{"missing", "inactive"} {
		t.Run(unavailable+" Founder", func(t *testing.T) {
			f := newPermissionFixture(t)
			founder := f.agents[0]
			if unavailable == "missing" {
				f.agents = f.agents[1:]
			} else {
				f.agents[0].Active = false
			}
			request, err := f.engine.requestPermission(context.Background(), f.c, &f.task, "task:fixture:2:0:0:", "workspace")
			if err == nil || domain.AsFault(err).Code != domain.Prerequisite || request.State != "delivery-pending" || request.Error == nil || request.ID == "" || len(f.attempts) != 0 {
				t.Fatal("missing Founder was replaced by a delegate or hidden", request, err)
			}
			f.reopen(t)
			f.agents = slices.DeleteFunc(f.agents, func(agent domain.Agent) bool { return agent.Founder })
			f.agents = append(f.agents, founder)
			f.now += 10
			replayed, err := f.engine.requestPermission(context.Background(), f.c, &f.task, request.ActionKey, "workspace")
			if err != nil || replayed.State != "requested" || replayed.Error != nil || replayed.ID != request.ID || replayed.ExpiresAt != request.ExpiresAt || len(f.messages) != 2 {
				t.Fatal("restored Founder did not receive the same durable episode with current delegate", replayed, err)
			}
			for _, recipient := range []string{"agent_founder", "agent_delegate"} {
				if delivery := permissionDelivery(t, replayed, recipient); delivery.State != "delivered" || delivery.MessageID == "" || !domain.ValidOperation(delivery.OperationID) {
					t.Fatal("missing exact delivered recipient", delivery)
				}
			}
		})
	}
	t.Run("computer local has no project", func(t *testing.T) {
		f := newPermissionFixture(t)
		request, err := f.engine.requestPermission(context.Background(), f.c, nil, "explicit:manual-operation", "provider-permission")
		if err != nil || request.ProjectID != "" || request.TaskID != "" || len(request.Deliveries) != 1 || request.Deliveries[0].RecipientID != "agent_founder" || len(f.messages) != 1 {
			t.Fatal("computer-local help leaked into an unrelated project", request, err)
		}
	})
	t.Run("inactive delegate skipped", func(t *testing.T) {
		f := newPermissionFixture(t)
		f.agents[1].Active = false
		request, err := f.engine.requestPermission(context.Background(), f.c, &f.task, "task:fixture", "workspace")
		if err != nil || len(request.Deliveries) != 1 || request.Deliveries[0].RecipientID != "agent_founder" || len(f.messages) != 1 {
			t.Fatal("inactive delegate received permission help", request, err)
		}
	})
}

func TestPermissionExactReplayAcrossLostAcknowledgementAndRestart(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	f.lostAcks["agent_delegate"] = 1
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "explicit:original-operation", "workspace")
	if err == nil || request.State != "delivery-pending" || request.Error == nil || len(f.messages) != 2 || len(f.attempts) != 2 {
		t.Fatal("lost acknowledgement was treated as delivered or prevented Founder notification", request, err)
	}
	firstBody := bytes.Clone(f.attemptBodies[0])
	firstOperation := f.attempts[0].OperationID
	if permissionDelivery(t, request, "agent_founder").State != "delivered" {
		t.Fatal("delegate delivery failure suppressed mandatory Founder delivery")
	}
	var prepared proposal
	if found, err := f.state.Get(ctx, "proposal:"+firstOperation, &prepared); err != nil || !found || len(prepared.Result) != 0 || !bytes.Equal(prepared.Body, firstBody) {
		t.Fatal("lost acknowledgement did not retain an immutable local proposal", prepared, err)
	}
	f.reopen(t)
	f.now += 40
	f.agents[3].Name = "Renamed target"
	f.task.Description = "private /private/workspace/credentials and synthetic secret"
	f.c.Mode = "ai"
	replayed, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, "workspace")
	if err != nil || replayed.ID != request.ID || replayed.ExpiresAt != request.ExpiresAt || replayed.AgentName != request.AgentName || replayed.State != "requested" || replayed.Error != nil || len(f.messages) != 2 || len(f.attempts) != 3 {
		t.Fatal("restart/lost acknowledgement created a replacement notification", replayed, err)
	}
	if f.attempts[2].OperationID != firstOperation || !bytes.Equal(firstBody, f.attemptBodies[2]) {
		t.Fatal("lost acknowledgement replay changed message bytes or operation identity")
	}
	for range 3 {
		if _, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, "workspace"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.attempts) != 3 || strings.Contains(string(f.stored(t)), "/private/") {
		t.Fatal("unchanged polling resent notices or retained private diagnostics")
	}
}

func TestPermissionCachedMessageReceiptRecoversPendingEpisode(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "explicit:cached-receipt", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	original := request.Deliveries[0]
	request.State = "delivery-pending"
	request.Deliveries[0].State, request.Deliveries[0].MessageID = "pending", ""
	if err := f.state.Put(ctx, permissionKey, []PermissionRequest{request}); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	attempts := len(f.attempts)
	current, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
	if err != nil || current.ID != request.ID || current.State != "requested" || len(f.attempts) != attempts || !reflect.DeepEqual(permissionDelivery(t, current, original.RecipientID), original) {
		t.Fatal("durable message receipt did not repair interrupted episode bookkeeping without a new send", current, err)
	}
}

func TestPermissionRecipientRevocationAndNewDelegationKeepEpisode(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	f.failures["agent_delegate"] = domain.Fail(domain.Denied, "synthetic denied /private/provider/config token=not-for-messages")
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "task:current:2", "task-authority")
	if err == nil || request.State != "delivery-pending" || len(f.messages) != 1 || permissionDelivery(t, request, "agent_founder").State != "delivered" {
		t.Fatal("explicit delivery failure was hidden or blocked Founder", request, err)
	}
	if strings.Contains(string(f.stored(t)), "/private/") || strings.Contains(request.Error.Message, "token=") || strings.Contains(err.Error(), "token=") {
		t.Fatal("private remote error escaped into permission-help state")
	}
	f.reopen(t)
	f.project.Delegates = []string{"agent_replacement"}
	f.now += 20
	before := len(f.attempts)
	replayed, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
	if err != nil || replayed.ID != request.ID || replayed.ExpiresAt != request.ExpiresAt || replayed.State != "requested" || len(f.messages) != 2 || len(f.attempts) != before+1 || f.attempts[before].RecipientID != "agent_replacement" {
		t.Fatal("changed current delegation renewed the episode or retried a revoked recipient", replayed, err)
	}
	if permissionDelivery(t, replayed, "agent_delegate").State != "no-longer-authorized" || permissionDelivery(t, replayed, "agent_replacement").State != "delivered" {
		t.Fatal("recipient authorization change was not projected", replayed)
	}
	delete(f.failures, "agent_delegate")
	f.project.Delegates = []string{"agent_replacement", "agent_delegate"}
	replayed, err = f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
	if err != nil || replayed.ID != request.ID || permissionDelivery(t, replayed, "agent_delegate").OperationID != permissionDelivery(t, request, "agent_delegate").OperationID || len(f.messages) != 3 {
		t.Fatal("reauthorized recipient lost its original exact operation", replayed, err)
	}
}

func TestPermissionExpiryIsReadOnlyAndOnlyFreshActionsRenotify(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "message:fixture:1", "workspace")
	if err != nil || request.ExpiresAt != request.CreatedAt+domain.AuthenticationRequestTTL {
		t.Fatal(request, err)
	}
	original := f.stored(t)
	f.now = request.ExpiresAt
	beforeRequests := f.requests
	f.reopen(t)
	for range 3 {
		requests, err := f.engine.permissionRequests(ctx)
		if err != nil || len(requests) != 1 || requests[0].State != "expired" || requests[0].ExpiresAt != request.ExpiresAt {
			t.Fatal("status renewed expiry", requests, err)
		}
		expired, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
		if err != nil || expired.ID != request.ID || expired.State != "expired" || expired.ExpiresAt != request.ExpiresAt {
			t.Fatal("same expired action silently renewed", expired, err)
		}
	}
	if !bytes.Equal(original, f.stored(t)) || f.requests != beforeRequests || len(f.messages) != 2 {
		t.Fatal("expired status/polling rewrote state or attempted delivery")
	}
	fresh, err := f.engine.requestPermission(ctx, f.c, &f.task, "message:fixture:2", "workspace")
	if err != nil || fresh.ID == request.ID || fresh.State != "requested" || fresh.ExpiresAt != f.now+900 || len(f.messages) != 4 {
		t.Fatal("new requested action did not produce exactly one fresh episode", fresh, err)
	}
	requests, err := f.engine.permissionRequests(ctx)
	if err != nil || len(requests) != 2 || requests[0].State != "expired" || requests[1].State != "requested" {
		t.Fatal("new action replaced unrelated expired evidence", requests, err)
	}
}

func TestPermissionSuccessfulObservationResolvesAndRearmsOnlyOnce(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	if err := f.engine.resolvePermission(ctx, "", "workspace"); err != nil {
		t.Fatal("healthy idle boundary without requested action failed", err)
	}
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "task:fixture:2", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.resolvePermission(ctx, request.ActionKey, "retry-approval"); err != nil {
		t.Fatal(err)
	}
	unchanged, err := f.engine.permissionRequests(ctx)
	if err != nil || unchanged[0].State != "requested" {
		t.Fatal("resolution crossed a different capability boundary", unchanged, err)
	}
	if err := f.engine.resolvePermission(ctx, request.ActionKey, request.Requirement); err != nil {
		t.Fatal(err)
	}
	resolved := f.stored(t)
	f.now += 901
	for range 2 {
		if err := f.engine.resolvePermission(ctx, request.ActionKey, request.Requirement); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := f.engine.permissionRequests(ctx)
	if err != nil || requests[0].State != "resolved" || !bytes.Equal(resolved, f.stored(t)) {
		t.Fatal("expiry or repeated success changed a resolved episode", requests, err)
	}
	f.reopen(t)
	reblocked, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
	if err != nil || reblocked.ID == request.ID || reblocked.State != "requested" || reblocked.CreatedAt != f.now || len(f.messages) != 4 {
		t.Fatal("a real successful observation did not allow one reblocking episode", reblocked, err)
	}
	for range 3 {
		current, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
		if err != nil || current.ID != reblocked.ID || len(f.messages) != 4 {
			t.Fatal("unchanged reblocking/polling emitted another notice", current, err)
		}
	}
}

func TestPermissionRequirementsHaveIndependentEpisodes(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	workspace, err := f.engine.requestPermission(ctx, f.c, &f.task, "task:independent-boundaries", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.engine.requestPermission(ctx, f.c, &f.task, workspace.ActionKey, "retry-approval")
	if err != nil || retry.ID == workspace.ID || len(f.messages) != 4 {
		t.Fatal("different requirements shared or suppressed an episode", retry, err)
	}
	if err := f.engine.resolvePermission(ctx, workspace.ActionKey, workspace.Requirement); err != nil {
		t.Fatal(err)
	}
	requests, err := f.engine.permissionRequests(ctx)
	if err != nil || len(requests) != 2 || requests[0].State != "resolved" || requests[1].State != "requested" {
		t.Fatal("boundary success resolved unrelated permission help", requests, err)
	}
}

func TestPermissionRechecksCurrentActorAndTaskBeforeDelivery(t *testing.T) {
	for _, item := range []struct {
		name   string
		change func(*permissionFixture)
	}{
		{"inactive requester", func(f *permissionFixture) { f.agents[3].Active = false }},
		{"banned requester", func(f *permissionFixture) { f.agents[3].Banned = true }},
		{"moved task", func(f *permissionFixture) { f.task.ProjectID = "project_other" }},
		{"reassigned task", func(f *permissionFixture) { f.task.AssigneeID = "agent_replacement" }},
		{"changed specification", func(f *permissionFixture) { f.task.SpecGeneration++ }},
		{"archived task", func(f *permissionFixture) { f.task.ArchivedAt = f.now }},
		{"terminal task", func(f *permissionFixture) { f.task.Status = domain.Done }},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := newPermissionFixture(t)
			snapshot := f.task
			item.change(f)
			request, err := f.engine.requestPermission(context.Background(), f.c, &snapshot, "task:stale-request", "task-authority")
			if err == nil || request.State != "delivery-pending" || request.Error == nil || len(f.attempts) != 0 {
				t.Fatal("stale scope or inactive actor sent permission help", request, err)
			}
		})
	}
	t.Run("authority blocker is not permission to act", func(t *testing.T) {
		f := newPermissionFixture(t)
		f.task.AssigneeID = "agent_replacement"
		request, err := f.engine.requestPermission(context.Background(), f.c, &f.task, "explicit:ask-for-assignment", "task-authority")
		if err != nil || request.State != "requested" || len(f.messages) != 2 || f.task.AssigneeID != "agent_replacement" {
			t.Fatal("notification required or manufactured the missing task authority", request, err)
		}
	})
	t.Run("delegation changes before pending delivery", func(t *testing.T) {
		f := newPermissionFixture(t)
		checks := 0
		f.beforeRequest = func(request *http.Request) {
			if request.URL.Path == "/v1/agents" {
				checks++
				if checks == 2 {
					f.project.Delegates = []string{"agent_replacement"}
				}
			}
		}
		request, err := f.engine.requestPermission(context.Background(), f.c, &f.task, "task:delegation-race", "workspace")
		if err != nil || request.State != "requested" || permissionDelivery(t, request, "agent_delegate").State != "no-longer-authorized" || len(f.messages) != 2 {
			t.Fatal("pending delivery did not recheck current recipient authority", request, err)
		}
		for _, attempt := range f.attempts {
			if attempt.RecipientID == "agent_delegate" {
				t.Fatal("revoked delegate received a new notice")
			}
		}
	})
	t.Run("task changes before pending delivery", func(t *testing.T) {
		f := newPermissionFixture(t)
		snapshot := f.task
		checks := 0
		f.beforeRequest = func(request *http.Request) {
			if request.URL.Path == api.Path("tasks", f.task.ID) {
				checks++
				if checks == 2 {
					f.task.SpecGeneration++
				}
			}
		}
		request, err := f.engine.requestPermission(context.Background(), f.c, &snapshot, "task:scope-race", "workspace")
		if err == nil || request.State != "delivery-pending" || len(f.attempts) != 0 {
			t.Fatal("pending delivery did not recheck current task context", request, err)
		}
	})
	t.Run("expiry during recipient check", func(t *testing.T) {
		f := newPermissionFixture(t)
		checks := 0
		f.beforeRequest = func(request *http.Request) {
			if request.URL.Path == "/v1/agents" {
				checks++
				if checks == 2 {
					f.now += 900
				}
			}
		}
		request, err := f.engine.requestPermission(context.Background(), f.c, &f.task, "task:deadline-race", "workspace")
		if err != nil || request.State != "expired" || len(f.attempts) != 0 {
			t.Fatal("delivery proceeded after expiry while rechecking recipients", request, err)
		}
	})
}

func TestPermissionStateFormatValidationAndLegacyDeadline(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	for _, item := range []struct{ key, requirement string }{
		{"", "workspace"}, {"/private/workspace", "workspace"}, {"action with private description", "workspace"}, {strings.Repeat("a", 513), "workspace"}, {"action", "provider-login"}, {"action", "WORKSPACE"},
	} {
		if _, err := f.engine.requestPermission(ctx, f.c, nil, item.key, item.requirement); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("unbounded/unrecognized permission context accepted", item, err)
		}
	}
	if f.requests != 0 || f.stored(t) != nil {
		t.Fatal("invalid permission requests had side effects")
	}
	legacy := PermissionRequest{ID: "permission_request_legacy", AgentID: "agent_target", ActionKey: "explicit:legacy", Requirement: "workspace", State: "delivery-pending", CreatedAt: f.now - 900, Deliveries: []AuthenticationDelivery{}}
	if err := f.state.Put(ctx, permissionKey, []PermissionRequest{legacy}); err != nil {
		t.Fatal(err)
	}
	before := f.stored(t)
	requests, err := f.engine.permissionRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].State != "expired" || requests[0].ExpiresAt != legacy.CreatedAt+900 || !bytes.Equal(before, f.stored(t)) {
		t.Fatal("legacy deadline was renewed or rewritten", requests, err)
	}
	legacy.FormatVersion = permissionFormat + 1
	if err := f.state.Put(ctx, permissionKey, []PermissionRequest{legacy}); err != nil {
		t.Fatal(err)
	}
	before = f.stored(t)
	if _, err := f.engine.permissionRequests(ctx); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("future permission state was accepted", err)
	}
	if err := f.engine.resolvePermission(ctx, legacy.ActionKey, legacy.Requirement); err == nil || domain.AsFault(err).Code != domain.Prerequisite || !bytes.Equal(before, f.stored(t)) {
		t.Fatal("resolution reset future state", err)
	}
}

func TestPermissionPreparedDeliveryRejectsProtectedFieldMismatch(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	f.failures["agent_delegate"] = domain.Fail(domain.Unavailable, "synthetic failed delivery")
	request, err := f.engine.requestPermission(ctx, f.c, &f.task, "task:prepared", "workspace")
	if err == nil {
		t.Fatal("fixture did not retain pending delivery")
	}
	delivery := permissionDelivery(t, request, "agent_delegate")
	var prepared proposal
	if found, err := f.state.Get(ctx, "proposal:"+delivery.OperationID, &prepared); err != nil || !found {
		t.Fatal("missing pending proposal", err)
	}
	var input domain.MessageRequest
	if err := json.Unmarshal(prepared.Body, &input); err != nil {
		t.Fatal(err)
	}
	input.ResponseRequest = true
	prepared.Body, _ = json.Marshal(input)
	if err := f.state.Put(ctx, "proposal:"+delivery.OperationID, prepared); err != nil {
		t.Fatal(err)
	}
	delete(f.failures, "agent_delegate")
	before := len(f.attempts)
	current, err := f.engine.requestPermission(ctx, f.c, &f.task, request.ActionKey, request.Requirement)
	if err == nil || domain.AsFault(err).Code != domain.Conflict || current.State != "delivery-pending" || len(f.attempts) != before {
		t.Fatal("prepared protected-field mismatch was sent or silently replaced", current, err)
	}
}

type permissionExecutor struct{ checks, invocations int }

func (p *permissionExecutor) Check(context.Context, string) (provider.Health, error) {
	p.checks++
	return provider.Health{}, errors.New("unexpected provider check")
}

func (p *permissionExecutor) Invoke(context.Context, provider.Invocation) (provider.Reply, error) {
	p.invocations++
	return provider.Reply{}, errors.New("unexpected inference")
}

func TestPermissionNoticesAreDataNeverAutomaticResponses(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	executor := &permissionExecutor{}
	f.engine.Executor = executor
	configuration := f.engine.Config
	for index, requirement := range []string{"workspace", "provider-permission", "task-authority", "retry-approval"} {
		request, err := f.engine.requestPermission(ctx, f.c, &f.task, "explicit:notice-"+strconv.Itoa(index), requirement)
		if err != nil || request.State != "requested" || request.Requirement != requirement {
			t.Fatal(request, err)
		}
		body := permissionMessage(request)
		remediation := map[string][]string{
			"workspace":           {"agent-cli workspace bind --path OWNED_DIR --allow-tools", "agent-cli workspace enable REF"},
			"provider-permission": {"agent-cli provider status", "there is no generic Foundation OS-permission grant"},
			"task-authority":      {"agent-cli project delegate " + request.ProjectID, "agent-cli task change " + request.TaskID, "kind approve"},
			"retry-approval":      {"agent-cli task runs " + request.TaskID, "agent-cli task change " + request.TaskID, "kind retry-approve"},
		}
		for _, action := range remediation[requirement] {
			if !strings.Contains(body, action) {
				t.Fatal("notice omitted its exact supported remediation reference", requirement, action)
			}
		}
		for _, expected := range []string{request.ID, request.AgentID, request.ProjectID, request.TaskID, requirement, time.Unix(request.ExpiresAt, 0).UTC().Format(time.RFC3339), "local OS operator authority", "No inference, login, approval", "will not automatically renew"} {
			if !strings.Contains(body, expected) {
				t.Fatal("notice omitted its safe reference/remediation boundary", requirement, expected)
			}
		}
		for _, private := range []string{f.state.Root, f.c.ProviderPaths["codex"], "%!", f.task.Description} {
			if private != "" && strings.Contains(body, private) {
				t.Fatal("notice included private machine/provider material or invalid formatting", requirement)
			}
		}
		f.inbox = append(f.inbox, domain.Message{ID: "message_notice_" + strconv.Itoa(index), Kind: "permission", Body: body, ResponseRequest: index%2 == 0})
	}
	for _, urgent := range []bool{false, true} {
		acted, err := f.engine.messages(ctx, f.c, urgent, true)
		if err != nil || acted {
			t.Fatal("permission notification became an automatic text response", acted, err)
		}
	}
	if executor.checks != 0 || executor.invocations != 0 || !reflect.DeepEqual(f.engine.Config, configuration) {
		t.Fatal("permission notification changed mode or invoked a provider", executor)
	}
	for _, attempt := range f.attempts {
		if attempt.Kind != "permission" || attempt.Origin != "service" || attempt.ResponseRequest || !attempt.Urgent {
			t.Fatal("permission request lost protected delivery semantics", attempt)
		}
	}
}
