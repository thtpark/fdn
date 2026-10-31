package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/apitest"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

type operationCLIRequest struct {
	method, path string
	body         []byte
}

func operationCLIFixture(t *testing.T, handler http.HandlerFunc, founder ...bool) (config.Worker, string, func() []operationCLIRequest) {
	t.Helper()
	root, err := os.MkdirTemp(".", ".op-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("HOME", filepath.Join(root, "home"))
	calls := filepath.Join(root, "provider-calls")
	t.Setenv("OPERATION_CLI_PROVIDER_CALLS", calls)
	executable := filepath.Join(root, "unexpected-provider")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'unexpected call\\n' >> \"$OPERATION_CLI_PROVIDER_CALLS\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(calls); !os.IsNotExist(err) {
			t.Error("operation CLI invoked a provider locally", err)
		}
	})
	for _, name := range []string{"codex", "copilot"} {
		if err := os.Symlink(executable, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	worker := config.DefaultWorker(filepath.Join(root, "state"))
	worker.IdentitySocket = filepath.Join(root, "i.sock")
	worker.ProviderPaths = map[string]string{"codex": executable, "copilot": executable}
	file := filepath.Join(root, "config", "agent.json")
	if err := config.Write(file, worker); err != nil {
		t.Fatal(err)
	}
	actor := domain.NewID("agent")
	if err := config.Write(worker.CredentialFile, config.Credential{AgentID: actor}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", worker.IdentitySocket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []operationCLIRequest
	server := &apitest.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		requests = append(requests, operationCLIRequest{r.Method, r.URL.RequestURI(), body})
		mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Error("operation CLI forwarded identity credentials")
		}
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		if r.URL.Path == "/v1/me" {
			if r.Method != http.MethodGet || len(body) != 0 {
				t.Error("identity check is not a bodyless GET")
			}
			diagnosticsEnvelope(t, w, domain.Agent{ID: actor, Founder: len(founder) == 0 || founder[0], Active: true, Mode: "manual"}, nil)
			return
		}
		handler(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
	return worker, file, func() []operationCLIRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
}

func TestAgentOperationCLIRefreshUsesDurableWriteAndExactReplay(t *testing.T) {
	ctx := context.Background()
	target := domain.NewID("agent")
	path := api.Path("agents", target, "diagnostics")
	operation := domain.AgentOperation{Version: domain.AgentOperationVersion, ID: domain.NewID("message"), TargetID: target, Kind: "diagnostics", State: "queued", CreatedAt: 1800000000, ExpiresAt: 1800000900, ServerObservedAt: 1800000002}
	var attempts int
	worker, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != path {
			t.Error("refresh used an unexpected route", r.Method, r.URL.RequestURI())
		}
		attempts++
		if attempts == 1 {
			diagnosticsEnvelope(t, w, nil, domain.AsFault(domain.Fail(domain.Unavailable, "fixture lost the accepted acknowledgement")))
			return
		}
		diagnosticsEnvelope(t, w, operation, nil)
	})
	args := []string{"--config", file, "--operation-id", "refresh-exact", "agent", "diagnose", target, "--refresh"}
	first, code := invoke(t, "agent", args, nil)
	if code != 7 || first.OK || first.OperationID != "refresh-exact" {
		t.Fatal("uncertain refresh was hidden", code, first)
	}
	second, code := invoke(t, "agent", args, nil)
	var got domain.AgentOperation
	if code != 0 || !second.OK || json.Unmarshal(second.Data, &got) != nil || !reflect.DeepEqual(got, operation) || got.Result != nil {
		t.Fatal("queued refresh was mistaken for fresh success", code, second, got)
	}
	recorded := requests()
	if len(recorded) != 4 || recorded[0].path != "/v1/me" || recorded[2].path != "/v1/me" || recorded[1].method != http.MethodPost || recorded[3].method != http.MethodPost || !bytes.Equal(recorded[1].body, recorded[3].body) {
		t.Fatal("refresh did not replay exactly once per explicit submission", recorded)
	}
	var req domain.DiagnosticRefreshRequest
	if err := config.Decode(recorded[1].body, &req); err != nil || req.OperationID != "refresh-exact" || req.ExpectedRevision != 0 || req.Reason != "Founder requested fresh diagnostics" {
		t.Fatal("refresh request changed metadata or disclosed caller input", req, err)
	}
	state, err := local.Open(ctx, worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	var proposal cliProposal
	found, err := state.Get(ctx, "cli-proposal:refresh-exact", &proposal)
	closeErr := state.Close()
	if err != nil || closeErr != nil || !found || proposal.Path != path || !bytes.Equal(proposal.Body, recorded[1].body) || len(proposal.Result) == 0 {
		t.Fatal("refresh bypassed durable CLI proposals", found, proposal, err, closeErr)
	}
	conflict, code := invoke(t, "agent", []string{"--config", file, "--operation-id", "refresh-exact", "agent", "diagnose", domain.NewID("agent"), "--refresh"}, nil)
	if code != 5 || conflict.OK || len(requests()) != 5 {
		t.Fatal("operation ID was rebound to another target", code, conflict, requests())
	}
}

func TestAgentOperationCLIRepairUsesExactInputAndCommonOutcome(t *testing.T) {
	target := domain.NewID("agent")
	req := domain.SessionRecoveryRequest{Command: domain.Command{OperationID: "repair-exact"}, SessionID: domain.NewID("session"), ExpectedFence: domain.Hash("fresh exact fence fixture"), Reason: "Recover the observed closed session"}
	operation := domain.AgentOperation{Version: domain.AgentOperationVersion, ID: domain.NewID("message"), Kind: "session-recovery", TargetID: target, SessionID: req.SessionID, ExpectedFence: req.ExpectedFence, State: "queued", CreatedAt: 1800000000, ExpiresAt: 1800000900, ServerObservedAt: 1800000000}
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == api.Path("agents", target, "repairs") {
			var submitted domain.SessionRecoveryRequest
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil || submitted != req {
				t.Error("repair changed exact request", submitted, err)
			}
			diagnosticsEnvelope(t, w, operation, nil)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != api.Path("agent-operations", operation.ID) || r.ContentLength != 0 {
			t.Error("repair outcome did not use the common bodyless operation route", r.Method, r.URL.Path)
		}
		result := operation
		result.State = "operator-required"
		result.ServerObservedAt = 1800000003
		result.Result = &domain.AgentOperationResult{Code: "cleanup-unproven", CapturedAt: 1800000001, ModeAtCheck: "manual", AutomationMayResume: false,
			Observation: &domain.AgentInspection{ObservedAt: 1800086400, WorkSlot: "session-fenced", SessionFence: &domain.SessionFenceObservation{SessionID: req.SessionID, Fingerprint: req.ExpectedFence, SessionState: "closed", RecoveryState: "operator-required"}}}
		diagnosticsEnvelope(t, w, result, nil)
	})
	input := filepath.Join(filepath.Dir(file), "repair.json")
	if err := config.Write(input, req); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", file, "agent", "repair", target, "--input", input}
	for range 2 {
		envelope, code := invoke(t, "agent", args, nil)
		var got domain.AgentOperation
		if code != 0 || !envelope.OK || envelope.OperationID != req.OperationID || json.Unmarshal(envelope.Data, &got) != nil || got.State != "queued" {
			t.Fatal("repair submission failed", code, envelope)
		}
	}
	recorded := requests()
	if len(recorded) != 4 || !bytes.Equal(recorded[1].body, recorded[3].body) {
		t.Fatal("repair proposal did not replay exact bytes", recorded)
	}
	envelope, code := invoke(t, "agent", []string{"--config", file, "agent", "operation", operation.ID}, nil)
	var got domain.AgentOperation
	if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &got) != nil || got.State != "operator-required" || got.Result == nil || got.Result.AutomationMayResume || got.Result.Observation.SessionFence.CleanupProven {
		t.Fatal("operation show obscured proof or state", code, envelope, got)
	}
	if got.ServerObservedAt != 1800000003 || got.Result.CapturedAt != 1800000001 || got.Result.Observation.ObservedAt != 1800086400 {
		t.Fatal("operation show conflated server capture/expiry with target UTC", got)
	}
	if len(requests()) != 6 {
		t.Fatal("repair polled, probed or inferred automatically", requests())
	}
	req.ExpectedFence = domain.Hash("changed fence")
	envelope, code = invoke(t, "agent", []string{"--config", file, "agent", "repair", target, "--input", "-"}, req)
	if code != 5 || envelope.OK || len(requests()) != 7 {
		t.Fatal("repair operation accepted changed fence input", code, envelope, requests())
	}
}

