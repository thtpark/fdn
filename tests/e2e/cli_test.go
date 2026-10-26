//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

var binaries string

func TestMain(m *testing.M) {
	root, err := makeFixtureRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(root)
		os.Exit(1)
	}
	binaries = root
	build := exec.Command("go", "build", "-trimpath", "-o", root,
		"github.com/thtpark/fdn/cmd/fdn-service", "github.com/thtpark/fdn/cmd/agent-service", "github.com/thtpark/fdn/cmd/agent-cli", "github.com/thtpark/fdn/tests/fixtureprovider")
	output, err := build.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, string(output), err)
		os.RemoveAll(root)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

type child struct {
	cmd     *exec.Cmd
	done    chan error
	stderr  *lockedBuffer
	stopped bool
}

func isolatedEnvironment(args []string) []string {
	home := filepath.Join(binaries, "isolated-home")
	for index, value := range args {
		if value == "--config" && index+1 < len(args) {
			home = filepath.Join(filepath.Dir(args[index+1]), "home")
			break
		}
	}
	return append(os.Environ(), "HOME="+home)
}

func start(t *testing.T, name string, args ...string) (*child, api.Envelope) {
	t.Helper()
	cmd := exec.Command(filepath.Join(binaries, name), args...)
	cmd.Env = isolatedEnvironment(args)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := &lockedBuffer{}
	cmd.Stderr = diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, done: make(chan error, 1), stderr: diagnostic}
	go func() { c.done <- cmd.Wait() }()
	first := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		if scanner.Scan() {
			first <- append([]byte{}, scanner.Bytes()...)
		} else {
			first <- nil
		}
		for scanner.Scan() {
		}
	}()
	t.Cleanup(func() { c.stop(t) })
	select {
	case line := <-first:
		var envelope api.Envelope
		if err := json.Unmarshal(line, &envelope); err != nil || !envelope.OK {
			t.Fatalf("%s startup failed: %s %s", name, line, diagnostic.String())
		}
		return c, envelope
	case <-time.After(15 * time.Second):
		t.Fatalf("%s startup timeout: %s", name, diagnostic.String())
	}
	return nil, api.Envelope{}
}

func (c *child) stop(t *testing.T) {
	t.Helper()
	if c.stopped {
		return
	}
	c.stopped = true
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error(err)
	}
	select {
	case <-c.done:
	case <-time.After(12 * time.Second):
		if err := c.cmd.Process.Signal(syscall.SIGQUIT); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
			<-c.done
		}
		t.Errorf("owned test process required forced shutdown; diagnostic stack:\n%s", c.stderr.String())
	}
}

func (c *child) crash(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-c.done
	c.stopped = true
}

func run(t *testing.T, name string, args []string, input any) (api.Envelope, int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binaries, name), args...)
	cmd.Env = isolatedEnvironment(args)
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = bytes.NewReader(data)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	var envelope api.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid CLI envelope: %s %s", stdout.String(), stderr.String())
	}
	return envelope, code, stderr.String()
}

