package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type Question struct {
	Body          string   `json:"body"`
	RespondentIDs []string `json:"respondent_ids"`
}

type ConversationAction struct {
	Title              string `json:"title,omitempty"`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Instructions       string `json:"instructions"`
	WorkspaceRef       string `json:"workspace_ref,omitempty"`
	TaskID             string `json:"task_id,omitempty"`
}

type PublicReport struct {
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

func (r PublicReport) Validate() error {
	if err := domain.CheckText(r.Summary, "public task report summary", 32768, true); err != nil {
		return err
	}
	if len(r.Evidence) > 64 {
		return domain.Fail(domain.Provider, "public task report exceeds its evidence budget")
	}
	for _, evidence := range r.Evidence {
		if err := domain.CheckText(evidence, "public task report evidence", 8192, true); err != nil {
			return err
		}
	}
	return nil
}

type PreferenceChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Reason   string `json:"reason"`
}

type Reply struct {
	Outcome            string              `json:"outcome"`
	Decision           string              `json:"decision,omitempty"`
	Summary            string              `json:"summary"`
	Evidence           []string            `json:"evidence"`
	Questions          []Question          `json:"questions"`
	Description        *string             `json:"description,omitempty"`
	AcceptanceCriteria *string             `json:"acceptance_criteria,omitempty"`
	AnswerIDs          []string            `json:"answer_ids"`
	SafeToRetry        bool                `json:"safe_to_retry"`
	Checkpoint         string              `json:"checkpoint,omitempty"`
	MessageRead        bool                `json:"message_read,omitempty"`
	RequestDecision    string              `json:"request_decision,omitempty"`
	ResponseRequested  *bool               `json:"response_requested,omitempty"`
	Action             *ConversationAction `json:"action,omitempty"`
	Recovery           *RecoveryResult     `json:"recovery,omitempty"`
	PublicReport       *PublicReport       `json:"public_report,omitempty"`
	Preferences        *PreferenceChoice   `json:"preferred_model,omitempty"`
}

func (r Reply) RequestsResponse() bool {
	return r.ResponseRequested == nil || *r.ResponseRequested
}

func (r Reply) Validate() error {
	if r.Preferences != nil {
		if r.Preferences.Provider == "" {
			return domain.Fail(domain.Provider, "an agent-selected preference requires an explicit authenticated provider")
		}
		if err := domain.ValidateProviderModel(r.Preferences.Provider, r.Preferences.Model); err != nil {
			return err
		}
		if err := domain.CheckText(r.Preferences.Reason, "preferred model selection reason", 8192, true); err != nil {
			return err
		}
	}
	if r.PublicReport != nil {
		if err := r.PublicReport.Validate(); err != nil {
			return err
		}
	}
	if r.Recovery != nil {
		if err := r.Recovery.Validate(r.Outcome); err != nil {
			return err
		}
	}
	if r.Action != nil {
		if r.Outcome != "responded" {
			return domain.Fail(domain.Provider, "a conversation action requires a responded planning reply")
		}
		if err := domain.CheckText(r.Action.Instructions, "conversation action instructions", 32768, true); err != nil {
			return err
		}
		for _, field := range []struct {
			text string
			name string
			max  int
		}{
			{r.Action.Title, "public action title", 512},
			{r.Action.Description, "public action description", 65536},
			{r.Action.AcceptanceCriteria, "public action acceptance criteria", 32768},
		} {
			if err := domain.CheckText(field.text, field.name, field.max, r.Action.TaskID == ""); err != nil {
				return err
			}
		}
		if r.Action.WorkspaceRef != "" && !domain.ValidOperation(r.Action.WorkspaceRef) {
			return domain.Fail(domain.Provider, "conversation action workspace must be an existing opaque reference")
		}
		if r.Action.TaskID != "" && !domain.ValidOperation(r.Action.TaskID) {
			return domain.Fail(domain.Provider, "conversation continuation requires an existing task reference")
		}
	}
	if r.RequestDecision != "" && (r.Outcome != "responded" || !slices.Contains([]string{"accept", "defer", "decline", "answer"}, r.RequestDecision)) {
		return domain.Fail(domain.Provider, "request_decision requires responded and accept/defer/decline/answer")
	}
	if !slices.Contains([]string{"reviewed", "clear", "blocked", "completed", "paused", "failed", "responded", "researched", "reconciled"}, r.Outcome) {
		return domain.Fail(domain.Provider, "provider reply has an unsupported outcome")
	}
	if r.Decision != "" && (r.Outcome != "reviewed" || (r.Decision != "accept" && r.Decision != "rework")) {
		return domain.Fail(domain.Provider, "only a reviewed verification reply may propose accept or rework")
	}
	if err := domain.CheckText(r.Summary, "provider summary", 32768, true); err != nil {
		return err
	}
	if len(r.Questions) > 16 || len(r.Evidence) > 64 || len(r.AnswerIDs) > 64 {
		return domain.Fail(domain.Provider, "provider reply exceeds its reference/question budget")
	}
	for _, q := range r.Questions {
		if err := domain.CheckText(q.Body, "question", 8192, true); err != nil {
			return err
		}
		if len(q.RespondentIDs) > 32 {
			return domain.Fail(domain.Provider, "too many requested respondents")
		}
	}
	for _, e := range r.Evidence {
		if err := domain.CheckText(e, "evidence", 8192, true); err != nil {
			return err
		}
	}
	if r.Description != nil {
		if err := domain.CheckText(*r.Description, "refined description", 65536, true); err != nil {
			return err
		}
	}
	if r.AcceptanceCriteria != nil {
		if err := domain.CheckText(*r.AcceptanceCriteria, "refined criteria", 32768, true); err != nil {
			return err
		}
	}
	return domain.CheckText(r.Checkpoint, "opaque checkpoint", 256, false)
}

