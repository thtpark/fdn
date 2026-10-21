// Package scenario supplies private, deterministic test-provider scripts and GitHub fixtures.
// It is not a production provider or a live-provider qualification harness.
package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

const PlanDirectory = ".fdn-fixture"

// Plans are opt-in files in the provider's isolated HOME; no environment credential is used.
type Plan struct {
	ActorID   string            `json:"actor_id"`
	Provider  string            `json:"provider"`
	Model     string            `json:"model"`
	Models    []string          `json:"models,omitempty"`
	APIURL    string            `json:"api_url,omitempty"`
	APIToken  string            `json:"api_token,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	Rules     []Rule            `json:"rules"`
}

type Rule struct {
	Name                string   `json:"name"`
	Phases              []string `json:"phases"`
	Call                int      `json:"call,omitempty"`
	Waiting             *bool    `json:"waiting,omitempty"`
	DescriptionIncludes []string `json:"description_includes,omitempty"`
	DescriptionExcludes []string `json:"description_excludes,omitempty"`
	MaxCalls            int      `json:"max_calls,omitempty"`
	Actions             []Action `json:"actions,omitempty"`
	Reply               string   `json:"reply"`
}

// Action deliberately has no shell escape: only bounded local git, relative files,
// assertions, and an authenticated literal-loopback HTTP fixture are available.
type Action struct {
	Kind     string   `json:"kind"`
	Path     string   `json:"path,omitempty"`
	Text     string   `json:"text,omitempty"`
	Equal    *string  `json:"equal,omitempty"`
	Contains []string `json:"contains,omitempty"`
	Args     []string `json:"args,omitempty"`
	Method   string   `json:"method,omitempty"`
	Body     string   `json:"body,omitempty"`
	Store    string   `json:"store,omitempty"`
}

type Call struct {
	Phase        string          `json:"phase"`
	Rule         string          `json:"rule"`
	ActorID      string          `json:"actor_id"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Workspace    string          `json:"workspace"`
	Coding       bool            `json:"coding"`
	Task         *domain.Task    `json:"task,omitempty"`
	ProjectEpoch int64           `json:"project_epoch,omitempty"`
	Discussion   []domain.Entry  `json:"discussion"`
	Reply        json.RawMessage `json:"reply,omitempty"`
	Completed    bool            `json:"completed"`
	Action       int             `json:"action,omitempty"`
	ActionKind   string          `json:"action_kind,omitempty"`
}

type Trace struct {
	Calls []Call `json:"calls"`
}

type scriptContext struct {
	work.Launch
	Plan       Plan
	Answers    []domain.Entry
	AnswerIDs  []string
	AnswerText string
	Vars       map[string]any
}

func WritePlan(home string, plan Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(data) > 512<<10 {
		return errors.New("fixture plan exceeds its private size budget")
	}
	return secure.Create(filepath.Join(home, PlanDirectory, "plan.json"), data)
}

func ReadPlan(home string) (Plan, bool, error) {
	var plan Plan
	data, err := secure.Read(filepath.Join(home, PlanDirectory, "plan.json"), 512<<10)
	if errors.Is(err, os.ErrNotExist) {
		return plan, false, nil
	}
	if err != nil {
		return plan, true, err
	}
	return plan, true, config.Decode(data, &plan)
}

func ReadTrace(home string) (Trace, error) {
	trace := Trace{Calls: []Call{}}
	data, err := secure.Read(filepath.Join(home, PlanDirectory, "trace.json"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return trace, nil
	}
	if err == nil {
		err = config.Decode(data, &trace)
	}
	return trace, err
}

func saveTrace(home string, trace Trace) error {
	data, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("fixture trace exceeds its private size budget")
	}
	return secure.Write(filepath.Join(home, PlanDirectory, "trace.json"), data)
}