func must[T any](t *testing.T, name string, args []string, input any) T {
	t.Helper()
	envelope, code, diagnostic := run(t, name, args, input)
	if code != 0 || !envelope.OK {
		t.Fatalf("%s %v failed: code=%d error=%v diagnostic=%s", name, args, code, envelope.Error, diagnostic)
	}
	var value T
	if err := json.Unmarshal(envelope.Data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type worker struct {
	configFile   string
	config       config.Worker
	agent        domain.Agent
	client       *api.Client
	fixtureToken string
}
type lab struct {
	root         string
	url          string
	fixture      *workflowAPI
	serverConfig string
	founder      *worker
	worker       *worker
	project      domain.Project
	workspace    string
	binding      local.Binding
}

func (l *lab) cli(t *testing.T, w *worker, args []string, input any) json.RawMessage {
	t.Helper()
	full := append([]string{"--config", w.configFile, "--json"}, args...)
	return must[json.RawMessage](t, "agent-cli", full, input)
}

func (l *lab) change(t *testing.T, w *worker, id string, req domain.TaskChange) domain.Task {
	t.Helper()
	data := l.cli(t, w, []string{"task", "change", id, "--input", "-"}, req)
	var task domain.Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func newLab(t *testing.T, provider string) *lab {
	t.Helper()
	root := fixtureRoot(t)
	l := &lab{root: root, serverConfig: filepath.Join(root, "foundation-config", "server.json")}
	state := filepath.Join(root, "foundation")
	l.fixture = newWorkflowAPI(t, state)
	l.url = l.fixture.server.URL
	if err := config.Write(l.serverConfig, config.Server{StateDir: state,
		CertFile: filepath.Join(state, "server.pem"), KeyFile: filepath.Join(state, "server-key.pem"),
		IngressSocket: filepath.Join(root, "api", "api.sock"), JoinSocket: filepath.Join(root, "join", "join.sock"), IngressGroup: 4100,
		AccountSocket: filepath.Join(root, "accounts", "helper.sock")}); err != nil {
		t.Fatal(err)
	}
	l.founder = l.newWorker(t, "founder", provider)
	mode := must[map[string]string](t, "agent-cli", []string{"--config", l.founder.configFile, "mode", "show"}, nil)
	if mode["mode"] != "manual" {
		t.Fatal("Founder bootstrap did not default to manual")
	}
	l.worker = l.newWorker(t, "worker", provider)
	raw := l.cli(t, l.founder, []string{"project", "create", "--input", "-"}, domain.ProjectRequest{Name: "native e2e"})
	if err := json.Unmarshal(raw, &l.project); err != nil {
		t.Fatal(err)
	}
	l.workspace = filepath.Join(root, "workspaces", "explicit")
	if err := os.MkdirAll(l.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	raw = l.cli(t, l.worker, []string{"workspace", "bind", "--path", l.workspace, "--allow-tools", "--timeout", "30"}, nil)
	if err := json.Unmarshal(raw, &l.binding); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *lab) newWorker(t *testing.T, name, providerName string) *worker {
	t.Helper()
	w := &worker{configFile: filepath.Join(l.root, name, "config", "agent.json")}
	founderID := ""
	if l.founder != nil {
		founderID = l.founder.agent.ID
	}
	w.agent, w.fixtureToken = l.fixture.seed(t, founderID, name)
	socket := filepath.Join(l.root, name, "identity.sock")
	l.fixture.localProxy(t, socket, w.agent, w.fixtureToken)
	state := filepath.Join(l.root, name, "state")
	args := []string{"--config", w.configFile, "config", "init", "--state", state, "--identity-socket", socket,
		"--provider", providerName, "--workspace-root", filepath.Join(l.root, "workspaces", name), "--allow-workspace-tools"}
	must[any](t, "agent-cli", args, nil)
	l.cli(t, w, []string{"config", "set", "--input", "-"}, map[string]any{"poll_seconds": 1, "invocation_timeout_seconds": 30, "automatic_provider_updates": false,
		"provider_paths": map[string]string{"codex": filepath.Join(binaries, "fixtureprovider"), "copilot": filepath.Join(binaries, "fixtureprovider")}})
	connected := must[domain.Agent](t, "agent-cli", []string{"--config", w.configFile, "connect"}, nil)
	if connected.ID != w.agent.ID {
		t.Fatal("local identity attach changed the seeded actor")
	}
	l.cli(t, w, []string{"preferences", "set", "--input", "-"}, map[string]string{"provider": providerName, "model": "fixture-model"})
	var err error
	w.config, err = config.LoadWorker(w.configFile)
	if err != nil {
		t.Fatal(err)
	}
	var credential config.Credential
	if err := config.Read(w.config.CredentialFile, &credential); err != nil {
		t.Fatal(err)
	}
	if credential.AgentID != w.agent.ID || credential.Token != "" {
		t.Fatal("worker credential must contain only its local identity binding")
	}
	w.client, err = api.NewLocalClient(w.config)
	if err != nil {
		t.Fatal(err)
	}
	w.client.Credential = credential
	t.Cleanup(w.client.Close)
	return w
}

func (l *lab) approved(t *testing.T, title string) domain.Task {
	t.Helper()
	raw := l.cli(t, l.worker, []string{"task", "create", "--input", "-"}, domain.TaskRequest{ProjectID: l.project.ID, Title: title, Description: "Write result.txt containing the exact fixture result. Work only in the approved workspace.", AcceptanceCriteria: "result.txt contains verified fixture result and a trailing newline", Priority: 4, Importance: 3})
	var task domain.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "assign", Reason: "assign exact provider/model", AgentID: l.worker.agent.ID, Provider: l.worker.config.Provider, Model: "fixture-model"})
	task = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "retry-policy", Reason: "bounded local test retry policy", RetryPolicy: &domain.RetryPolicy{MaxRetries: 1, BackoffSeconds: 1}})
	task = l.change(t, l.worker, task.ID, domain.TaskChange{Kind: "workspace", Reason: "explicit local binding", WorkspaceRef: l.binding.Ref})
	task = l.change(t, l.worker, task.ID, domain.TaskChange{Kind: "status", Reason: "ready for preflight", Status: domain.Todo})
	return l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "approve", Reason: "authorize exact current specification", SpecGeneration: task.SpecGeneration})
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for " + description)
}

