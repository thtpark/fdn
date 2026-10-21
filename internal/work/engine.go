package work

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

//go:embed prompt.txt
var Prompt string

func BasePrompt() (string, error) {
	operating, err := guidance.WorkContext()
	if err != nil {
		return "", err
	}
	return Prompt + "\nFDN_OPERATING_GUIDANCE\n" + operating, nil
}

func invocationPrompt(context []byte) (string, error) {
	base, err := BasePrompt()
	if err != nil {
		return "", err
	}
	return base + "\nFDN_CONTEXT_JSON\n" + string(context), nil
}

type Blocker struct {
	TaskID string      `json:"task_id,omitempty"`
	Code   domain.Code `json:"code"`
	Reason string      `json:"reason"`
}

type Status struct {
	State             string         `json:"state"`
	Mode              string         `json:"mode"`
	TaskID            string         `json:"task_id,omitempty"`
	JournalID         string         `json:"journal_id,omitempty"`
	PendingReports    int            `json:"pending_reports"`
	InitialReviewDone bool           `json:"initial_review_done"`
	LastReport        *ReportReceipt `json:"last_report,omitempty"`
	Blockers          []Blocker      `json:"blockers"`
	UpdatedAt         int64          `json:"updated_at"`
}

type Launch struct {
	Phase         string              `json:"phase"`
	AgentID       string              `json:"agent_id"`
	Task          *domain.Task        `json:"task,omitempty"`
	Project       *domain.Project     `json:"project,omitempty"`
	Discussion    []domain.Entry      `json:"discussion"`
	Capabilities  []local.Observation `json:"capabilities"`
	Knowledge     []local.Knowledge   `json:"selected_private_knowledge"`
	Message       *domain.Message     `json:"message,omitempty"`
	Attention     *domain.Attention   `json:"attention,omitempty"`
	InventoryHash string              `json:"inventory_hash,omitempty"`
	Continuation  *provider.Reply     `json:"continuation,omitempty"`
}

type Engine struct {
	Client     *api.Client
	State      *local.State
	Config     config.Worker
	ConfigPath string
	Executor   provider.Executor
	Log        *slog.Logger
	Now        func() time.Time
}

func New(c config.Worker, path string, client *api.Client, state *local.State, executor provider.Executor) *Engine {
	return &Engine{Config: c, ConfigPath: path, Client: client, State: state, Executor: executor, Log: slog.Default(), Now: time.Now}
}

func (e *Engine) currentConfig() (config.Worker, error) {
	if e.ConfigPath != "" {
		return config.LoadWorker(e.ConfigPath)
	}
	return e.Config, e.Config.Validate()
}

func (e *Engine) Status(ctx context.Context) (Status, error) {
	c, err := e.currentConfig()
	if err != nil {
		return Status{}, err
	}
	status := Status{State: "idle", Mode: c.Mode, Blockers: []Blocker{}}
	_, err = e.State.Get(ctx, "work-status", &status)
	if err != nil {
		return status, err
	}
	status.Mode = c.Mode
	items, err := e.State.Outbox(ctx)
	if err != nil {
		return status, err
	}
	status.PendingReports = len(items)
	inventory, err := e.State.Inventory(ctx)
	if err != nil {
		if domain.AsFault(err).Code != domain.Prerequisite {
			return status, err
		}
		return status, nil
	}
	var receipt string
	found, err := e.State.Get(ctx, "initial-review", &receipt)
	if err != nil {
		return status, err
	}
	status.InitialReviewDone = found && receipt == inventory.Hash()
	return status, nil
}

