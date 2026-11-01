package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/platform"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/upgrade"
)

func TestAgentAdditionRequiresFounderBeforeSSHOrHumanTerminal(t *testing.T) {
	_, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("non-Founder reached an addition operation")
		diagnosticsEnvelope(t, w, nil, domain.AsFault(domain.Fail(domain.Denied, "fixture denial")))
	}, false)
	var out bytes.Buffer
	code := RunAgent(context.Background(), []string{"--config", file, "--json", "agent", "add",
		"--host", "192.0.2.10", "--user", "worker", "--name", "NewAgent"}, strings.NewReader(""), &out, io.Discard)
	var envelope api.Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if code != domain.ExitCode(domain.Fail(domain.Denied, "")) || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatalf("non-Founder addition was not denied: code=%d error=%v", code, envelope.Error)
	}
	for _, request := range requests() {
		if request.path != "/v1/me" {
			t.Errorf("non-Founder reached %s", request.path)
		}
	}
}

func TestAgentAdditionSSHInputPinsAndQuoting(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "host.example", "--user", "worker", "--name", "NewAgent"},
		{"--host", "192.0.2.1", "--user", "root", "--name", "NewAgent"},
		{"--host", "192.0.2.1", "--user", "worker;id", "--name", "NewAgent"},
		{"--host", "192.0.2.1", "--user", "worker", "--name", "NewAgent", "--port", "0"},
		{"--host", "192.0.2.1", "--user", "worker", "--name", "NewAgent", "--identity-file", "relative"},
	} {
		if _, err := parseAdditionTarget(args); err == nil {
			t.Errorf("accepted invalid target %q", args)
		}
	}
	target, err := parseAdditionTarget([]string{"--host", "2001:db8::1", "--user", "existing_user", "--name", "NewAgent"})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(additionSSH(target, "/private/known_hosts", false), " ")
	for _, required := range []string{"StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes", "ControlMaster=no", "PermitLocalCommand=no", "-F /dev/null"} {
		if !strings.Contains(args, required) {
			t.Errorf("missing SSH protection %s", required)
		}
	}
	value := "a'b; $(printf unexpected) \nquoted"
	output, err := exec.Command("/bin/sh", "-c", "printf '%s' "+additionQuote(value)).Output()
	if err != nil || string(output) != value {
		t.Fatal("remote shell literal did not preserve data", err)
	}
}

type additionRunner struct {
	t            *testing.T
	public       domain.PublicIdentity
	agentID      string
	calls        []string
	fail         string
	stopped      bool
	wrongVersion bool
}