type Health struct {
	Provider          string   `json:"provider"`
	Version           string   `json:"version"`
	Path              string   `json:"path"`
	Authentication    string   `json:"authentication"`
	Models            []string `json:"models"`
	ModelCatalogError string   `json:"model_catalog_error,omitempty"`
}

type Invocation struct {
	ID        string
	Provider  string
	Model     string
	Path      string
	Prompt    string
	Workspace string
	Coding    bool
	Timeout   time.Duration
	Started   func(int) error
}

type Executor interface {
	Check(context.Context, string) (Health, error)
	Invoke(context.Context, Invocation) (Reply, error)
}

type Manager struct {
	Config config.Worker
	Runner process.Runner
}

func New(c config.Worker) *Manager { return &Manager{Config: c, Runner: process.Native{}} }

var versionPattern = regexp.MustCompile(`\b([0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?(?:\+[a-zA-Z0-9.-]+)?)\b`)

func (m *Manager) Resolve(name string) (string, error) {
	if !domain.ValidProvider(name) {
		return "", domain.Fail(domain.Invalid, "unsupported provider")
	}
	if override := m.Config.ProviderPaths[name]; override != "" {
		if !filepath.IsAbs(override) {
			return "", domain.Fail(domain.Invalid, "configured provider executable must be absolute")
		}
		return override, nil
	}
	var installed Installed
	err := config.Read(filepath.Join(m.Config.StateDir, "providers", name, "active.json"), &installed)
	if os.IsNotExist(err) {
		return "", errors.Join(domain.Fail(domain.Prerequisite, "provider is not installed; run provider install or configure an explicit executable"), err)
	}
	if err != nil {
		return "", err
	}
	verified := installed.SignaturesVerified
	if name == "antigravity" && domain.ValidHash(installed.ArtifactSHA256) && domain.ValidHash(installed.ExecutableSHA256) {
		hash, err := hashProviderExecutable(installed.Path)
		if err != nil {
			return "", err
		}
		verified = hash == installed.ExecutableSHA256
	}
	if installed.Provider != name || !filepath.IsAbs(installed.Path) || !verified {
		return "", domain.Fail(domain.Prerequisite, "managed provider metadata failed validation")
	}
	return installed.Path, nil
}

