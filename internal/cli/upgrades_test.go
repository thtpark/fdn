package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/upgrade"
)

type upgradeHTTPFixture func(*http.Request) (*http.Response, error)

func (f upgradeHTTPFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type upgradeOperatorFixture struct {
	calls []string
	fail  string
}

func (f *upgradeOperatorFixture) Run(_ context.Context, spec process.Spec) error {
	if spec.Path != "ssh" || !strings.Contains(strings.Join(spec.Args, " "), "StrictHostKeyChecking=yes") {
		return errors.New("unexpected native operator invocation")
	}
	data, err := io.ReadAll(spec.Stdin)
	if err != nil {
		return err
	}
	var req domain.UpgradeNativeRequest
	if err := message.UnmarshalPayload(data, &req); err != nil {
		return err
	}
	role, target := "agent", "agent_target"
	if strings.Contains(spec.Args[len(spec.Args)-1], "fdn-service") {
		role, target = "foundation", "instance_fixture"
	}
	f.calls = append(f.calls, role+":"+req.Action)
	if f.fail == role+":"+req.Action {
		return errors.New("fixture uncertain response")
	}
	reply := domain.UpgradeNativeReply{State: "prepared"}
	switch req.Action {
	case "challenge", "inspect":
		reply.Challenge = &domain.UpgradeChallenge{ID: req.ID, Nonce: domain.NewToken(), Role: role,
			InstanceID: "instance_fixture", TargetID: target, Generation: 1, CurrentVersion: "0.3.2",
			CurrentCommit: strings.Repeat("a", 40), PolicySHA256: strings.Repeat("e", 64),
			ExpiresAt: time.Now().Unix() + 900}
		reply.State = "planned"
		if req.Action == "inspect" {
			reply.State, reply.Version, reply.Commit = "observed", "0.3.2", strings.Repeat("a", 40)
		}
	case "apply", "status":
		reply.State, reply.Version, reply.Commit = "verified", "0.3.3", strings.Repeat("b", 40)
	}
	encoded, err := message.MarshalPayload(reply)
	if err != nil {
		return err
	}
	_, err = spec.Stdout.Write(encoded)
	return err
}

func upgradeCLIFixture(t *testing.T) (*app, *resources, upgrade.Catalog, string) {
	t.Helper()
	var founder string
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/upgrades/context":
			diagnosticsEnvelope(t, w, domain.UpgradeContext{InstanceID: "instance_fixture", Version: "0.3.2", Commit: strings.Repeat("a", 40),
				Agents: []domain.Agent{{ID: founder, Founder: true, Active: true, Mode: "manual", Revision: 1},
					{ID: "agent_target", Active: true, Mode: "ai", Revision: 1}}}, nil)
		case r.URL.Path == "/v1/upgrades/authorize":
			var request domain.UpgradeAuthorizationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			diagnosticsEnvelope(t, w, domain.UpgradePermit{Proposal: request.Proposal, ActorID: founder,
				ActorGeneration: 1, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 900, Signature: "fixture"}, nil)
		case strings.HasSuffix(r.URL.Path, "/mode"):
			diagnosticsEnvelope(t, w, domain.Agent{ID: "agent_target", Mode: "manual"}, nil)
		default:
			t.Error("unexpected upgrade API call", r.Method, r.URL)
			diagnosticsEnvelope(t, w, nil, domain.AsFault(domain.Fail(domain.NotFound, "fixture route not found")))
		}
	})
	a := &app{configFile: file, operation: domain.NewID("op"), in: strings.NewReader(""), out: io.Discard, diagnostic: io.Discard}
	r, err := a.load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.close() })
	founder = r.client.Credential.AgentID
	catalog := upgrade.NewCatalog()
	catalog.HTTP.Transport = upgradeHTTPFixture(func(request *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"tag_name": "v0.3.3", "target_commitish": strings.Repeat("b", 40),
			"assets": []map[string]any{{"name": "fdn-0.3.3-linux-amd64.zip",
				"browser_download_url": "https://github.com/thtpark/fdn/releases/download/v0.3.3/fdn-0.3.3-linux-amd64.zip",
				"digest":               "sha256:" + strings.Repeat("c", 64), "size": 12345, "state": "uploaded"}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	root := filepath.Dir(file)
	key, known := filepath.Join(root, "operator-key"), filepath.Join(root, "known-hosts")
	for _, path := range []string{key, known} {
		if err := os.WriteFile(path, []byte("fixture public/nonfunctional key data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	targets := upgrade.Targets{Targets: []upgrade.Target{
		{Role: "agent", TargetID: "agent_target", Platform: "linux-amd64",
			SSH:    config.SSH{Host: "127.0.0.1", Port: 2222, User: "operator", IdentityFile: key, KnownHostsFile: known},
			Helper: "/usr/bin/agent-service", PolicyFile: "/etc/fdn/updater.json", Sudo: true},
		{Role: "foundation", TargetID: "instance_fixture", Platform: "linux-amd64",
			SSH:    config.SSH{Host: "127.0.0.1", Port: 2223, User: "operator", IdentityFile: key, KnownHostsFile: known},
			Helper: "/usr/bin/fdn-service", PolicyFile: "/etc/fdn/updater.json", Sudo: true},
	}}
	targetFile := filepath.Join(root, "targets.json")
	if err := config.Write(targetFile, targets); err != nil {
		t.Fatal(err)
	}
	return a, r, catalog, targetFile
}

func TestFounderUpgradeCLIPlanApplyServerFirstAndRetainUncertainReceipts(t *testing.T) {
	for _, fail := range []string{"", "foundation:apply", "agent:apply"} {
		t.Run(fail, func(t *testing.T) {
			a, r, catalog, targets := upgradeCLIFixture(t)
			runner := &upgradeOperatorFixture{}
			planFile := filepath.Join(filepath.Dir(targets), "plan.json")
			ctx := context.Background()
			_, err := a.upgradeWith(ctx, r, []string{"plan", "--input", targets, "--output", planFile}, catalog, runner)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(runner.calls, ","), ":prepare") {
				t.Fatal("planning performed native mutations")
			}
			var plan upgrade.Plan
			if err := config.Read(planFile, &plan); err != nil || len(plan.Items) != 2 || plan.Items[0].Target.Role != "foundation" {
				t.Fatal("plan did not bind Foundation-first rollout", err)
			}
			if info, err := os.Stat(plan.RecoveryKey); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("off-host recovery key was not retained privately", err)
			}
			runner.calls, runner.fail = nil, fail
			_, err = a.upgradeWith(ctx, r, []string{"apply", "--input", planFile}, catalog, runner)
			if (err != nil) != (fail != "") {
				t.Fatal("native failure projected incorrectly", err)
			}
			got := strings.Join(runner.calls, ",")
			want := "agent:prepare,foundation:prepare,foundation:apply"
			if fail != "foundation:apply" {
				want += ",agent:apply"
			}
			if got != want {
				t.Fatal("native stop/install order is unsafe", got, want)
			}
			if err := config.Read(planFile, &plan); err != nil {
				t.Fatal(err)
			}
			if fail == "" && plan.State != "verified" || fail != "" && plan.State != "submitted" {
				t.Fatal("uncertainty erased or unverified deployment marked complete", plan.State)
			}
			count := len(runner.calls)
			_, err = a.upgradeWith(ctx, r, []string{"apply", "--input", planFile}, catalog, runner)
			if domain.AsFault(err).Code != domain.Conflict || len(runner.calls) != count {
				t.Fatal("repeated upgrade apply reran installation", err)
			}
			runner.fail = ""
			client := r.client
			r.client = nil
			_, err = a.upgradeWith(ctx, r, []string{"status", "--input", planFile}, catalog, runner)
			r.client = client
			if err != nil {
				t.Fatal("native status required a running Foundation API after uncertainty", err)
			}
		})
	}
}

func TestFounderUpgradeCheckReportsActualNativeInventoryWithoutMutations(t *testing.T) {
	a, r, catalog, targets := upgradeCLIFixture(t)
	runner := &upgradeOperatorFixture{}
	result, err := a.upgradeWith(context.Background(), r, []string{"check", "--platform", "linux-amd64", "--input", targets}, catalog, runner)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), `"upgrade_available":true`) ||
		strings.Join(runner.calls, ",") != "agent:inspect,foundation:inspect" {
		t.Fatal("upgrade inventory did not measure native targets read-only", string(raw), runner.calls, err)
	}
}