func (e *Engine) Step(ctx context.Context, manual bool) (status Status, err error) {
	c, err := e.currentConfig()
	if err != nil {
		return status, err
	}
	status = Status{State: "idle", Mode: c.Mode, Blockers: []Blocker{}, UpdatedAt: e.Now().UTC().Unix()}
	defer func() {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var receipt ReportReceipt
		if found, readErr := e.State.Get(saveCtx, "last-report", &receipt); readErr != nil {
			err = errors.Join(err, readErr)
		} else if found {
			status.LastReport = &receipt
		}
		if pending, readErr := e.State.Outbox(saveCtx); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.PendingReports = len(pending)
		}
		if err != nil {
			status.State = "blocked"
			fault := domain.AsFault(err)
			status.Blockers = append(status.Blockers, Blocker{TaskID: status.TaskID, Code: fault.Code, Reason: fault.Message})
		}
		err = errors.Join(err, e.State.Put(saveCtx, "work-status", status))
	}()
	slot, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		return status, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	if e.Client.Credential.AgentID == "" || len(e.Client.Credential.Token) != 43 {
		return status, domain.Fail(domain.Prerequisite, "enroll this worker before starting work")
	}
	if err := e.State.BindIdentity(ctx, e.Client.Credential.AgentID); err != nil {
		return status, err
	}
	if handled, err := e.reconcile(ctx, manual); err != nil {
		return status, err
	} else if handled {
		status.State = "reconciled"
		return status, nil
	}
	var maintenance struct {
		Active bool `json:"active"`
	}
	if _, err := e.State.Get(ctx, "maintenance", &maintenance); err != nil {
		return status, err
	}
	if maintenance.Active {
		return status, domain.Fail(domain.Prerequisite, "maintenance hold blocks new work and sessions")
	}
	var sessionFence struct {
		Active bool   `json:"active"`
		ID     string `json:"id"`
	}
	if _, err := e.State.Get(ctx, "session-fence", &sessionFence); err != nil {
		return status, err
	}
	if sessionFence.Active {
		return status, domain.Fail(domain.Prerequisite, "an interrupted interactive session requires explicit stopped-process recovery")
	}
	if !manual && c.Mode == "manual" {
		status.State = "manual"
		return status, nil
	}
	me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return status, err
	}
	if me.ID != e.Client.Credential.AgentID {
		return status, domain.Fail(domain.Denied, "configured identity does not match Foundation authentication")
	}
	owned, err := api.Call[[]domain.Task](ctx, e.Client, http.MethodGet, "/v1/ownership", nil)
	if err != nil {
		return status, err
	}
	if len(owned) != 0 {
		status.TaskID = owned[0].ID
		return status, domain.Fail(domain.Prerequisite, "an active server claim has no reconciled local invocation; confirm original processes stopped and obtain an explicit fenced release")
	}
	inventory, err := e.State.Inventory(ctx)
	if err != nil {
		if domain.AsFault(err).Code != domain.Prerequisite {
			return status, err
		}
		paths := c.ProviderPaths
		if manager, ok := e.Executor.(*provider.Manager); ok {
			paths, err = manager.Paths()
			if err != nil {
				return status, err
			}
		}
		inventory, err = e.State.RefreshInventory(ctx, paths)
		if err != nil {
			return status, err
		}
	}
	var reviewed string
	found, err := e.State.Get(ctx, "initial-review", &reviewed)
	if err != nil {
		return status, err
	}
	if !found || reviewed != inventory.Hash() {
		launch, err := e.launch(ctx, "onboarding", nil)
		if err != nil {
			return status, err
		}
		launch.Capabilities, launch.InventoryHash = inventory.Summary(), inventory.Hash()
		status.State, status.JournalID = "onboarding", ""
		journal, err := e.text(ctx, c, launch, manual)
		status.JournalID = journal.ID
		return status, err
	}
	status.InitialReviewDone = true
	sessions, err := api.Call[[]domain.Session](ctx, e.Client, http.MethodGet, "/v1/sessions", nil)
	if err != nil {
		return status, err
	}
	for _, session := range sessions {
		if session.TargetID == me.ID && session.OpenRequested && session.State == "consented" {
			status.State = "session-pending"
			return status, nil
		}
	}
	if acted, err := e.messages(ctx, c, true, manual); err != nil {
		return status, err
	} else if acted {
		status.State = "urgent-message"
		return status, nil
	}
	tasks, err := e.assigned(ctx, me.ID)
	if err != nil {
		return status, err
	}
	slices.SortStableFunc(tasks, func(a, b domain.Task) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		if a.Importance != b.Importance {
			return b.Importance - a.Importance
		}
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return 0
	})
	for _, task := range tasks {
		if task.Status != domain.Todo && task.Status != domain.Paused {
			continue
		}
		if task.Repetitive || task.ArchivedAt != 0 {
			continue
		}
		if task.ActiveRun != nil {
			status.Blockers = append(status.Blockers, Blocker{TaskID: task.ID, Code: domain.Prerequisite, Reason: "active server claim needs its original local journal or explicit stopped-process release"})
			continue
		}
		acted, actionErr := e.prepare(ctx, c, task, manual)
		if actionErr != nil {
			fault := domain.AsFault(actionErr)
			if slices.Contains([]domain.Code{domain.Prerequisite, domain.Conflict, domain.NotFound}, fault.Code) {
				status.Blockers = append(status.Blockers, Blocker{TaskID: task.ID, Code: fault.Code, Reason: fault.Message})
				continue
			}
			status.TaskID = task.ID
			return status, actionErr
		}
		if acted {
			status.State, status.TaskID = "task", task.ID
			return status, nil
		}
	}
	if acted, err := e.attention(ctx, c, manual); err != nil {
		return status, err
	} else if acted {
		status.State = "attention"
		return status, nil
	}
	if acted, err := e.messages(ctx, c, false, manual); err != nil {
		return status, err
	} else if acted {
		status.State = "message"
		return status, nil
	}
	if acted, err := e.research(ctx, c, tasks, manual); err != nil {
		return status, err
	} else if acted {
		status.State = "research"
		return status, nil
	}
	return status, nil
}