func (m *Manager) Paths() (map[string]string, error) {
	paths := map[string]string{}
	for _, name := range domain.ProviderNames() {
		path, err := m.Resolve(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

func (m *Manager) capture(ctx context.Context, path, dir string, args ...string) ([]byte, error) {
	output, diagnostic := process.NewBuffer(256<<10), process.NewBuffer(32<<10)
	err := m.Runner.Run(ctx, process.Spec{Path: path, Args: args, Dir: dir, Env: process.Environment(), Stdout: output, Stderr: diagnostic})
	data, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	return data, errors.Join(err, outputErr, diagnosticErr)
}

func (m *Manager) compatibility(ctx context.Context, name, path string) error {
	if name == "antigravity" {
		return m.antigravityCompatibility(ctx, path)
	}
	help, err := m.capture(ctx, path, "", "--help")
	if err != nil {
		return domain.Fail(domain.Prerequisite, "provider help/capability probe failed")
	}
	required := []string{"--available-tools", "--allow-all", "--disable-builtin-mcps", "--no-custom-instructions", "--no-ask-user", "--no-auto-update", "--silent"}
	if name == "codex" {
		required = []string{"--dangerously-bypass-approvals-and-sandbox", "--ask-for-approval"}
		execHelp, err := m.capture(ctx, path, "", "exec", "--help")
		if err != nil {
			return domain.Fail(domain.Prerequisite, "Codex exec capability probe failed")
		}
		for _, flag := range []string{"--json", "--output-last-message", "--ephemeral", "--sandbox", "--skip-git-repo-check"} {
			if !bytes.Contains(execHelp, []byte(flag)) {
				return domain.Fail(domain.Prerequisite, "Codex is missing a required unattended-output/sandbox capability")
			}
		}
	}
	for _, flag := range required {
		if !bytes.Contains(help, []byte(flag)) {
			return domain.Fail(domain.Prerequisite, "provider is missing a required non-prompting/tool-isolation capability")
		}
	}
	return nil
}

func (m *Manager) Check(ctx context.Context, name string) (Health, error) {
	health := Health{Provider: name, Authentication: "unknown", Models: []string{}}
	path, err := m.Resolve(name)
	if err != nil {
		return health, err
	}
	health.Path = path
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := m.capture(ctx, path, "", "--version")
	if err != nil {
		return health, domain.Fail(domain.Prerequisite, "provider version probe failed")
	}
	match := versionPattern.FindSubmatch(output)
	if len(match) != 2 {
		return health, domain.Fail(domain.Prerequisite, "provider did not report a supported version")
	}
	health.Version = string(match[1])
	if err := m.compatibility(ctx, name, path); err != nil {
		return health, err
	}
	if name == "antigravity" {
		authenticated, models, err := m.antigravityReadiness(ctx, path)
		var cleanup *process.CleanupError
		if err != nil && (!authenticated || ctx.Err() != nil || errors.As(err, &cleanup)) {
			return health, err
		}
		if authenticated {
			health.Authentication = "authenticated"
			if err != nil {
				health.ModelCatalogError = "read-only model catalog unavailable; session model selection will be delegated to the provider"
			} else {
				health.Models = models
			}
		} else {
			health.Authentication = "login-required"
		}
		return health, nil
	}
	if name == "copilot" {
		authenticated, models, err := m.copilotAuthentication(ctx, path)
		var cleanup *process.CleanupError
		if err != nil && (!authenticated || ctx.Err() != nil || errors.As(err, &cleanup)) {
			return health, err
		}
		if authenticated {
			health.Authentication = "authenticated"
			if err != nil {
				health.ModelCatalogError = "read-only model catalog unavailable; session model selection will be delegated to the provider"
			} else {
				health.Models = models
			}
		} else {
			health.Authentication = "login-required"
		}
		return health, nil
	}
	_, err = m.capture(ctx, path, "", "login", "status")
	if err == nil {
		health.Authentication = "credentials-present-unverified"
		health.Models, err = m.codexModels(ctx, path)
		var cleanup *process.CleanupError
		if err != nil {
			if ctx.Err() != nil || errors.As(err, &cleanup) {
				return health, err
			}
			health.Models = []string{}
			health.ModelCatalogError = "read-only model catalog unavailable; session model selection will be delegated to the provider"
		}
		return health, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		health.Authentication = "login-required"
		return health, nil
	}
	return health, domain.Fail(domain.Unavailable, "provider authentication status could not be determined")
}

func NativeArguments(name string, arguments []string) ([]string, error) {
	if !domain.ValidProvider(name) {
		return nil, domain.Fail(domain.Invalid, "unsupported provider adapter")
	}
	if name == "antigravity" && len(arguments) != 0 && (arguments[0] == "update" || arguments[0] == "install") {
		return nil, domain.Fail(domain.Prerequisite, "use Foundation provider install/update to preserve exact verified Antigravity CLI activation")
	}
	if name != "copilot" {
		return slices.Clone(arguments), nil
	}
	if len(arguments) != 0 && arguments[0] == "update" {
		return nil, domain.Fail(domain.Prerequisite, "use agent-cli provider update --provider copilot so integrity/signature checks and active-version selection are preserved")
	}
	if slices.Contains(arguments, "--no-auto-update") {
		return slices.Clone(arguments), nil
	}
	return append([]string{"--no-auto-update"}, arguments...), nil
}

func Arguments(name, workspace, output, model string, coding bool) ([]string, error) {
	if !domain.ValidProvider(name) {
		return nil, domain.Fail(domain.Invalid, "unsupported provider adapter")
	}
	if name == "antigravity" {
		return append(antigravityACPArguments(coding), "--agent", antigravityAgent), nil
	}
	var args []string
	if name == "codex" {
		if coding {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--ask-for-approval", "never")
		}
		args = append(args, "exec", "--json", "--ephemeral", "--skip-git-repo-check", "--color", "never")
		if !coding {
			args = append(args, "--sandbox", "read-only")
			for _, feature := range []string{"shell_tool", "unified_exec", "multi_agent", "multi_agent_v2", "apply_patch_freeform", "js_repl", "js_repl_tools_only", "apps", "browser_use", "computer_use", "view_image", "search_tool", "tool_search", "hooks", "plugin_hooks", "plugins"} {
				args = append(args, "-c", "features."+feature+"=false")
			}
			args = append(args, "-c", "project_doc_max_bytes=0", "-c", `web_search="disabled"`)
		} else {
			args = append(args, "-c", "features.multi_agent=false", "-c", "features.hooks=false", "-c", "features.plugin_hooks=false")
		}
		args = append(args, "-c", "mcp_servers={}", "-c", "check_for_update_on_startup=false")
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, "--output-last-message", output, "-"), nil
	}
	if coding {
		args = append(args, "--allow-all")
	}
	args = append(args, "--available-tools")
	if coding {
		args = append(args, "bash,list_bash,read_bash,write_bash,stop_bash,view,create,edit,apply_patch,glob,grep,web_fetch")
	}
	args = append(args, "--disable-builtin-mcps", "--no-custom-instructions", "--no-ask-user", "--no-auto-update", "--silent", "--stream", "off", "--log-level", "none")
	if model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

func (m *Manager) Invoke(ctx context.Context, inv Invocation) (reply Reply, err error) {
	if !domain.ValidOperation(inv.ID) || inv.Timeout <= 0 {
		return reply, domain.Fail(domain.Invalid, "invocation needs an ID and timeout")
	}
	if inv.Path == "" {
		return reply, domain.Fail(domain.Prerequisite, "provider executable was not pinned")
	}
	dir := filepath.Join(m.Config.StateDir, "invocations", inv.ID)
	if err := secure.Dir(dir); err != nil {
		return reply, err
	}
	if err := secure.Create(filepath.Join(dir, "prompt.txt"), []byte(inv.Prompt)); err != nil {
		return reply, err
	}
	if inv.Provider == "antigravity" {
		return m.invokeAntigravity(ctx, inv, dir)
	}
	outputFile := filepath.Join(dir, "reply.json")
	if inv.Provider == "codex" {
		if err := secure.Create(outputFile, nil); err != nil {
			return reply, err
		}
	}
	args, err := Arguments(inv.Provider, inv.Workspace, outputFile, inv.Model, inv.Coding)
	if err != nil {
		return reply, err
	}
	cwd := dir
	if inv.Coding {
		if inv.Workspace == "" {
			return reply, domain.Fail(domain.Prerequisite, "coding requires an authorized workspace")
		}
		cwd = inv.Workspace
	}
	ctx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()
	stdout, err := newInvocationOutput(filepath.Join(dir, "stdout.log"), 2<<20)
	if err != nil {
		return reply, err
	}
	stderr, err := newInvocationOutput(filepath.Join(dir, "stderr.log"), 256<<10)
	if err != nil {
		_, closeErr := stdout.finish()
		return reply, errors.Join(err, closeErr)
	}
	environment := process.Environment()
	if inv.Coding && m.Config.SourcePath() != "" {
		environment = append(environment, config.WorkerConfigEnvironment+"="+m.Config.SourcePath())
	}
	runErr := m.Runner.Run(ctx, process.Spec{Path: inv.Path, Args: args, Dir: cwd, Env: environment,
		Stdin: strings.NewReader(inv.Prompt), Stdout: stdout, Stderr: stderr, Started: inv.Started})
	out, outErr := stdout.finish()
	_, diagErr := stderr.finish()
	if err := errors.Join(runErr, outErr, diagErr); err != nil {
		if ctx.Err() != nil {
			return reply, errors.Join(fmt.Errorf("provider stopped: %w", ctx.Err()), err)
		}
		return reply, errors.Join(domain.Fail(domain.Provider, "provider failed; private local evidence retained and side effects require reconciliation"), err)
	}
	response := bytes.TrimSpace(out)
	if inv.Provider == "codex" {
		response, err = secure.Read(outputFile, 256<<10)
		if err != nil {
			return reply, err
		}
	}
	if len(response) > 256<<10 {
		return reply, domain.Fail(domain.Provider, "provider final response exceeds its size limit")
	}
	if err := config.Decode(response, &reply); err != nil {
		return reply, domain.Fail(domain.Provider, "provider did not return the required structured reply")
	}
	if err := reply.Validate(); err != nil {
		return reply, err
	}
	if inv.Provider != "codex" {
		if err := secure.Create(outputFile, response); err != nil {
			return reply, err
		}
	}
	return reply, nil
}