func TestAgentOperationCLIShowsAllStatesWithoutFreshnessFallback(t *testing.T) {
	for _, state := range []string{"queued", "checking", "refreshed", "recovered", "rejected", "operator-required", "expired"} {
		t.Run(state, func(t *testing.T) {
			operation := domain.AgentOperation{Version: domain.AgentOperationVersion, ID: domain.NewID("message"), Kind: "diagnostics", State: state, CreatedAt: 1800000000, ExpiresAt: 1800000900, ServerObservedAt: 1800000002}
			if state == "expired" {
				operation.ServerObservedAt = operation.ExpiresAt + 1
			}
			if state != "queued" && state != "checking" && state != "expired" {
				targetObservedAt := int64(1800086400)
				if state == "recovered" {
					targetObservedAt = 1799913600
				}
				operation.Result = &domain.AgentOperationResult{Code: "observed", CapturedAt: 1800000001, ModeAtCheck: "ai", AutomationMayResume: false, Observation: &domain.AgentInspection{ObservedAt: targetObservedAt, WorkSlot: "busy", Provider: &domain.ProviderObservation{Provider: "codex", Authentication: "credentials-present-unverified", ObservedAt: targetObservedAt}}}
			}
			_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != api.Path("agent-operations", operation.ID) || r.ContentLength != 0 {
					t.Error("operation lookup changed route or supplied a body", r.Method, r.URL.RequestURI())
				}
				diagnosticsEnvelope(t, w, operation, nil)
			})
			envelope, code := invoke(t, "agent", []string{"--config", file, "agent", "operation", operation.ID}, nil)
			var got domain.AgentOperation
			if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &got) != nil || !reflect.DeepEqual(got, operation) || len(requests()) != 2 {
				t.Fatal("operation outcome was replaced or polled", code, envelope, got)
			}
		})
	}
}