func (l *lab) task(t *testing.T, id string) domain.Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, err := api.Call[domain.Task](ctx, l.worker.client, "GET", api.Path("tasks", id), nil)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (l *lab) startAgent(t *testing.T) *child {
	t.Helper()
	agent, _ := start(t, "agent-service", "--config", l.worker.configFile, "serve")
	waitFor(t, 5*time.Second, func() bool {
		value := must[struct {
			Running bool `json:"daemon_running"`
		}](t, "agent-cli", []string{"--config", l.worker.configFile, "service", "status"}, nil)
		return value.Running
	}, "native daemon singleton/health")
	return agent
}

func TestNativeBinariesPhaseOneThroughFive(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t, name)
			l.cli(t, l.worker, []string{"knowledge", "put", "--input", "-"}, local.KnowledgeChange{Kind: "memory", Name: "lesson", Content: "PRIVATE_TEST_NOTE_NOT_PUBLISHED", Selected: true})
			l.cli(t, l.worker, []string{"inventory", "refresh"}, nil)
			task := l.approved(t, "success")
			agent := l.startAgent(t)
			waitFor(t, 15*time.Second, func() bool { return l.task(t, task.ID).Status == domain.Completed }, "automatic provider execution")
			current := l.task(t, task.ID)
			if current.Status == domain.Done || current.ResultGeneration != 1 {
				t.Fatal("provider success bypassed verification")
			}
			result, err := os.ReadFile(filepath.Join(l.workspace, "result.txt"))
			if err != nil || string(result) != "verified fixture result\n" {
				t.Fatal("actual workspace edit failed", err)
			}
			count, err := os.ReadFile(filepath.Join(l.workspace, "invocations.count"))
			if err != nil || string(count) != name+"\n" {
				t.Fatal("duplicate/wrong coding provider", string(count), err)
			}
			runs := must[[]domain.Run](t, "agent-cli", []string{"--config", l.worker.configFile, "task", "runs", task.ID}, nil)
			if len(runs) != 1 || runs[0].Provider != name || runs[0].Model != "fixture-model" {
				t.Fatal("run attribution/model was lost")
			}
			commentRaw := l.cli(t, l.worker, []string{"comment", "add", task.ID, "--input", "-"}, domain.EntryRequest{Body: "Please verify @{" + l.founder.agent.Name + "}."})
			var entry domain.Entry
			if err := json.Unmarshal(commentRaw, &entry); err != nil || len(entry.Mentions) != 1 || entry.Mentions[0] != l.founder.agent.ID {
				t.Fatal("name mention did not resolve to the recipient ID", entry.Mentions, err)
			}
			l.cli(t, l.founder, []string{"mention", "acknowledge", entry.ID, "--input", "-"}, domain.AttentionRequest{Generation: entry.Generation, State: "handled"})
			current = l.task(t, task.ID)
			current = l.change(t, l.founder, task.ID, domain.TaskChange{Kind: "verify", Reason: "checked actual fixture bytes", Decision: "accept", SpecGeneration: current.SpecGeneration, ResultGeneration: current.ResultGeneration})
			if current.Status != domain.Done {
				t.Fatal("verification did not reach done")
			}
			l.cli(t, l.founder, []string{"watch", "--once", "--task", task.ID}, nil)
			shared, _ := json.Marshal(current)
			if strings.Contains(string(shared), "PRIVATE_TEST_NOTE_NOT_PUBLISHED") || strings.Contains(string(shared), l.workspace) {
				t.Fatal("private knowledge/path published")
			}
			agent.stop(t)
			state, err := local.Open(context.Background(), l.worker.config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			journals, err := state.Journals(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			reviews := 0
			for _, journal := range journals {
				if journal.Kind == "onboarding" {
					reviews++
				}
				if strings.Contains(string(journal.Context), l.worker.fixtureToken) {
					t.Fatal("credential entered model context")
				}
				prompt, err := os.ReadFile(filepath.Join(l.worker.config.StateDir, "invocations", journal.ID, "prompt.txt"))
				if err != nil || !bytes.Contains(prompt, []byte("FDN_OPERATING_GUIDANCE")) || !bytes.Contains(prompt, []byte("name: foundation-work")) {
					t.Fatal("actual provider invocation did not receive the operating skill", journal.Kind, err)
				}
				if journal.PromptHash != domain.Hash(string(prompt)) {
					t.Fatal("private prompt hash did not bind the delivered operational guidance")
				}
			}
			if reviews != 1 {
				t.Fatal("initial review not serialized once", reviews)
			}
			catalog, err := guidance.Catalog()
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(filepath.Dir(l.worker.configFile), "home")
			for _, providerName := range []string{"codex", "copilot"} {
				command := exec.Command(filepath.Join(home, ".local", "bin", providerName), "--version")
				command.Env = isolatedEnvironment([]string{"--config", l.worker.configFile})
				if output, err := command.CombinedOutput(); err != nil || string(output) != "fixture-cli 1.2.3\n" {
					t.Fatal("worker startup did not repair the bare managed provider command", providerName, string(output), err)
				}
			}
			for _, skillRoot := range []string{".agents", ".copilot"} {
				for _, skill := range catalog.Skills {
					data, err := os.ReadFile(filepath.Join(home, skillRoot, "skills", skill, "SKILL.md"))
					if err != nil || !bytes.Contains(data, []byte("name: "+skill+"\n")) {
						t.Fatal("native worker is missing a discoverable skill", skillRoot, skill, err)
					}
				}
			}
			knowledge, err := state.Knowledge(context.Background(), "memory:lesson")
			if err != nil || knowledge.Content != "PRIVATE_TEST_NOTE_NOT_PUBLISHED" {
				t.Fatal("private state lost", err)
			}
			state.Close()
			restarted := l.startAgent(t)
			time.Sleep(1200 * time.Millisecond)
			restarted.stop(t)
			count, err = os.ReadFile(filepath.Join(l.workspace, "invocations.count"))
			if err != nil || string(count) != name+"\n" {
				t.Fatal("restart duplicated execution")
			}
			must[any](t, "fdn-service", []string{"--config", l.serverConfig, "doctor"}, nil)
		})
	}
}

