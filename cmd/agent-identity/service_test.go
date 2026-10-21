package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/platform"
)

type recordedIdentityLifecycle struct {
	installs int
	statuses int
	apply    bool
	err      error
}

func (r *recordedIdentityLifecycle) Install(_ context.Context, apply bool) (platform.IdentityServiceStatus, error) {
	r.installs++
	r.apply = apply
	return platform.IdentityServiceStatus{Name: "agent1", Identity: platform.IdentityUnitStatus{Registered: true, Running: true}, Worker: platform.IdentityUnitStatus{Registered: true, Running: true}}, r.err
}
func (r *recordedIdentityLifecycle) Status(context.Context) (platform.IdentityServiceStatus, error) {
	r.statuses++
	return platform.IdentityServiceStatus{Name: "agent1"}, r.err
}

func serviceOptionsFile(t *testing.T) (string, platform.IdentityServiceOptions) {
	t.Helper()
	root, err := os.MkdirTemp(filepath.Join("..", ".."), ".identity-lifecycle-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	o := platform.IdentityServiceOptions{OS: "linux", Name: "agent1", IdentityUser: "fdni_agent1", IdentityUID: 1101, IdentityGID: 1101,
		WorkerUser: "fdnw_agent1", WorkerUID: 1102, WorkerGID: 1102, IdentityBinary: "/opt/fdn/agent-identity", WorkerBinary: "/opt/fdn/agent-service",
		IdentityConfig: "/var/lib/fdn-identity/agent1/identity.json", WorkerConfig: "/home/fdnw_agent1/.config/fdn/agent.json",
		StateDir: "/var/lib/fdn-identity/agent1/state", WorkerStateDir: "/home/fdnw_agent1/.local/state/fdn", IdentityHome: "/var/lib/fdn-identity/agent1", WorkerHome: "/home/fdnw_agent1",
		SocketDir: "/run/fdn-agent1", IdentityDefinition: "/etc/systemd/system/fdn-identity-agent1.service", WorkerDefinition: "/home/fdnw_agent1/.config/systemd/user/fdn-worker-agent1.service"}
	file := filepath.Join(root, "options.json")
	if err := config.Write(file, o); err != nil {
		t.Fatal(err)
	}
	return file, o
}

func TestIdentityServiceLifecycleCLIPlansWithoutPrivateConfigAndAppliesOnlyExplicitly(t *testing.T) {
	file, options := serviceOptionsFile(t)
	var output, stderr bytes.Buffer
	called := 0
	lifecycle := &recordedIdentityLifecycle{}
	factory := func(o platform.IdentityServiceOptions) (serviceLifecycle, error) {
		called++
		if o != options {
			t.Fatal("native lifecycle did not receive the exact approved options")
		}
		return lifecycle, nil
	}
	if code := runWithServiceFactory(context.Background(), []string{"service", "plan", "--input", file}, &output, &stderr, factory); code != 0 || called != 0 {
		t.Fatal("read-only planning accessed native lifecycle or required enrollment", code)
	}
	var envelope api.Envelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatal(err)
	}
	var plan platform.IdentityServicePlan
	if err := json.Unmarshal(envelope.Data, &plan); err != nil || plan.Accounts[0].Marker != "fdn-identity=agent1" || plan.Accounts[1].UID != options.WorkerUID {
		t.Fatal("plan omitted exact account prerequisites", err)
	}
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "install", "--input", file}, &output, &stderr, factory); code != 4 || called != 0 {
		t.Fatal("implicit installation crossed the --apply boundary", code)
	}
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "install", "--input", file, "--apply"}, &output, &stderr, factory); code != 0 || called != 1 || lifecycle.installs != 1 || !lifecycle.apply {
		t.Fatal("explicit lifecycle install was not wired", code)
	}
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "status", "--input", file}, &output, &stderr, factory); code != 0 || lifecycle.statuses != 1 {
		t.Fatal("native service status was not wired", code)
	}
	lifecycle.err = domain.Fail(domain.Prerequisite, "dedicated account missing; follow service plan")
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "install", "--input", file, "--apply"}, &output, &stderr, factory); code != 6 {
		t.Fatal("missing dedicated account looked like successful installation", code)
	}
}

func TestIdentityServiceLifecycleCLIRejectsUnknownOptionsFlagsAndUIDBypasses(t *testing.T) {
	file, options := serviceOptionsFile(t)
	var output, stderr bytes.Buffer
	called := false
	factory := func(platform.IdentityServiceOptions) (serviceLifecycle, error) {
		called = true
		return &recordedIdentityLifecycle{}, nil
	}
	for _, args := range [][]string{
		{"service", "plan", "--input", file, "--apply"},
		{"service", "install", "--input", file, "--apply", "--test-uid", "0"},
		{"service", "status", "--input", file, "extra"},
		{"--config", "/private/config", "service", "install", "--input", file, "--apply"},
		{"service", "execute", "--input", file},
	} {
		output.Reset()
		if code := runWithServiceFactory(context.Background(), args, &output, &stderr, factory); code != 2 || called {
			t.Fatal("native lifecycle accepted unsupported flags, arbitrary commands or account bypass", args, code)
		}
	}
	options.WorkerUID = options.IdentityUID
	if err := config.Write(file, options); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "install", "--input", file, "--apply"}, &output, &stderr, factory); code != 2 || called {
		t.Fatal("same-UID native service options were accepted", code)
	}
	if err := os.WriteFile(file, []byte(`{"os":"linux","test_uid":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runWithServiceFactory(context.Background(), []string{"service", "plan", "--input", file}, &output, &stderr, factory); code != 2 || called {
		t.Fatal("native lifecycle accepted unknown protected options", code)
	}
	fifo := filepath.Join(filepath.Dir(file), "not-json.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readServiceOptions(fifo); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native lifecycle accepted or blocked waiting on a nonregular options file", err)
	}
}