func TestAgentOperationCLICachedDiagnosisStaysBodyless(t *testing.T) {
	target := domain.NewID("agent")
	saved := domain.AgentDiagnostics{AgentID: target, State: "unknown", Stale: true, Reason: "No current observation"}
	worker, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != api.Path("agents", target, "diagnostics") || r.ContentLength != 0 {
			t.Error("cached diagnosis became an action", r.Method, r.URL.RequestURI())
		}
		diagnosticsEnvelope(t, w, saved, nil)
	})
	for _, flags := range [][]string{nil, {"--refresh=false"}} {
		args := append([]string{"--config", file, "--operation-id", "cached-observations", "agent", "diagnose", target}, flags...)
		envelope, code := invoke(t, "agent", args, nil)
		var got domain.AgentDiagnostics
		if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &got) != nil || !reflect.DeepEqual(got, saved) {
			t.Fatal("cached diagnosis replaced unknown/stale state", code, envelope)
		}
	}
	if len(requests()) != 4 {
		t.Fatal("cached diagnosis polled or submitted extra actions", requests())
	}
	state, err := local.Open(context.Background(), worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	var proposal cliProposal
	found, err := state.Get(context.Background(), "cli-proposal:cached-observations", &proposal)
	closeErr := state.Close()
	if err != nil || closeErr != nil || found {
		t.Fatal("cached diagnosis created a durable write proposal", found, err, closeErr)
	}
}

func TestAgentOperationCLIPreservesServerErrors(t *testing.T) {
	for _, command := range []string{"diagnose", "repair", "operation"} {
		for _, faultCode := range []domain.Code{domain.Denied, domain.NotFound, domain.Unavailable, domain.Conflict} {
			t.Run(command+"/"+string(faultCode), func(t *testing.T) {
				fault := domain.AsFault(domain.Fail(faultCode, "fixture operation unavailable"))
				_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
					diagnosticsEnvelope(t, w, nil, fault)
				})
				args := []string{"--config", file, "--operation-id", "unavailable-operation", "agent", command, "agent_fixture"}
				var input any
				switch command {
				case "diagnose":
					args = append(args, "--refresh")
				case "repair":
					args = append(args, "--input", "-")
					input = domain.SessionRecoveryRequest{SessionID: "session_fixture", ExpectedFence: domain.Hash("fence"), Reason: "Recover observed session"}
				}
				envelope, code := invoke(t, "agent", args, input)
				if code != domain.ExitCode(fault) || envelope.OK || len(envelope.Data) != 0 || !reflect.DeepEqual(envelope.Error, fault) || len(requests()) != 2 {
					t.Fatal("operation error was hidden, retried or replaced", code, envelope, requests())
				}
			})
		}
	}
}