func (f *additionRunner) Run(_ context.Context, spec process.Spec) error {
	f.t.Helper()
	if spec.Path != "ssh" || !strings.Contains(strings.Join(spec.Args, " "), "StrictHostKeyChecking=yes") {
		f.t.Fatal("addition did not use pinned SSH")
	}
	command := spec.Args[len(spec.Args)-1]
	stage := ""
	var data any
	switch {
	case strings.Contains(command, "--version"):
		stage = "version"
		version := "0.3.15"
		if f.wrongVersion {
			version = "0.3.14"
		}
		data = map[string]any{"version": version, "commit": strings.Repeat("a", 40), "api_version": domain.APIVersion, "schema_version": domain.SchemaVersion}
	case strings.Contains(command, " prepare --name "):
		stage, data = "prepare", f.public
	case strings.Contains(command, " service install "):
		stage, data = "activate", map[string]string{"agent_id": f.agentID}
	case strings.Contains(command, " service status "):
		stage = "status"
		data = platform.IdentityServiceStatus{Identity: platform.IdentityUnitStatus{Registered: true, Running: true},
			Worker: platform.IdentityUnitStatus{Registered: true, Running: !f.stopped}}
	default:
		f.t.Fatal("unexpected remote addition command")
	}
	for _, forbidden := range []string{"sudo ", "useradd ", "runuser ", "provider login", "work run", "chmod 700 '/home/worker'"} {
		if strings.Contains(command, forbidden) {
			f.t.Errorf("addition performed forbidden native action %s", forbidden)
		}
	}
	f.calls = append(f.calls, stage)
	if stage == f.fail {
		return errors.New("fixture lost native acknowledgement")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.NewEncoder(spec.Stdout).Encode(api.Envelope{OK: true, Data: raw, Warnings: []string{}})
}

func additionFixture(t *testing.T) (*app, *resources, *additionRunner) {
	t.Helper()
	public, _, _, err := device.Generate("NewAgent")
	if err != nil {
		t.Fatal(err)
	}
	instance, commit, agentID := "instance_fixture", strings.Repeat("a", 40), domain.NewID("agent")
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/upgrades/context":
			diagnosticsEnvelope(t, w, domain.UpgradeContext{InstanceID: instance, Version: "0.3.15", Commit: commit}, nil)
		case "/v1/identity/register":
			var request domain.RegisterIdentityRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(request.Identity, public) || request.OperationID == "" {
				t.Error("addition changed the signed request or omitted exact replay")
			}
			diagnosticsEnvelope(t, w, domain.AgentIdentity{AgentID: agentID, InstanceID: instance, RequestID: public.RequestID,
				Generation: 1, State: "registered"}, nil)
		case "/local/profile":
			diagnosticsEnvelope(t, w, config.ConnectionProfile{}, nil)
		case "/v1/agents/" + agentID:
			diagnosticsEnvelope(t, w, domain.Agent{ID: agentID, Name: "NewAgent", Active: true, Mode: "ai"}, nil)
		default:
			t.Error("unexpected addition API call", r.URL.Path)
		}
	})
	a := &app{configFile: file, operation: domain.NewID("op")}
	resources, err := a.load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resources.close() })
	saved := agentAddition{OperationID: a.operation, FounderID: resources.client.Credential.AgentID,
		InstanceID: instance, Target: additionTarget{Host: "192.0.2.1", Port: 22, User: "worker", Name: "NewAgent"},
		Stage: "installed", Installed: true, Directory: t.TempDir(), Home: "/home/worker", UID: 1001, GID: 1001,
		Release: domain.UpgradeRelease{Version: "0.3.15", Commit: commit, Platform: "linux-amd64"}}
	if err := resources.state.Put(context.Background(), "agent-add:"+a.operation, saved); err != nil {
		t.Fatal(err)
	}
	return a, resources, &additionRunner{t: t, public: public, agentID: agentID}
}

func TestAgentAdditionSignedEnrollmentAndNativeVerification(t *testing.T) {
	a, r, runner := additionFixture(t)
	target := additionTarget{Host: "192.0.2.1", Port: 22, User: "worker", Name: "NewAgent"}
	result, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved := result.(agentAddition)
	if !saved.Installed || !saved.Enrolled || !saved.Connected || saved.Pending != "" || saved.Stage != "connected" {
		t.Fatalf("addition did not verify separate stages: %+v", saved)
	}
	if saved.Mode != "ai" || saved.Version != saved.Release.Version || saved.Commit != saved.Release.Commit {
		t.Fatal("addition did not record actual version/source and authoritative AI mode")
	}
	if !reflect.DeepEqual(runner.calls, []string{"version", "prepare", "activate", "status"}) {
		t.Fatal("unexpected native actions", runner.calls)
	}
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatal("acknowledged addition repeated native effects")
	}
	other := target
	other.Host = "192.0.2.2"
	if _, err := a.addAgentWith(context.Background(), r, other, upgrade.NewCatalog(), runner, nil, nil); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("addition operation accepted a changed target", err)
	}
}

