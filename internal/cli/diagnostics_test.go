package cli

import (
	"context"
	"encoding/json"
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
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func diagnosticsEnvelope(t *testing.T, w http.ResponseWriter, value any, fault *domain.Fault) {
	t.Helper()
	envelope := api.Envelope{OK: fault == nil, Error: fault, Warnings: []string{}}
	if fault != nil {
		w.WriteHeader(http.StatusForbidden)
	} else {
		var err error
		envelope.Data, err = json.Marshal(value)
		if err != nil {
			t.Error(err)
			return
		}
	}
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		t.Error(err)
	}
}

func diagnosticsCLIFixture(t *testing.T, actor string, handler http.HandlerFunc) (config.Worker, string, func() []string) {
	t.Helper()
	root, err := os.MkdirTemp(".", ".diagnostics-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("HOME", filepath.Join(root, "home"))
	worker := config.DefaultWorker(filepath.Join(root, "state"))
	worker.IdentitySocket = filepath.Join(root, "i.sock")
	file := filepath.Join(root, "config", "agent.json")
	if err := config.Write(file, worker); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(worker.CredentialFile, config.Credential{AgentID: actor}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", worker.IdentitySocket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		if r.Method != http.MethodGet || r.ContentLength != 0 || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			t.Error("diagnostics must use the existing public-identity proxy with bodyless GET requests")
		}
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		if r.URL.Path == "/v1/me" {
			diagnosticsEnvelope(t, w, domain.Agent{ID: actor, Active: true, Mode: "ai"}, nil)
			return
		}
		handler(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
	return worker, file, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
}

func TestDiagnosticsCLIReadOnlyTypedRoutes(t *testing.T) {
	actor, target, message := domain.NewID("agent"), domain.NewID("agent"), domain.NewID("message")
	const now int64 = 1800000000
	for _, item := range []struct {
		name, group, id, path string
		value                 any
	}{
		{"agent-blocked", "agent", target, api.Path("agents", target, "diagnostics"), domain.AgentDiagnostics{
			AgentID: target, Name: "Fixture", Mode: "manual", State: "blocked", LastSeen: now, ReceivedAt: now, ObservedAt: now,
			Health: &domain.WorkerHealth{Version: domain.WorkerHealthVersion, Sequence: 2, ObservedAt: now, Mode: "manual", Provider: "copilot", PreferredProvider: "codex", Model: "fixture-model", ProviderVersion: "1.2.3", Authentication: "ready", AuthenticationObservedAt: now - 1,
				Phase: "blocked", PhaseStartedAt: now - 60, DeadlineAt: now + 60, LastProgressAt: now - 90, StepStartedAt: now - 5, StepFinishedAt: now - 4,
				PendingReports: 1, Blocker: "session", Problems: []domain.HealthProblem{{Component: "report", Code: domain.Unavailable, FirstSeenAt: now - 90, LastSeenAt: now - 4}}},
			PendingReplies: 1, OldestReplyAt: now - 120, Reason: "Founder pause and session fence remain", NextAction: "Inspect current authority and session",
			Incident: &domain.HealthIncident{ID: "incident_fixture", Kind: "report", State: "open", Since: now - 90, Reason: "Report pending", NextAction: "Inspect delivery"},
		}},
		{"agent-unknown", "agent", target, api.Path("agents", target, "diagnostics"), domain.AgentDiagnostics{
			AgentID: target, State: "unknown", ObservedAt: now, Stale: true, Reason: "No worker report received", NextAction: "Inspect the native service",
		}},
		{"agent-before-first-step", "agent", target, api.Path("agents", target, "diagnostics"), domain.AgentDiagnostics{
			AgentID: target, State: "unknown", LastSeen: now, ReceivedAt: now, ObservedAt: now,
			Health: &domain.WorkerHealth{Version: domain.WorkerHealthVersion, ObservedAt: now, Mode: "ai", PreferredProvider: "codex", Authentication: "unknown", Phase: "unknown", Problems: []domain.HealthProblem{}},
			Reason: "No controller step observed", NextAction: "Inspect the native service",
		}},
		{"message-blocked-recipient", "message", message, api.Path("messages", message, "diagnostics"), domain.MessageDiagnostics{
			MessageID: message, RecipientID: target, ResponseRequested: true, State: "blocked", Stage: "recipient", ObservedAt: now, HealthReceivedAt: now - 120, Stale: true,
			Reason: "Worker observations are stale", NextAction: "Inspect recipient service",
		}},
		{"message-generating-invoking", "message", message, api.Path("messages", message, "diagnostics"), domain.MessageDiagnostics{
			MessageID: message, RecipientID: target, ResponseRequested: true, State: "generating", Stage: "invoking", ObservedAt: now, HealthReceivedAt: now,
			Reason: "Reply invocation is still pending", NextAction: "Observe the declared deadline",
		}},
		{"message-handled", "message", message, api.Path("messages", message, "diagnostics"), domain.MessageDiagnostics{
			MessageID: message, RecipientID: target, ResponseRequested: true, State: "handled", Stage: "receipt", ObservedAt: now,
			Reason: "handled-receipt-only", NextAction: "Inspect the private conversation",
		}},
		{"message-not-requested", "message", message, api.Path("messages", message, "diagnostics"), domain.MessageDiagnostics{
			MessageID: message, RecipientID: target, State: "not-requested", Stage: "receipt", ObservedAt: now, Reason: "No response requested",
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			_, file, requests := diagnosticsCLIFixture(t, actor, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != item.path {
					t.Errorf("unexpected route: %s", r.URL.Path)
				}
				diagnosticsEnvelope(t, w, item.value, nil)
			})
			envelope, code := invoke(t, "agent", []string{"--config", file, "--json", "--non-interactive", "--operation-id", "diagnostics-read", item.group, "diagnose", item.id}, nil)
			if code != 0 || !envelope.OK || envelope.OperationID != "diagnostics-read" || envelope.Error != nil {
				t.Fatalf("diagnostics failed: code=%d envelope=%+v", code, envelope)
			}
			want, err := json.Marshal(item.value)
			if err != nil || string(envelope.Data) != string(want) {
				t.Fatalf("typed diagnostics changed observations: got=%s want=%s err=%v", envelope.Data, want, err)
			}
			if got := requests(); !slices.Equal(got, []string{"GET /v1/me", "GET " + item.path}) {
				t.Fatal("diagnostics performed unexpected probes or actions", got)
			}
		})
	}
}

func TestDiagnosticsCLIPreservesErrorsWithoutHealthyFallback(t *testing.T) {
	for _, group := range []string{"agent", "message"} {
		for _, item := range []struct {
			name       string
			code       domain.Code
			badVersion bool
			badData    bool
		}{
			{"denied", domain.Denied, false, false},
			{"not-found", domain.NotFound, false, false},
			{"unavailable", domain.Unavailable, false, false},
			{"incompatible", domain.Prerequisite, true, false},
			{"invalid-contract", domain.Internal, false, true},
		} {
			t.Run(group+"/"+item.name, func(t *testing.T) {
				id := domain.NewID(group)
				path := api.Path(group+"s", id, "diagnostics")
				fault := domain.Fail(item.code, "diagnostics unavailable")
				_, file, requests := diagnosticsCLIFixture(t, domain.NewID("agent"), func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != path {
						t.Errorf("unexpected route: %s", r.URL.Path)
					}
					if item.badVersion {
						w.Header().Set("X-Fdn-API-Version", "1")
						diagnosticsEnvelope(t, w, map[string]any{}, nil)
					} else if item.badData {
						diagnosticsEnvelope(t, w, map[string]any{"observed_at": "not-a-timestamp"}, nil)
					} else {
						diagnosticsEnvelope(t, w, nil, domain.AsFault(fault))
					}
				})
				envelope, code := invoke(t, "agent", []string{"--config", file, group, "diagnose", id}, nil)
				if code != domain.ExitCode(fault) || envelope.OK || envelope.Error == nil || envelope.Error.Code != item.code || len(envelope.Data) != 0 {
					t.Fatalf("diagnostics hid a failure: code=%d envelope=%+v", code, envelope)
				}
				if !item.badVersion && !item.badData && !reflect.DeepEqual(envelope.Error, domain.AsFault(fault)) {
					t.Fatal("diagnostics changed the server error envelope", envelope.Error)
				}
				if got := requests(); !slices.Equal(got, []string{"GET /v1/me", "GET " + path}) {
					t.Fatal("diagnostics retried or invoked another action after an error", got)
				}
			})
		}
	}
}