func TestAgentOperationCLITypedRepliesDoNotEchoPrivateFields(t *testing.T) {
	for _, command := range []string{"diagnose", "repair", "operation"} {
		for _, malformed := range []bool{false, true} {
			t.Run(command+"/malformed="+strconv.FormatBool(malformed), func(t *testing.T) {
				_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
					var value any = map[string]any{
						"version": domain.AgentOperationVersion, "id": "message_fixture", "state": "refreshed", "server_observed_at": 1800000002, "raw_stdout": "fixture-private-output",
						"result": map[string]any{"code": "observed", "captured_at": 1800000001, "mode_at_check": "manual", "automation_may_resume": false, "raw_error": "fixture-private-error",
							"observation": map[string]any{"observed_at": 1800086400, "work_slot": "free", "workspace_path": "fixture-private-path",
								"provider":      map[string]any{"provider": "codex", "authentication": "credentials-present-unverified", "observed_at": 1800086400, "credentials": "fixture-private-credentials"},
								"session_fence": map[string]any{"session_id": "session_fixture", "fingerprint": domain.Hash("fence"), "cleanup_proven": false, "session_state": "closed", "recovery_state": "operator-required", "process_args": "fixture-private-arguments"}}},
					}
					if malformed {
						value = map[string]any{"version": "not-a-version", "raw_stdout": "fixture-private-output"}
					}
					diagnosticsEnvelope(t, w, value, nil)
				})
				args := []string{"--config", file, "agent", command, "agent_fixture"}
				var input any
				switch command {
				case "diagnose":
					args = append(args, "--refresh")
				case "repair":
					args = append(args, "--input", "-")
					input = domain.SessionRecoveryRequest{SessionID: "session_fixture", ExpectedFence: domain.Hash("fence"), Reason: "Recover observed session"}
				}
				envelope, code := invoke(t, "agent", args, input)
				if bytes.Contains(envelope.Data, []byte("fixture-private")) || len(requests()) != 2 {
					t.Fatal("operation response echoed private extras or performed extra work", code, envelope, requests())
				}
				if malformed {
					if code != 9 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Internal || len(envelope.Data) != 0 {
						t.Fatal("malformed operation became success", code, envelope)
					}
					return
				}
				var got domain.AgentOperation
				if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &got) != nil || got.Result == nil || got.Result.Code != "observed" || got.Result.Observation.Provider.Authentication != "credentials-present-unverified" {
					t.Fatal("typed operation discarded safe observation", code, envelope)
				}
				if got.ServerObservedAt != 1800000002 || got.Result.CapturedAt != 1800000001 || got.Result.Observation.ObservedAt != 1800086400 || got.Result.Observation.Provider.ObservedAt != 1800086400 {
					t.Fatal("typed operation dropped server clocks or normalized skewed target observations", got)
				}
			})
		}
	}
}

func TestAgentOperationCLIRejectsIncompleteOrFutureOutcome(t *testing.T) {
	for _, command := range []string{"diagnose", "repair", "operation"} {
		for _, item := range []struct {
			name  string
			value any
			code  domain.Code
		}{
			{"null", nil, domain.Internal},
			{"empty", map[string]any{}, domain.Internal},
			{"future", domain.AgentOperation{Version: domain.AgentOperationVersion + 1, ID: "message_fixture", State: "queued"}, domain.Prerequisite},
		} {
			t.Run(command+"/"+item.name, func(t *testing.T) {
				_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
					diagnosticsEnvelope(t, w, item.value, nil)
				})
				args := []string{"--config", file, "agent", command, "agent_fixture"}
				var input any
				switch command {
				case "diagnose":
					args = append(args, "--refresh")
				case "repair":
					args = append(args, "--input", "-")
					input = domain.SessionRecoveryRequest{SessionID: "session_fixture", ExpectedFence: domain.Hash("fence"), Reason: "Recover observed session"}
				}
				envelope, code := invoke(t, "agent", args, input)
				if code != domain.ExitCode(domain.Fail(item.code, "")) || envelope.OK || envelope.Error == nil || envelope.Error.Code != item.code || len(envelope.Data) != 0 || len(requests()) != 2 {
					t.Fatal("incomplete/future operation became success or triggered fallback", code, envelope, requests())
				}
			})
		}
	}
}