func RunScript(home string, launch work.Launch, args []string) ([]byte, bool, error) {
	plan, present, err := ReadPlan(home)
	if !present || err != nil {
		return nil, present, err
	}
	lock, err := secure.Acquire(filepath.Join(home, PlanDirectory, "script.lock"))
	if err != nil {
		return nil, true, err
	}
	defer lock.Close()
	trace, err := ReadTrace(home)
	if err != nil {
		return nil, true, err
	}
	if len(trace.Calls) >= 64 || len(plan.Rules) > 64 {
		return nil, true, errors.New("fixture invocation/rule budget exceeded")
	}
	name, coding := "copilot", slices.Contains(args, "--allow-all")
	if slices.Contains(args, "exec") {
		name, coding = "codex", slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	model := ""
	if index := slices.Index(args, "--model"); index >= 0 && index+1 < len(args) {
		model = args[index+1]
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, true, err
	}
	call := Call{Phase: launch.Phase, ActorID: launch.AgentID, Provider: name, Model: model, Workspace: cwd, Coding: coding, Task: launch.Task, Discussion: launch.Discussion}
	if launch.Project != nil {
		call.ProjectEpoch = launch.Project.Epoch
	}
	trace.Calls = append(trace.Calls, call)
	if err := saveTrace(home, trace); err != nil {
		return nil, true, err
	}
	if plan.ActorID != launch.AgentID || plan.Provider != name || plan.Model != model {
		return nil, true, errors.New("scripted invocation used another actor/provider/model")
	}
	if coding != (launch.Phase == "execution" || launch.Phase == "verification") {
		return nil, true, errors.New("scripted invocation violated text/tool isolation")
	}
	phaseCall := 0
	for _, previous := range trace.Calls {
		if previous.Phase == launch.Phase {
			phaseCall++
		}
	}
	for _, rule := range plan.Rules {
		if !matches(rule, launch, phaseCall) {
			continue
		}
		trace.Calls[len(trace.Calls)-1].Rule = rule.Name
		if rule.MaxCalls > 0 {
			count := 0
			for _, previous := range trace.Calls {
				if previous.Rule == rule.Name {
					count++
				}
			}
			if count > rule.MaxCalls {
				return nil, true, errors.New("fixture rule was reinvoked beyond its side-effect budget")
			}
		}
		if err := saveTrace(home, trace); err != nil {
			return nil, true, err
		}
		ctx := scriptContext{Launch: launch, Plan: plan, AnswerIDs: []string{}, Vars: map[string]any{}}
		if launch.Task != nil && launch.Task.Waiting != nil {
			for _, entry := range launch.Discussion {
				if entry.Kind == "comment" && slices.Contains(launch.Task.Waiting.QuestionIDs, entry.ParentID) && slices.Contains(launch.Task.Waiting.RequestedIDs, entry.AuthorID) {
					ctx.Answers = append(ctx.Answers, entry)
					ctx.AnswerIDs = append(ctx.AnswerIDs, entry.ID)
					if ctx.AnswerText != "" {
						ctx.AnswerText += "\n"
					}
					ctx.AnswerText += entry.Body
				}
			}
		}
		for index, action := range rule.Actions {
			trace.Calls[len(trace.Calls)-1].Action, trace.Calls[len(trace.Calls)-1].ActionKind = index+1, action.Kind
			if err := saveTrace(home, trace); err != nil {
				return nil, true, err
			}
			if err := runAction(ctx, action, coding, cwd); err != nil {
				return nil, true, fmt.Errorf("fixture rule %s: %w", rule.Name, err)
			}
		}
		reply, err := render(string(rule.Reply), ctx)
		if err != nil || !json.Valid([]byte(reply)) {
			return nil, true, errors.New("fixture reply template did not produce JSON")
		}
		trace.Calls[len(trace.Calls)-1].Reply = json.RawMessage(reply)
		trace.Calls[len(trace.Calls)-1].Completed = true
		return []byte(reply), true, saveTrace(home, trace)
	}
	return nil, true, fmt.Errorf("no scripted rule matched phase %s call %d", launch.Phase, phaseCall)
}

func matches(rule Rule, launch work.Launch, call int) bool {
	if !slices.Contains(rule.Phases, launch.Phase) || (rule.Call != 0 && rule.Call != call) {
		return false
	}
	waiting := launch.Task != nil && launch.Task.Waiting != nil
	if rule.Waiting != nil && *rule.Waiting != waiting {
		return false
	}
	description := ""
	if launch.Task != nil {
		description = launch.Task.Description
	}
	for _, text := range rule.DescriptionIncludes {
		if !strings.Contains(description, text) {
			return false
		}
	}
	for _, text := range rule.DescriptionExcludes {
		if strings.Contains(description, text) {
			return false
		}
	}
	return true
}

func render(source string, ctx scriptContext) (string, error) {
	functions := template.FuncMap{"json": func(value any) (string, error) { data, err := json.Marshal(value); return string(data), err }}
	tmpl, err := template.New("fixture").Option("missingkey=error").Funcs(functions).Parse(source)
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, ctx); err != nil {
		return "", err
	}
	if output.Len() > 256<<10 {
		return "", errors.New("fixture template exceeds its output budget")
	}
	return output.String(), nil
}