func TestNativeCancellationAndCrashRecovery(t *testing.T) {
	l := newLab(t, "codex")
	task := l.approved(t, "slow")
	agent := l.startAgent(t)
	started := filepath.Join(l.workspace, "started")
	waitFor(t, 12*time.Second, func() bool { _, err := os.Stat(started); return err == nil }, "owned provider process")
	l.cli(t, l.founder, []string{"agent", "mode", l.worker.agent.ID, "--input", "-"}, domain.ModeRequest{Mode: "manual", Reason: "Founder requests bounded test maintenance"})
	waitFor(t, 6*time.Second, func() bool {
		current := l.task(t, task.ID)
		return current.ActiveRun == nil && current.Status == domain.Paused
	}, "manual mode cancellation and report")
	current := l.task(t, task.ID)
	if !current.UnsafeRetry || current.Failures != 0 {
		t.Fatal("intentional stop should require side-effect review without consuming a failure retry")
	}
	agent.stop(t)
	if err := os.Remove(started); err != nil {
		t.Fatal(err)
	}
	crashTask := l.approved(t, "crash")
	l.cli(t, l.founder, []string{"agent", "mode", l.worker.agent.ID, "--input", "-"}, domain.ModeRequest{Mode: "ai", Reason: "Founder explicitly resumes worker"})
	agent = l.startAgent(t)
	waitFor(t, 12*time.Second, func() bool { _, err := os.Stat(started); return err == nil }, "crash fixture process")
	pidBytes, err := os.ReadFile(started)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	agent.crash(t)
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	agent = l.startAgent(t)
	time.Sleep(1200 * time.Millisecond)
	current = l.task(t, crashTask.ID)
	if current.ActiveRun == nil || current.Status != domain.Running {
		t.Fatal("restart invented a replacement execution")
	}
	state, err := local.Open(context.Background(), l.worker.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	journals, err := state.Journals(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	state.Close()
	var interrupted string
	for _, journal := range journals {
		if journal.TaskID == crashTask.ID && journal.State == "invoking" {
			interrupted = journal.ID
		}
	}
	if interrupted == "" {
		t.Fatal("durable invocation fence missing")
	}
	args := []string{"--config", l.worker.configFile, "work", "recover", "--journal", interrupted, "--acknowledge-stopped"}
	waitFor(t, 5*time.Second, func() bool {
		envelope, code, _ := run(t, "agent-cli", args, nil)
		if code == 5 {
			return false
		}
		if code != 0 || !envelope.OK {
			t.Fatal("explicit stopped-process recovery failed", envelope.Error)
		}
		return true
	}, "exact crash recovery")
	current = l.task(t, crashTask.ID)
	if current.ActiveRun != nil || !current.UnsafeRetry || current.Status != domain.Paused {
		t.Fatal("unsafe crash retry was not fenced")
	}
	agent.stop(t)
}