func TestAgentOperationCLIRejectsFlagsAndArityBeforeRequests(t *testing.T) {
	a := &app{}
	for _, args := range [][]string{
		{"diagnose", "--refresh"}, {"diagnose", "ID", "--refresh", "extra"}, {"diagnose", "ID", "--refresh", "--input", "-"},
		{"diagnose", "ID", "--wait"}, {"diagnose", "ID", "--refresh", "--wait"}, {"diagnose", "ID", "--provider", "codex"},
		{"diagnose", "ID", "--inference"}, {"diagnose", "ID", "--shell", "command"}, {"diagnose", "ID", "--refresh=invalid"},
		{"repair"}, {"repair", "--input", "-"}, {"repair", "ID"}, {"repair", "ID", "--input"}, {"repair", "ID", "extra"},
		{"repair", "ID", "--input", "-", "extra"}, {"repair", "ID", "--input", "-", "--acknowledge-stopped"},
		{"repair", "ID", "--wait"}, {"repair", "ID", "--session", "SESSION"}, {"repair", "ID", "--journal", "INVOCATION"},
		{"operation"}, {"operation", "--input"}, {"operation", "ID", "extra"}, {"operation", "ID", "--wait"},
		{"operation", "ID", "--refresh"}, {"operation", "ID", "--input", "-"}, {"operation", "ID", "--limit", "5"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			value, err := a.agentCommand(context.Background(), &resources{}, args)
			if value != nil || err == nil || domain.AsFault(err).Code != domain.Invalid {
				t.Fatal("operation command accepted unsupported arguments", args, value, err)
			}
		})
	}
}

func TestAgentOperationCLIRejectsUnknownPrivateRepairInput(t *testing.T) {
	for _, field := range []string{"acknowledge_stopped", "cleanup_proven", "proof", "native_pid", "process_args", "workspace_path", "credentials", "requester_id", "target_generation", "server_observed_at", "captured_at", "state", "result"} {
		t.Run(field, func(t *testing.T) {
			a := &app{in: strings.NewReader(`{"session_id":"session_fixture","expected_fence":"` + domain.Hash("fence") + `","reason":"Recover observed session","` + field + `":true}`)}
			value, err := a.agentCommand(context.Background(), &resources{}, []string{"repair", "agent_fixture", "--input", "-"})
			if value != nil || err == nil || domain.AsFault(err).Code != domain.Invalid {
				t.Fatal("repair accepted caller proof/private/control input", field, value, err)
			}
		})
	}
}

func TestAgentOperationCLIHelpAndDiscoveryRemainOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for command, terms := range map[string][]string{
		"diagnose":  {"AGENT_ID [--refresh]", "read-only", "Founder-only", "15-minute", "agent operation REQUEST_ID", "no login, inference or mode change"},
		"repair":    {"AGENT_ID --input FILE", "closed-session", "session_id", "expected_fence SHA256", "durable native cleanup proof", "no acknowledge-stopped", "agent operation REQUEST_ID"},
		"operation": {"REQUEST_ID", "read-only common state/outcome", "queued/checking is not fresh success", "no leaf flags"},
	} {
		for _, flag := range []string{"--help", "-h"} {
			envelope, code := invoke(t, "agent", []string{"--config", "missing-operation-config.json", "agent", command, flag}, nil)
			var help struct{ Command, Usage string }
			if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &help) != nil || help.Command != "agent "+command {
				t.Fatal("operation help requires enrollment or omitted command", command, code, envelope)
			}
			for _, term := range terms {
				if !strings.Contains(help.Usage, term) {
					t.Fatal("operation help omitted boundary", command, term, help)
				}
			}
		}
	}
	envelope, code := invoke(t, "agent", []string{"--help"}, nil)
	var help struct{ Commands []string }
	if code != 0 || json.Unmarshal(envelope.Data, &help) != nil {
		t.Fatal("operation discovery failed", code, envelope)
	}
	for _, command := range []string{"agent diagnose", "agent repair", "agent operation", "agent recover"} {
		if !slices.Contains(help.Commands, command) {
			t.Fatal("operation/identity command missing from discovery", command, help)
		}
	}
}