func TestDiagnosticsCLIRequiresExistingEnrollment(t *testing.T) {
	for _, group := range []string{"agent", "message"} {
		t.Run(group, func(t *testing.T) {
			worker, file, requests := diagnosticsCLIFixture(t, domain.NewID("agent"), func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unauthenticated diagnostics reached %s", r.URL.Path)
				diagnosticsEnvelope(t, w, nil, domain.AsFault(domain.Fail(domain.Denied, "not enrolled")))
			})
			call := func(file string, want domain.Code) {
				t.Helper()
				envelope, code := invoke(t, "agent", []string{"--config", file, group, "diagnose", domain.NewID(group)}, nil)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != want || code != domain.ExitCode(domain.Fail(want, "")) {
					t.Fatalf("diagnostics bypassed enrollment: code=%d envelope=%+v", code, envelope)
				}
			}
			call(file+".missing", domain.Prerequisite)
			if err := os.Remove(worker.CredentialFile); err != nil {
				t.Fatal(err)
			}
			call(file, domain.Prerequisite)
			if err := config.Write(worker.CredentialFile, config.Credential{AgentID: domain.NewID("agent"), Token: "fixture-retired-token"}); err != nil {
				t.Fatal(err)
			}
			call(file, domain.Prerequisite)
			if err := config.Write(worker.CredentialFile, config.Credential{AgentID: domain.NewID("agent")}); err != nil {
				t.Fatal(err)
			}
			call(file, domain.Denied)
			if got := requests(); !slices.Equal(got, []string{"GET /v1/me"}) {
				t.Fatal("diagnostics issued a request before matching the enrolled identity", got)
			}
		})
	}
}

