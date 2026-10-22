//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type authenticationExecutor struct {
	authentication string
	checkError     error
	checks         []string
	invocations    int
}

func (x *authenticationExecutor) Check(_ context.Context, name string) (provider.Health, error) {
	x.checks = append(x.checks, name)
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/PRIVATE_PROVIDER_PATH_MUST_NOT_BE_SENT",
		Authentication: x.authentication, Models: []string{}}, x.checkError
}

func (x *authenticationExecutor) Invoke(_ context.Context, invocation provider.Invocation) (provider.Reply, error) {
	x.invocations++
	return provider.Reply{Outcome: "reviewed", Summary: "fixture reviewed capabilities without executing a task", Evidence: []string{},
		Questions: []provider.Question{}, AnswerIDs: []string{}}, nil
}

func authenticationEngine(t *testing.T, h *harness, x *authenticationExecutor) *work.Engine {
	t.Helper()
	engine := newEngine(t, h, &executor{})
	engine.Executor = x
	return engine
}

func authenticationInbox(t *testing.T, client *api.Client) []domain.Message {
	t.Helper()
	page, err := api.Call[domain.Page[domain.Message]](context.Background(), client, "GET", "/v1/messages?limit=200", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := []domain.Message{}
	for _, message := range page.Items {
		if message.Kind == "authentication" {
			out = append(out, message)
		}
	}
	return out
}

func requireAuthenticationBlock(t *testing.T, engine *work.Engine, code domain.Code) work.Status {
	t.Helper()
	status, err := engine.Step(context.Background(), false)
	if err == nil || domain.AsFault(err).Code != code || status.State != "blocked" || len(status.AuthenticationRequests) != 1 {
		t.Fatalf("wanted an explicit %s sign-in blocker and durable request, got status=%+v error=%v", code, status, err)
	}
	read, err := engine.Status(context.Background())
	if err != nil || len(read.AuthenticationRequests) != 1 || read.AuthenticationRequests[0].ID != status.AuthenticationRequests[0].ID {
		t.Fatal("read-only work status lost the authentication request", read, err)
	}
	return status
}

func requireTaskAuthenticationBlock(t *testing.T, engine *work.Engine) work.AuthenticationRequest {
	t.Helper()
	status, err := engine.Step(context.Background(), false)
	if err != nil || status.State != "blocked" || len(status.AuthenticationRequests) != 1 || len(status.Blockers) == 0 {
		t.Fatal("task sign-in blocker/durable request is missing", status, err)
	}
	return status.AuthenticationRequests[0]
}

func restartAuthenticationEngine(t *testing.T, engine *work.Engine) *work.Engine {
	t.Helper()
	if err := engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(context.Background(), engine.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	restarted := work.New(engine.Config, engine.ConfigPath, engine.Client, state, engine.Executor)
	restarted.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return restarted
}

func TestMissingAuthenticationReachesFounderBeforeOnboardingWithoutInference(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			x := &authenticationExecutor{authentication: "login-required"}
			engine := authenticationEngine(t, h, x)
			engine.Config.Provider = name
			status := requireAuthenticationBlock(t, engine, domain.Prerequisite)
			request := status.AuthenticationRequests[0]
			if request.Provider != name || request.State != "requested" || len(request.Deliveries) != 1 ||
				request.Deliveries[0].RecipientID != h.founderAgent.ID || request.Deliveries[0].MessageID == "" {
				t.Fatal("wrong provider/recipient/delivery state", request)
			}
			messages := authenticationInbox(t, h.founder)
			if len(messages) != 1 {
				t.Fatal("Founder did not receive exactly one sign-in request", messages)
			}
			message := messages[0]
			if message.Origin != "service" || message.SenderID != h.workerAgent.ID || message.ResponseRequest ||
				message.Urgent || message.ID != request.Deliveries[0].MessageID {
				t.Fatal("authentication handoff is not protected/attributed", message)
			}
			for _, expected := range []string{request.ID, h.workerAgent.ID, name, "agent mode",
				"assistance open GRANT --provider " + name, "Expires at:", "will not automatically renew", "autonomous", "delegate may be an AI", "spending"} {
				if !strings.Contains(message.Body, expected) {
					t.Fatal("request omitted an actionable boundary/reference", expected, message.Body)
				}
			}
			for _, private := range []string{h.worker.Credential.Token, engine.Config.StateDir, "/PRIVATE_PROVIDER_PATH_MUST_NOT_BE_SENT"} {
				if strings.Contains(message.Body, private) {
					t.Fatal("private provider/credential material entered a message")
				}
			}
			for range 3 {
				next := requireAuthenticationBlock(t, engine, domain.Prerequisite)
				if next.AuthenticationRequests[0].ID != request.ID {
					t.Fatal("unchanged blocker minted a new request")
				}
			}
			engine = restartAuthenticationEngine(t, engine)
			receipt := domain.MessageReceipt{Command: command(), State: "handled"}
			receipt.ExpectedRevision = message.Revision
			if _, err := api.Call[domain.Message](context.Background(), h.founder, "POST", api.Path("messages", message.ID, "receipt"), receipt); err != nil {
				t.Fatal(err)
			}
			requireAuthenticationBlock(t, engine, domain.Prerequisite)
			if len(authenticationInbox(t, h.founder)) != 0 {
				t.Fatal("acknowledging a request reopened or duplicated it")
			}
			if x.invocations != 0 {
				t.Fatal("authentication request depended on model inference", x.invocations)
			}
			journals, err := engine.State.Journals(context.Background(), false)
			if err != nil || len(journals) != 0 {
				t.Fatal("missing authentication started a provider invocation", journals, err)
			}
			sessions, err := api.Call[[]domain.Session](context.Background(), h.worker, "GET", "/v1/sessions", nil)
			if err != nil || len(sessions) != 0 || engine.Config.Mode != "ai" {
				t.Fatal("authentication notice created consent/session or changed mode", sessions, err)
			}
		})
	}
}