func runAction(ctx scriptContext, action Action, coding bool, cwd string) error {
	text, err := render(action.Text, ctx)
	if err != nil {
		return err
	}
	switch action.Kind {
	case "assert":
		if action.Equal != nil {
			expected, err := render(*action.Equal, ctx)
			if err != nil || text != expected {
				return errors.New("fixture assertion failed")
			}
		}
		for _, part := range action.Contains {
			expected, err := render(part, ctx)
			if err != nil || !strings.Contains(text, expected) {
				return errors.New("fixture content assertion failed")
			}
		}
		return nil
	case "git", "write", "file", "http":
		if !coding {
			return errors.New("text-only fixture attempted a tool action")
		}
	default:
		return errors.New("unsupported fixture action")
	}
	var result any
	switch action.Kind {
	case "git":
		args := make([]string, len(action.Args))
		for index, arg := range action.Args {
			args[index], err = render(arg, ctx)
			if err != nil {
				return err
			}
		}
		result, err = Git(context.Background(), cwd, args...)
	case "write", "file":
		path, pathErr := relativeFile(cwd, action.Path)
		if pathErr != nil {
			return pathErr
		}
		if action.Kind == "write" {
			if err = os.MkdirAll(filepath.Dir(path), 0700); err == nil {
				err = os.WriteFile(path, []byte(text), 0600)
			}
		} else {
			var data []byte
			data, err = os.ReadFile(path)
			if err == nil && string(data) != text {
				err = errors.New("fixture file content differs from the reviewed specification")
			}
		}
	case "http":
		result, err = fixtureRequest(ctx, action)
	}
	if err == nil && action.Store != "" {
		ctx.Vars[action.Store] = result
	}
	return err
}

func relativeFile(cwd, path string) (string, error) {
	clean := filepath.Clean(path)
	if path == "" || filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Split(clean, string(filepath.Separator))[0] == ".git" {
		return "", errors.New("fixture file must be relative to its owned workspace")
	}
	current := cwd
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("fixture file must not traverse a symlink")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return current, nil
}

// Git forbids network transports, hooks, inherited Git configuration and credential helpers.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--config") || strings.HasPrefix(arg, "--exec-path") || strings.HasPrefix(arg, "--upload-pack") || strings.HasPrefix(arg, "--receive-pack") || arg == "--global" || arg == "--system" {
			return "", errors.New("isolated git configuration/tool overrides are forbidden")
		}
	}
	path, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, diagnostic := process.NewBuffer(128<<10), process.NewBuffer(32<<10)
	options := []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.Getenv("TMPDIR"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Foundation fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Foundation fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	// Inherit the provider's owned process group so killing it also stops nested Git.
	command := exec.CommandContext(ctx, path, append(options, args...)...)
	command.Dir, command.Env, command.Stdout, command.Stderr = dir, env, output, diagnostic
	command.WaitDelay = 2 * time.Second
	runErr := command.Run()
	data, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	if err := errors.Join(runErr, outputErr, diagnosticErr); err != nil {
		return "", fmt.Errorf("isolated git failed: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func fixtureRequest(ctx scriptContext, action Action) (any, error) {
	base, err := url.Parse(ctx.Plan.APIURL)
	if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" || !net.ParseIP(base.Hostname()).IsLoopback() || base.Port() == "" {
		return nil, errors.New("scripted HTTP requires an explicit literal loopback fixture")
	}
	path, err := render(action.Path, ctx)
	if err != nil || !strings.HasPrefix(path, "/repos/") || strings.Contains(path, "://") {
		return nil, errors.New("scripted HTTP requires a repository fixture path")
	}
	body, err := render(action.Body, ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(action.Method, strings.TrimRight(ctx.Plan.APIURL, "/")+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+ctx.Plan.APIToken)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("fixture HTTP reply exceeds its budget")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fixture HTTP rejected operation: status %d", response.StatusCode)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}
