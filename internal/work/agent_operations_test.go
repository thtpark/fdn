package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
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
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
)

type operationExecutor struct {
	checks, invocations atomic.Int64
	checkError          error
}

func (x *operationExecutor) Check(context.Context, string) (provider.Health, error) {
	x.checks.Add(1)
	return provider.Health{Provider: "codex", Version: "1.2.3", Path: "/PRIVATE_PROVIDER_PATH", Authentication: "credentials-present-unverified", Models: []string{"PRIVATE_MODEL_CATALOG"}}, x.checkError
}
func (x *operationExecutor) Invoke(context.Context, provider.Invocation) (provider.Reply, error) {
	x.invocations.Add(1)
	return provider.Reply{}, errors.New("operation controls must never invoke AI")
}

type operationFixture struct {
	t                  *testing.T
	mu                 sync.Mutex
	engine             *Engine
	executor           *operationExecutor
	operation          domain.AgentOperation
	me, founder        domain.Agent
	session            domain.Session
	bodies             map[string][]byte
	reports            []domain.AgentOperationReport
	lostAck            bool
	getCount           int
	denyBeforeMutation bool
}

func newOperationFixture(t *testing.T, kind string, proof bool) *operationFixture {
	t.Helper()
	state, err := local.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	f := &operationFixture{t: t, executor: &operationExecutor{}, bodies: map[string][]byte{},
		me: domain.Agent{ID: "agent_target", Active: true, Mode: "manual"}, founder: domain.Agent{ID: "agent_founder", Active: true, Founder: true, Mode: "manual"},
		session: domain.Session{ID: "session_exact", TargetID: "agent_target", State: "closed", ExpiresAt: 1799999900}}
	c := config.DefaultWorker(state.Root)
	c.Mode = "manual"
	client := &api.Client{BaseURL: "https://fixture.invalid", Credential: config.Credential{AgentID: f.me.ID}, HTTP: &http.Client{Transport: f}}
	f.engine = New(c, "", client, state, f.executor)
	f.engine.Now = func() time.Time { return time.Unix(1800000000, 0) }
	f.engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := state.Put(context.Background(), "session-fence", remote.Fence{Active: true, ID: f.session.ID, CleanupProven: proof, Generation: "fence_original"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := remote.ReadSessionFence(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	f.operation = domain.AgentOperation{Version: domain.AgentOperationVersion, ID: "operation_fixture", Revision: 1, Kind: kind,
		RequesterID: f.founder.ID, TargetID: f.me.ID, RequesterGeneration: 2, TargetGeneration: 3, State: "queued", Reason: "Authorized bounded fixture check", CreatedAt: 1799999999, ExpiresAt: 1800000899, ServerObservedAt: 1800000000}
	if kind == "session-recovery" {
		f.operation.SessionID, f.operation.ExpectedFence = f.session.ID, snapshot.Fingerprint
	}
	t.Cleanup(func() {
		if err := f.engine.State.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *operationFixture) response(value any, fault *domain.Fault) (*http.Response, error) {
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	body, err := json.Marshal(api.Envelope{OK: fault == nil, Data: data, Error: fault})
	if err != nil {
		f.t.Fatal(err)
	}
	status := http.StatusOK
	if fault != nil {
		status = domain.HTTPStatus(fault)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"X-Fdn-Api-Version": []string{strconv.Itoa(domain.APIVersion)}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (f *operationFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := request.URL.Path
	if request.Method == http.MethodGet {
		switch path {
		case "/v1/me":
			return f.response(f.me, nil)
		case api.Path("agents", f.founder.ID):
			return f.response(f.founder, nil)
		case "/v1/sessions/session_exact":
			return f.response(f.session, nil)
		case "/v1/agent-operations":
			items := []domain.AgentOperation{}
			if f.operation.State == "queued" || f.operation.State == "checking" {
				items = append(items, f.operation)
			}
			return f.response(domain.Page[domain.AgentOperation]{Items: items}, nil)
		case "/v1/agent-operations/operation_fixture":
			f.getCount++
			if f.denyBeforeMutation && f.getCount >= 3 {
				return f.response(nil, domain.Fail(domain.Denied, "authority changed"))
			}
			return f.response(f.operation, nil)
		}
	}
	if request.Method == http.MethodPost && path == "/v1/agent-operations/operation_fixture/result" {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			f.t.Fatal(err)
		}
		var report domain.AgentOperationReport
		if err := json.Unmarshal(data, &report); err != nil {
			f.t.Fatal(err)
		}
		f.reports = append(f.reports, report)
		if previous, ok := f.bodies[report.OperationID]; ok {
			if !bytes.Equal(previous, data) {
				return f.response(nil, domain.Fail(domain.Conflict, "operation changed exact report"))
			}
		} else {
			if report.ExpectedRevision != f.operation.Revision {
				return f.response(nil, domain.Fail(domain.Conflict, "revision changed"))
			}
			f.bodies[report.OperationID] = append([]byte{}, data...)
			f.operation.State, f.operation.Result = report.State, report.Result
			f.operation.Revision++
			if report.State == "checking" {
				f.operation.StartedAt = 1800000000
			} else {
				f.operation.FinishedAt = 1800000000
			}
		}
		if f.lostAck && report.State != "checking" {
			f.lostAck = false
			return nil, errors.New("fixture lost response with /PRIVATE/error")
		}
		return f.response(f.operation, nil)
	}
	f.t.Errorf("unexpected control action: %s %s", request.Method, path)
	return f.response(nil, domain.Fail(domain.Invalid, "unexpected fixture route"))
}

func (f *operationFixture) fence() remote.Fence {
	f.t.Helper()
	var fence remote.Fence
	if _, err := f.engine.State.Get(context.Background(), "session-fence", &fence); err != nil {
		f.t.Fatal(err)
	}
	return fence
}

func TestAgentOperationFingerprintPinsRequestNotObservationProjection(t *testing.T) {
	operation := domain.AgentOperation{Version: 1, ID: "operation_exact", Kind: "session-recovery", RequesterID: "agent_founder", TargetID: "agent_target",
		RequesterGeneration: 1, TargetGeneration: 2, SessionID: "session_exact", ExpectedFence: domain.Hash("exact fence"), CreatedAt: 1800000000, ExpiresAt: 1800000900}
	original, err := agentOperationHash(operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Revision, operation.State, operation.ServerObservedAt, operation.StartedAt = 4, "checking", 1800000200, 1800000001
	operation.Result = &domain.AgentOperationResult{Code: "session-recovered", CapturedAt: 1800000100}
	projected, err := agentOperationHash(operation)
	if err != nil || projected != original {
		t.Fatal("server time or outcome projection changed immutable request identity", projected, original, err)
	}
	operation.TargetGeneration++
	changed, err := agentOperationHash(operation)
	if err != nil || changed == original {
		t.Fatal("new identity generation reused an old recovery proposal", changed, err)
	}
}

func TestDiagnosticRefreshRunsWhilePausedAndBusyWithoutInferenceOrFenceChanges(t *testing.T) {
	f := newOperationFixture(t, "diagnostics", false)
	ctx := context.Background()
	if err := f.engine.beginHealth(ctx, f.engine.Config); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.finishHealth(ctx, Status{State: "manual", Mode: "manual"}, nil); err != nil {
		t.Fatal(err)
	}
	slot, err := secure.Acquire(filepath.Join(f.engine.Config.StateDir, "work.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Close()
	before := f.fence()
	recovered, err := f.engine.handleAgentOperation(ctx, f.operation.ID)
	if err != nil || recovered || f.operation.State != "refreshed" {
		t.Fatal("fresh diagnostics depended on inference admission", recovered, f.operation, err)
	}
	observation := f.operation.Result.Observation
	if observation == nil || observation.WorkSlot != "occupied" || observation.Provider == nil || observation.Provider.Authentication != "credentials-present-unverified" || observation.Provider.Provider != "codex" {
		t.Fatal("fresh diagnostic metadata missing", observation)
	}
	if f.fence() != before || f.engine.Config.Mode != "manual" || f.executor.invocations.Load() != 0 || f.executor.checks.Load() != 1 {
		t.Fatal("diagnostic refresh performed recovery, resumed or inferred", f.fence(), f.executor.invocations.Load())
	}
	data, _ := json.Marshal(observation)
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("private provider output entered operation result", string(data))
	}
	if health := readHealth(t, f.engine); health.Authentication != "unknown" || health.Phase != "blocked" {
		t.Fatal("provider probe overwrote active work observations", health)
	}
}

func TestDiagnosticRefreshProbeFailureIsNotSuccessfulAuthentication(t *testing.T) {
	f := newOperationFixture(t, "diagnostics", false)
	f.executor.checkError = domain.Fail(domain.Unavailable, "/PRIVATE/provider/raw-output")
	if _, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID); err != nil {
		t.Fatal(err)
	}
	probe := f.operation.Result.Observation.Provider
	if f.operation.State != "refreshed" || probe.Authentication != "unknown" || probe.ProblemCode != domain.Unavailable || f.executor.invocations.Load() != 0 {
		t.Fatal("probe failure invented auth readiness or inference", f.operation, probe)
	}
}

func TestDiagnosticRefreshLostAckReplaysOriginalEvidenceWithoutNewProbe(t *testing.T) {
	f := newOperationFixture(t, "diagnostics", false)
	ctx := context.Background()
	f.lostAck = true
	if _, err := f.engine.handleAgentOperation(ctx, f.operation.ID); err == nil {
		t.Fatal("fixture did not lose the accepted diagnostic result")
	}
	observed := f.operation.Result.Observation.ObservedAt
	f.engine.Now = func() time.Time { return time.Unix(1800000300, 0) }
	if err := f.engine.processAgentOperations(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if f.executor.checks.Load() != 1 || f.operation.Result.Observation.ObservedAt != observed || f.executor.invocations.Load() != 0 {
		t.Fatal("result replay fabricated fresh observations or inference", f.operation.Result, f.executor.checks.Load())
	}
}

func TestRemoteSessionRecoveryRequiresExactProvenClosedFenceAndPreservesMode(t *testing.T) {
	for _, item := range []struct {
		name                                    string
		proof                                   bool
		sessionState, mode, wantState, wantCode string
	}{
		{"proof-missing", false, "closed", "ai", "operator-required", "cleanup-unproven"},
		{"paused-recovery", true, "closed", "manual", "recovered", "session-recovered"},
		{"enabled-recovery", true, "closed", "ai", "recovered", "session-recovered"},
		{"live-session", true, "active", "manual", "rejected", "session-live"},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := newOperationFixture(t, "session-recovery", item.proof)
			f.session.State, f.me.Mode = item.sessionState, item.mode
			f.engine.Config.Mode = item.mode
			recovered, err := f.engine.handleAgentOperation(context.Background(), f.operation.ID)
			if err != nil || f.operation.State != item.wantState || f.operation.Result.Code != item.wantCode {
				t.Fatal("wrong exact recovery outcome", recovered, f.operation, err)
			}
			if recovered != (item.wantState == "recovered") || f.fence().Active == recovered {
				t.Fatal("fence changed without exact native proof", recovered, f.fence())
			}
			if f.me.Mode != item.mode || f.engine.Config.Mode != item.mode || f.operation.Result.AutomationMayResume != (recovered && item.mode == "ai") || f.executor.invocations.Load() != 0 {
				t.Fatal("recovery changed mode or started inference", f.operation.Result)
			}
		})
	}
}

func TestRemoteSessionRecoveryLostAckReplaysDurableResultNotMutation(t *testing.T) {
	f := newOperationFixture(t, "session-recovery", true)
	ctx := context.Background()
	f.lostAck = true
	recovered, err := f.engine.handleAgentOperation(ctx, f.operation.ID)
	if !recovered || err == nil || f.fence().Active {
		t.Fatal("fixture did not lose acknowledgement after local commit", recovered, f.fence(), err)
	}
	if err := f.engine.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: f.session.ID, CleanupProven: true, Generation: "fence_replacement"}); err != nil {
		t.Fatal(err)
	}
	root, client, c := f.engine.State.Root, f.engine.Client, f.engine.Config
	if err := f.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = New(c, "", client, state, f.executor)
	f.engine.Now = func() time.Time { return time.Unix(1800000100, 0) }
	f.engine.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := f.engine.processAgentOperations(ctx, nil); err != nil {
		t.Fatal("lost result did not replay after restart", err)
	}
	if fence := f.fence(); !fence.Active || fence.Generation != "fence_replacement" {
		t.Fatal("lost result replay cleared replacement fence", fence)
	}
	var receipt local.AgentOperationReceipt
	if _, err := f.engine.State.Get(ctx, local.AgentOperationResultKey(f.operation.ID), &receipt); err != nil || receipt.DeliveryState != "delivered" {
		t.Fatal("result delivery remained pending", receipt, err)
	}
	if f.executor.invocations.Load() != 0 {
		t.Fatal("recovery replay invoked a model")
	}
}

func TestRemoteRecoveryRejectsChangedFenceBusySlotAndChangedAuthority(t *testing.T) {
	for _, name := range []string{"changed-fence", "busy-slot", "changed-authority", "expired"} {
		t.Run(name, func(t *testing.T) {
			f := newOperationFixture(t, "session-recovery", true)
			ctx := context.Background()
			switch name {
			case "changed-fence":
				if err := f.engine.State.Put(ctx, "session-fence", remote.Fence{Active: true, ID: f.session.ID, CleanupProven: true, Generation: "fence_other"}); err != nil {
					t.Fatal(err)
				}
			case "busy-slot":
				slot, err := secure.Acquire(filepath.Join(f.engine.Config.StateDir, "work.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer slot.Close()
			case "changed-authority":
				f.denyBeforeMutation = true
			case "expired":
				f.operation.State = "expired"
			}
			before := f.fence()
			recovered, err := f.engine.handleAgentOperation(ctx, f.operation.ID)
			if name == "changed-authority" && domain.AsFault(err).Code != domain.Denied {
				t.Fatal("authority race was accepted", err)
			}
			if name != "changed-authority" && err != nil {
				t.Fatal(err)
			}
			if recovered || f.fence() != before || f.executor.invocations.Load() != 0 {
				t.Fatal("recovery bypassed a fence, busy slot, expiry or authority", recovered, f.fence(), err)
			}
		})
	}
}