func TestDiagnosticsCLIRejectsUnexpectedArityBeforeRequests(t *testing.T) {
	a := &app{}
	for _, item := range []struct {
		name string
		run  func(context.Context, *resources, []string) (any, error)
	}{{"agent", a.agentCommand}, {"message", a.messageCommand}} {
		for _, args := range [][]string{{"diagnose"}, {"diagnose", "--input"}, {"diagnose", "--watch"}, {"diagnose", "ID", "extra"}, {"diagnose", "ID", "--input", "-"}, {"diagnose", "ID", "--watch"}, {"diagnose", "ID", "--health", "watch"}} {
			t.Run(item.name+"/"+strings.Join(args, "_"), func(t *testing.T) {
				value, err := item.run(context.Background(), &resources{}, args)
				if value != nil || err == nil || domain.AsFault(err).Code != domain.Invalid {
					t.Fatal("diagnostics accepted unexpected arguments", args, value, err)
				}
			})
		}
	}
}

func TestDiagnosticsCLIOfflineHelpAndDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, group := range []string{"agent", "message"} {
		for _, flag := range []string{"--help", "-h"} {
			envelope, code := invoke(t, "agent", []string{"--config", "missing-diagnostics-config.json", group, "diagnose", flag}, nil)
			var help struct{ Command, Usage string }
			if code != 0 || !envelope.OK || json.Unmarshal(envelope.Data, &help) != nil || help.Command != group+" diagnose" || !strings.Contains(help.Usage, group+" diagnose "+strings.ToUpper(group)+"_ID") || !strings.Contains(help.Usage, "read-only") {
				t.Fatal("offline diagnostics help is incomplete", code, help, envelope.Error)
			}
			if group == "message" && (!strings.Contains(help.Usage, "observed lifecycle (state)") || !strings.Contains(help.Usage, "phase (stage)") || !strings.Contains(help.Usage, "not proof of authoritative receipt or AI completion")) {
				t.Fatal("message diagnostics help conflates lifecycle, phase or completion evidence", help)
			}
		}
	}
	envelope, code := invoke(t, "agent", []string{"--help"}, nil)
	var help struct{ Commands []string }
	if code != 0 || json.Unmarshal(envelope.Data, &help) != nil || !slices.Contains(help.Commands, "agent diagnose") || !slices.Contains(help.Commands, "message diagnose") {
		t.Fatal("diagnostics commands missing from executable discovery", code, help)
	}
}