type authenticationTransport struct {
	base       http.RoundTripper
	mode       string
	failed     atomic.Bool
	operations []string
}

func (transport *authenticationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == "POST" && request.URL.Path == "/v1/messages" {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(strings.NewReader(string(data)))
		var message domain.MessageRequest
		if err := json.Unmarshal(data, &message); err != nil {
			return nil, err
		}
		transport.operations = append(transport.operations, message.OperationID)
		if transport.failed.CompareAndSwap(false, true) {
			if transport.mode == "unavailable" {
				return nil, errors.New("fixture message delivery unavailable")
			}
			response, err := transport.base.RoundTrip(request)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(io.Discard, response.Body)
			return nil, errors.Join(errors.New("fixture lost message acknowledgment"), copyErr, response.Body.Close())
		}
	}
	if transport.mode == "no-authority" && request.URL.Path == "/v1/agents" {
		body := `{"ok":true,"data":[],"warnings":[]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Fdn-Api-Version": []string{strconv.Itoa(domain.APIVersion)}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	}
	return transport.base.RoundTrip(request)
}

func TestAuthenticationDeliveryFailureAndLostAckRemainDurableWithoutDuplicates(t *testing.T) {
	for _, mode := range []string{"unavailable", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			x := &authenticationExecutor{authentication: "login-required"}
			engine := authenticationEngine(t, h, x)
			original := h.worker.HTTP.Transport
			transport := &authenticationTransport{base: original, mode: mode}
			h.worker.HTTP.Transport = transport
			defer func() { h.worker.HTTP.Transport = original }()
			first := requireAuthenticationBlock(t, engine, domain.Unavailable).AuthenticationRequests[0]
			if first.State != "delivery-pending" || first.Error == nil || first.Deliveries[0].MessageID != "" {
				t.Fatal("uncertain delivery was presented as success", first)
			}
			engine = restartAuthenticationEngine(t, engine)
			engine.Now = func() time.Time { return time.Now().Add(time.Minute) }
			second := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
			if second.ID != first.ID || second.CreatedAt != first.CreatedAt || second.State != "requested" || second.Error != nil {
				t.Fatal("delivery replay changed the immutable request", first, second)
			}
			messages := authenticationInbox(t, h.founder)
			if len(messages) != 1 || messages[0].ID != second.Deliveries[0].MessageID || x.invocations != 0 ||
				len(transport.operations) != 2 || transport.operations[0] != transport.operations[1] {
				t.Fatal("message replay duplicated/lost delivery or invoked a provider", messages, transport.operations)
			}
		})
	}
}

func TestAuthenticationNoticeRoutesTaskProviderToCurrentProjectAuthorities(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "credentials-rejected"}
	engine := authenticationEngine(t, h, x)
	task := readyForEngine(t, h, engine)
	task = h.change(t, h.founder, task.ID, "provider", func(change *domain.TaskChange) { change.Provider = "copilot" })
	project, err := api.Call[domain.Project](context.Background(), h.founder, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID}
	grant.ExpectedRevision = project.Revision
	project, err = api.Call[domain.Project](context.Background(), h.founder, "POST", api.Path("projects", project.ID, "delegates"), grant)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := engine.State.RefreshInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.State.Put(context.Background(), "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	request := requireTaskAuthenticationBlock(t, engine)
	if request.Provider != "copilot" || request.ProjectID != task.ProjectID || request.TaskID != task.ID ||
		len(request.Deliveries) != 2 || len(authenticationInbox(t, h.founder)) != 1 || len(authenticationInbox(t, h.other)) != 1 {
		t.Fatal("task required provider or relevant delegate notification was skipped", request)
	}
	grant = domain.GrantRequest{Command: command(), AgentID: h.otherAgent.ID, Revoke: true}
	grant.ExpectedRevision = project.Revision
	if _, err := api.Call[domain.Project](context.Background(), h.founder, "POST", api.Path("projects", project.ID, "delegates"), grant); err != nil {
		t.Fatal(err)
	}
	next := requireTaskAuthenticationBlock(t, engine)
	for _, delivery := range next.Deliveries {
		if delivery.RecipientID == h.otherAgent.ID && delivery.State != "no-longer-authorized" {
			t.Fatal("revoked delegate remained an eligible pending recipient", delivery)
		}
	}
	if x.invocations != 0 || len(x.checks) != 2 || x.checks[0] != "copilot" || x.checks[1] != "copilot" {
		t.Fatal("task provider selection started/fell back to another model", x.checks, x.invocations)
	}
}

func TestUnknownAuthenticationAndProbeFailuresDoNotInventLoginRequests(t *testing.T) {
	for _, checkError := range []error{nil, domain.Fail(domain.Unavailable, "fixture provider status outage")} {
		h := newHarness(t)
		x := &authenticationExecutor{authentication: "unknown", checkError: checkError}
		engine := authenticationEngine(t, h, x)
		if _, err := engine.Step(context.Background(), false); err == nil {
			t.Fatal("unknown authentication was accepted")
		}
		status, err := engine.Status(context.Background())
		if err != nil || len(status.AuthenticationRequests) != 0 || len(authenticationInbox(t, h.founder)) != 0 || x.invocations != 0 {
			t.Fatal("uncertain provider status manufactured a sign-in request", status, err)
		}
	}
}

func TestAuthenticationRecoveryRearmsOnlyAfterHealthyObservation(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	first := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	x.authentication = "authenticated"
	status, err := engine.Step(context.Background(), false)
	if err != nil || len(status.AuthenticationRequests) != 1 || status.AuthenticationRequests[0].State != "resolved" || x.invocations != 1 {
		t.Fatal("healthy authentication did not resolve the old notice", status, err)
	}
	x.authentication = "login-required"
	if _, err := engine.State.RefreshInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	if next.ID == first.ID || next.State != "requested" || len(authenticationInbox(t, h.founder)) != 2 || x.invocations != 1 {
		t.Fatal("a new real sign-out did not create a fresh episode, or invoked a model", next)
	}
}

func TestMissingAuthenticationAuthorityRemainsExplicitlyPending(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	original := h.worker.HTTP.Transport
	h.worker.HTTP.Transport = &authenticationTransport{base: original, mode: "no-authority"}
	defer func() { h.worker.HTTP.Transport = original }()
	request := requireAuthenticationBlock(t, engine, domain.Prerequisite).AuthenticationRequests[0]
	if request.State != "delivery-pending" || request.Error == nil || len(request.Deliveries) != 0 ||
		!strings.Contains(request.Error.Message, "no active Founder") || x.invocations != 0 {
		t.Fatal("missing recipients were silently reported as delivered", request)
	}
}

func TestProtectedAuthenticationMessageNeverBecomesAnAutomaticAIReply(t *testing.T) {
	h := newHarness(t)
	signedOut := &authenticationExecutor{authentication: "login-required"}
	worker := authenticationEngine(t, h, signedOut)
	requireAuthenticationBlock(t, worker, domain.Prerequisite)
	receiver := &authenticationExecutor{authentication: "authenticated"}
	founder := authenticationEngine(t, h, receiver)
	founder.Client = h.founder
	inventory, err := founder.State.RefreshInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := founder.State.Put(context.Background(), "initial-review", inventory.Hash()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := founder.Step(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	if receiver.invocations != 0 || len(authenticationInbox(t, h.founder)) != 1 || signedOut.invocations != 0 {
		t.Fatal("protected authentication notification entered an automatic provider prompt")
	}
}

func TestFounderPauseCannotBeBypassedByManualWorkRun(t *testing.T) {
	h := newHarness(t)
	x := &authenticationExecutor{authentication: "login-required"}
	engine := authenticationEngine(t, h, x)
	mode := domain.ModeRequest{Command: command(), Mode: "manual", Reason: "Founder explicitly pauses worker"}
	mode.ExpectedRevision = h.workerAgent.Revision
	if _, err := api.Call[domain.Agent](context.Background(), h.founder, "POST", api.Path("agents", h.workerAgent.ID, "mode"), mode); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Step(context.Background(), false)
	if err != nil || status.State != "manual" || len(status.AuthenticationRequests) != 0 || len(x.checks) != 0 {
		t.Fatal("manual mode started automatic provider activity", status, err)
	}
	status, err = engine.Step(context.Background(), true)
	if err == nil || domain.AsFault(err).Code != domain.Prerequisite || len(status.AuthenticationRequests) != 0 ||
		len(authenticationInbox(t, h.founder)) != 0 || x.invocations != 0 || engine.Config.Mode != "manual" {
		t.Fatal("manual entrypoint bypassed Founder pause", status, err)
	}
}