func TestAgentAdditionUncertainNativeStageNeverReruns(t *testing.T) {
	a, r, runner := additionFixture(t)
	runner.fail = "activate"
	target := additionTarget{Host: "192.0.2.1", Port: 22, User: "worker", Name: "NewAgent"}
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); err == nil {
		t.Fatal("lost native acknowledgement was reported as success")
	}
	runner.fail = ""
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("uncertain native mutation was not fenced", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"version", "prepare", "activate"}) {
		t.Fatal("uncertain activation was repeated", runner.calls)
	}
	var saved agentAddition
	if _, err := r.state.Get(context.Background(), "agent-add:"+a.operation, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Pending != "activated" || !saved.Enrolled || !saved.Installed || saved.Connected {
		t.Fatal("uncertain enrollment evidence was lost")
	}
}

func TestAgentAdditionStoppedServicesAreNotCompletedAndInspectionDoesNotReinstall(t *testing.T) {
	a, r, runner := additionFixture(t)
	runner.stopped = true
	target := additionTarget{Host: "192.0.2.1", Port: 22, User: "worker", Name: "NewAgent"}
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("stopped services were reported as connected", err)
	}
	runner.stopped = false
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"version", "prepare", "activate", "status", "status"}) {
		t.Fatal("read-only verification repeated native installation", runner.calls)
	}

}

func TestAgentAdditionWrongInstalledSourceStopsBeforeKeyPreparation(t *testing.T) {
	a, r, runner := additionFixture(t)
	runner.wrongVersion = true
	target := additionTarget{Host: "192.0.2.1", Port: 22, User: "worker", Name: "NewAgent"}
	if _, err := a.addAgentWith(context.Background(), r, target, upgrade.NewCatalog(), runner, nil, nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("different installed source was accepted", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"version"}) {
		t.Fatal("wrong-source target prepared or registered keys", runner.calls)
	}
}

func TestAgentAdditionArchiveRejectsLinksTraversalDuplicatesAndIncompletePayloads(t *testing.T) {
	required := []string{"bin/agent-cli", "bin/agent-identity", "bin/agent-service", "bin/fdn-service", "guide/MANIFEST.json", "guide/AGENTS.md", "THIRD_PARTY_NOTICES"}
	for _, invalid := range []string{"", "link", "traversal", "duplicate", "missing"} {
		t.Run(invalid, func(t *testing.T) {
			var data bytes.Buffer
			compressed := gzip.NewWriter(&data)
			archive := tar.NewWriter(compressed)
			for index, name := range required {
				if invalid == "missing" && index == 0 {
					continue
				}
				if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755}); err != nil {
					t.Fatal(err)
				}
			}
			extra := &tar.Header{Name: "guide/extra.md", Typeflag: tar.TypeReg, Mode: 0644}
			switch invalid {
			case "link":
				extra.Typeflag, extra.Linkname = tar.TypeSymlink, "/outside"
			case "traversal":
				extra.Name = "guide/../outside.md"
			case "duplicate":
				extra.Name = "bin/agent-cli"
			}
			if err := archive.WriteHeader(extra); err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(archive.Close(), compressed.Close()); err != nil {
				t.Fatal(err)
			}
			err := validateAdditionArchive(bytes.NewReader(data.Bytes()))
			if (invalid == "") != (err == nil) {
				t.Fatal("unexpected archive validation", invalid, err)
			}
		})
	}
}

func TestAgentAdditionJSONUsesLiteralDataAndExclusiveFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "quoted'file.json")
	value := map[string]string{"value": "$(printf not-a-command)\n'quoted'"}
	script, err := additionJSON(path, value)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/sh", "-eu", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("write fixture failed: %s %v", output, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(data, &actual); err != nil || !reflect.DeepEqual(value, actual) {
		t.Fatal("JSON data was evaluated or changed", err)
	}
	if err := exec.Command("/bin/sh", "-eu", "-c", script).Run(); err == nil {
		t.Fatal("addition overwrote an existing configuration")
	}
	if _, err := additionJSON(path, make(chan int)); err == nil {
		t.Fatal("invalid configuration was not surfaced")
	}
}