func (e *Engine) assigned(ctx context.Context, actor string) ([]domain.Task, error) {
	items := []domain.Task{}
	for _, state := range []domain.Status{domain.Todo, domain.Paused, domain.Backlogged} {
		cursor := ""
		for {
			path := "/v1/tasks?assignee=" + actor + "&status=" + string(state) + "&limit=200"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			page, err := api.Call[domain.Page[domain.Task]](ctx, e.Client, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			items = append(items, page.Items...)
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
	}
	return items, nil
}

func (e *Engine) launch(ctx context.Context, phase string, task *domain.Task) (Launch, error) {
	launch := Launch{Phase: phase, AgentID: e.Client.Credential.AgentID, Task: task, Discussion: []domain.Entry{}, Capabilities: []local.Observation{}, Knowledge: []local.Knowledge{}}
	knowledge, err := e.State.SelectedKnowledge(ctx)
	if err != nil {
		return launch, err
	}
	launch.Knowledge = knowledge
	if task == nil {
		return launch, nil
	}
	project, err := api.Call[domain.Project](ctx, e.Client, http.MethodGet, api.Path("projects", task.ProjectID), nil)
	if err != nil {
		return launch, err
	}
	launch.Project = &project
	cursor := ""
	size := 0
	for {
		path := api.Path("tasks", task.ID, "entries") + "?limit=200"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page, err := api.Call[domain.Page[domain.Entry]](ctx, e.Client, http.MethodGet, path, nil)
		if err != nil {
			return launch, err
		}
		for _, entry := range page.Items {
			size += len(entry.Body)
			if size > 256<<10 {
				return launch, domain.Fail(domain.Prerequisite, "discussion exceeds the bounded review context; consolidate the current specification")
			}
		}
		launch.Discussion = append(launch.Discussion, page.Items...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	var continuation provider.Reply
	found, err := e.State.Get(ctx, "checkpoint:"+task.ID, &continuation)
	if err != nil {
		return launch, err
	}
	if found {
		launch.Continuation = &continuation
	}
	return launch, nil
}

func (e *Engine) selected(ctx context.Context, c config.Worker, task *domain.Task) (provider.Health, string, error) {
	name, model := c.Provider, c.Model
	if task != nil && task.Provider != "" {
		name = task.Provider
		if name != c.Provider {
			model = ""
		}
	}
	if task != nil && task.Model != "" {
		model = task.Model
	}
	health, err := e.Executor.Check(ctx, name)
	if err != nil {
		return health, model, err
	}
	if health.Authentication != "authenticated" && health.Authentication != "credentials-present-unverified" {
		return health, model, domain.Fail(domain.Prerequisite, "provider login is required; use a human-only consented authentication handoff")
	}
	if model != "" && len(health.Models) > 0 && !slices.Contains(health.Models, model) {
		return health, model, domain.Fail(domain.Prerequisite, "configured required model is absent from the fresh provider catalog")
	}
	return health, model, nil
}

func (e *Engine) newJournal(launch Launch, health provider.Health, model string) (local.Journal, error) {
	data, err := json.Marshal(launch)
	if err != nil {
		return local.Journal{}, err
	}
	prompt, err := invocationPrompt(data)
	if err != nil {
		return local.Journal{}, err
	}
	if len(prompt) > 512<<10 {
		return local.Journal{}, domain.Fail(domain.Prerequisite, "selected context exceeds the bounded prompt budget")
	}
	j := local.Journal{ID: domain.NewID("invocation"), Kind: launch.Phase, State: "prepared", Provider: health.Provider, ProviderVersion: health.Version, Model: model, Context: data, PromptHash: domain.Hash(prompt), CreatedAt: e.Now().UTC().Unix()}
	if launch.Task != nil {
		j.TaskID, j.SpecGeneration, j.DiscussionGeneration = launch.Task.ID, launch.Task.SpecGeneration, launch.Task.DiscussionGeneration
	}
	return j, nil
}

func (e *Engine) text(ctx context.Context, c config.Worker, launch Launch, manual bool) (local.Journal, error) {
	health, model, err := e.selected(ctx, c, launch.Task)
	if err != nil {
		return local.Journal{}, err
	}
	j, err := e.newJournal(launch, health, model)
	if err != nil {
		return j, err
	}
	if err := e.State.SaveJournal(ctx, j); err != nil {
		return j, err
	}
	err = e.invokeText(ctx, c, &j, launch, health.Path, manual)
	return j, err
}

func (e *Engine) invokeText(ctx context.Context, c config.Worker, j *local.Journal, launch Launch, path string, manual bool) error {
	prompt, err := invocationPrompt(j.Context)
	if err != nil {
		return err
	}
	j.State = "invoking"
	if err := e.State.SaveJournal(ctx, *j); err != nil {
		return err
	}
	invocationCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go e.guardMode(invocationCtx, cancel, manual, "")
	reply, err := e.Executor.Invoke(invocationCtx, provider.Invocation{ID: j.ID, Provider: j.Provider, Path: path, Model: j.Model,
		Prompt: prompt, Timeout: time.Duration(min(c.InvocationTimeoutSeconds, 300)) * time.Second,
		Started: func(pid int) error { j.PID = pid; return e.State.SaveJournal(invocationCtx, *j) }})
	if err == nil {
		err = reply.Validate()
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	if err != nil {
		j.State, j.Problem = "uncertain", "text invocation interrupted/invalid; explicit retry requires confirming its process stopped"
		return errors.Join(err, e.State.SaveJournal(saveCtx, *j))
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	j.Reply, j.State = data, "reply-prepared"
	if err := e.State.SaveJournal(saveCtx, *j); err != nil {
		return err
	}
	return e.applyText(saveCtx, j, launch, reply)
}

func (e *Engine) guardMode(ctx context.Context, cancel context.CancelCauseFunc, manual bool, workspace string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			config, err := e.currentConfig()
			if err != nil {
				cancel(err)
				return
			}
			if !manual && config.Mode == "manual" {
				cancel(domain.Fail(domain.Prerequisite, "automatic mode was stopped"))
				return
			}
			if workspace != "" {
				if _, err := e.State.Workspace(ctx, workspace); err != nil {
					cancel(err)
					return
				}
			}
		}
	}
}

func (e *Engine) taskChange(ctx context.Context, task domain.Task, operation, kind, reason string, set func(*domain.TaskChange)) (domain.Task, error) {
	request := domain.TaskChange{Command: domain.Command{OperationID: operation, ExpectedRevision: task.Revision}, Kind: kind, Reason: reason}
	if set != nil {
		set(&request)
	}
	return api.Call[domain.Task](ctx, e.Client, http.MethodPost, api.Path("tasks", task.ID, "change"), request)
}

func (e *Engine) done(ctx context.Context, j *local.Journal) error {
	j.State = "done"
	return e.State.SaveJournal(ctx, *j)
}

func checkCleanup(err error) bool { var failure *process.CleanupError; return errors.As(err, &failure) }

func (e *Engine) workspace(ctx context.Context, c config.Worker, task domain.Task) (domain.Task, local.Binding, error) {
	if task.WorkspaceRef != "" {
		binding, err := e.State.Workspace(ctx, task.WorkspaceRef)
		return task, binding, err
	}
	if !c.AllowWorkspaceTools {
		return task, local.Binding{}, domain.Fail(domain.Prerequisite, "bind an owned workspace with explicit local tool permission")
	}
	path := filepath.Join(c.WorkspaceRoot, task.ProjectID)
	if err := secure.Dir(path); err != nil {
		return task, local.Binding{}, err
	}
	forbidden := []string{filepath.Dir(c.CredentialFile), filepath.Dir(c.CAFile)}
	if e.ConfigPath != "" {
		forbidden = append(forbidden, filepath.Dir(e.ConfigPath))
	}
	binding, err := e.State.BindWorkspace(ctx, path, true, c.InvocationTimeoutSeconds, forbidden)
	if err != nil {
		return task, binding, err
	}
	task, err = e.taskChange(ctx, task, domain.NewID("op"), "workspace", "worker prepared an owned reusable local workspace", func(r *domain.TaskChange) { r.WorkspaceRef = binding.Ref })
	return task, binding, err
}

func (e *Engine) recordProblem(ctx context.Context, key string, err error) {
	fault := domain.AsFault(err)
	if saveErr := e.State.Put(ctx, key, fault); saveErr != nil {
		e.Log.Error("cannot persist worker diagnostic", "error", saveErr)
	}
	e.Log.Warn("worker operation blocked", "code", fault.Code, "message", fault.Message)
}
