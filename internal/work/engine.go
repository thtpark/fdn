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
	"sync"
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
	var launch Launch
	if err := json.Unmarshal(context, &launch); err != nil {
		return "", err
	}
	contract, err := phasePrompt(launch.Phase)
	if err != nil {
		return "", err
	}
	return base + "\n" + contract + "\nFDN_CONTEXT_JSON\n" + string(context), nil
}

type Blocker struct {
	TaskID string      `json:"task_id,omitempty"`
	Code   domain.Code `json:"code"`
	Reason string      `json:"reason"`
}

type Status struct {
	State                  string                  `json:"state"`
	Mode                   string                  `json:"mode"`
	TaskID                 string                  `json:"task_id,omitempty"`
	JournalID              string                  `json:"journal_id,omitempty"`
	PendingReports         int                     `json:"pending_reports"`
	InitialReviewDone      bool                    `json:"initial_review_done"`
	LastReport             *ReportReceipt          `json:"last_report,omitempty"`
	Blockers               []Blocker               `json:"blockers"`
	UpdatedAt              int64                   `json:"updated_at"`
	AuthenticationRequests []AuthenticationRequest `json:"authentication_requests"`
	ModeControl            *ModeControl            `json:"mode_control,omitempty"`
	PermissionRequests     []PermissionRequest     `json:"permission_requests"`
	Health                 *domain.WorkerHealth    `json:"health,omitempty"`
	TextResponseBlocks     []TextResponseBlock     `json:"text_response_blocks"`
	ActivityText           string                  `json:"activity_text"`
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
	ActionKey     string              `json:"action_key,omitempty"`
	Continuation  *provider.Reply     `json:"continuation,omitempty"`
}

type Engine struct {
	Client        *api.Client
	State         *local.State
	Config        config.Worker
	ConfigPath    string
	Executor      provider.Executor
	Log           *slog.Logger
	Now           func() time.Time
	configMu      sync.RWMutex
	statusWake    chan struct{}
	projectionMu  sync.Mutex
	projection    domain.ActivitySummary
	healthWake    chan struct{}
	controlWake   chan struct{}
	updateMu      sync.Mutex
	authorityMu   sync.Mutex
	feedRunning   bool
	feedOnline    bool
	authorities   map[*authoritySubscription]struct{}
	authMu        sync.Mutex
	authControlMu sync.Mutex
	permissionMu  sync.Mutex
}

func New(c config.Worker, path string, client *api.Client, state *local.State, executor provider.Executor) *Engine {
	return &Engine{Config: c, ConfigPath: path, Client: client, State: state, Executor: executor, Log: slog.Default(), Now: time.Now,
		statusWake: make(chan struct{}, 1), healthWake: make(chan struct{}, 1), controlWake: make(chan struct{}, 1), authorities: make(map[*authoritySubscription]struct{})}
}

func (e *Engine) currentConfig() (config.Worker, error) {
	if e.ConfigPath != "" {
		return config.LoadWorker(e.ConfigPath)
	}
	e.configMu.RLock()
	defer e.configMu.RUnlock()
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
	var control ModeControl
	if found, err := e.State.Get(ctx, "automation-control", &control); err != nil {
		return status, err
	} else if found {
		status.ModeControl = &control
	}
	items, err := e.State.Outbox(ctx)
	if err != nil {
		return status, err
	}
	status.PendingReports = len(items)
	status.AuthenticationRequests, err = e.authenticationRequests(ctx)
	if err != nil {
		return status, err
	}
	status.PermissionRequests, err = e.permissionRequests(ctx)
	if err != nil {
		return status, err
	}
	status.Health, err = e.health(ctx)
	if err != nil {
		return status, err
	}
	status.TextResponseBlocks, err = e.textResponseBlocks(ctx)
	if err != nil {
		return status, err
	}
	status.ActivityText, err = e.activityText(ctx, status.Health)
	if err != nil {
		return status, err
	}
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
		if requests, readErr := e.authenticationRequests(saveCtx); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.AuthenticationRequests = requests
		}
		if requests, readErr := e.permissionRequests(saveCtx); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.PermissionRequests = requests
		}
		var control ModeControl
		if found, readErr := e.State.Get(saveCtx, "automation-control", &control); readErr != nil {
			err = errors.Join(err, readErr)
		} else if found {
			status.ModeControl = &control
		}
		if health, readErr := e.health(saveCtx); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.Health = health
		}
		if blocks, readErr := e.textResponseBlocks(saveCtx); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.TextResponseBlocks = blocks
		}
		if text, readErr := e.activityText(saveCtx, status.Health); readErr != nil {
			err = errors.Join(err, readErr)
		} else {
			status.ActivityText = text
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
		if domain.AsFault(err).Code == domain.Conflict {
			deferWorkUntil(ctx, e.Now().Add(time.Second))
		}
		return status, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	defer func() {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, e.finishHealth(saveCtx, status, err))
	}()
	if err := e.beginHealth(ctx, c); err != nil {
		return status, err
	}
	if e.Client.Credential.AgentID == "" {
		return status, domain.Fail(domain.Prerequisite, "enroll this worker before starting work")
	}
	if err := e.State.BindIdentity(ctx, e.Client.Credential.AgentID); err != nil {
		return status, errors.Join(err, e.blockHealth(ctx, "identity-unavailable"))
	}
	if err := e.phaseHealth(ctx, "reconciling", nil, "", 120, false); err != nil {
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
		return status, errors.Join(domain.Fail(domain.Prerequisite, "maintenance hold blocks new work and sessions"), e.blockHealth(ctx, "maintenance"))
	}
	var sessionFence struct {
		Active bool   `json:"active"`
		ID     string `json:"id"`
	}
	if _, err := e.State.Get(ctx, "session-fence", &sessionFence); err != nil {
		return status, err
	}
	if sessionFence.Active {
		return status, errors.Join(domain.Fail(domain.Prerequisite, "an interrupted interactive session requires explicit stopped-process recovery"), e.blockHealth(ctx, "session-fence"))
	}
	if err := e.phaseHealth(ctx, "checking", nil, "", 120, false); err != nil {
		return status, err
	}
	me, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return status, err
	}
	if me.ID != e.Client.Credential.AgentID {
		return status, domain.Fail(domain.Denied, "configured identity does not match Foundation authentication")
	}
	c, err = e.applyMode(ctx, c, me)
	if err != nil {
		return status, err
	}
	status.Mode = c.Mode
	if handled, err := e.authenticationControl(ctx, c); err != nil {
		return status, err
	} else if handled {
		status.State = "authentication-refreshed"
		return status, nil
	}
	if c.Mode == "manual" && (!manual || !me.Founder) {
		status.State = "manual"
		if manual && !me.Founder {
			return status, domain.Fail(domain.Prerequisite, "Founder paused this agent; only Founder may authorize resumption")
		}
		return status, nil
	}
	owned, err := api.Call[[]domain.Task](ctx, e.Client, http.MethodGet, "/v1/ownership", nil)
	if err != nil {
		return status, err
	}
	if len(owned) != 0 {
		status.TaskID = owned[0].ID
		return status, errors.Join(domain.Fail(domain.Prerequisite, "an active server claim has no reconciled local invocation; confirm original processes stopped and obtain an explicit fenced release"), e.blockHealth(ctx, "unreconciled-claim"))
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
		if err := e.requestedAuthentication(ctx, c, me); err != nil {
			return status, err
		}
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
			if permissionFailure(actionErr) {
				actionErr = e.permissionBlocked(ctx, c, Launch{Task: &task}, "task-authority", actionErr)
			}
			fault := domain.AsFault(actionErr)
			if slices.Contains([]domain.Code{domain.Prerequisite, domain.Conflict, domain.NotFound, domain.Denied}, fault.Code) {
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
	reviews, err := e.verificationCandidates(ctx, me)
	if err != nil {
		return status, err
	}
	for _, task := range reviews {
		if err := e.verify(ctx, c, me, task, manual); err != nil {
			if permissionFailure(err) {
				err = e.permissionBlocked(ctx, c, Launch{Task: &task}, "task-authority", err)
			}
			fault := domain.AsFault(err)
			if slices.Contains([]domain.Code{domain.Prerequisite, domain.Conflict, domain.NotFound, domain.Denied}, fault.Code) {
				status.Blockers = append(status.Blockers, Blocker{TaskID: task.ID, Code: fault.Code, Reason: fault.Message})
				continue
			}
			status.TaskID = task.ID
			return status, err
		}
		status.State, status.TaskID = "verification", task.ID
		return status, nil
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
	if len(status.Blockers) != 0 {
		status.State = "blocked"
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

func (e *Engine) selected(ctx context.Context, c config.Worker, launch Launch) (provider.Health, string, error) {
	name, model := c.Provider, c.Model
	if task := launch.Task; task != nil && launch.Attention == nil && launch.Phase != domain.VerificationRun {
		if task.Provider != "" {
			name = task.Provider
			if name != c.Provider {
				model = ""
			}
		}
		if task.Model != "" {
			model = task.Model
		}
	}
	return e.selectProvider(ctx, c, name, model, launch.Task)
}

func (e *Engine) selectProvider(ctx context.Context, c config.Worker, name, model string, task *domain.Task) (provider.Health, string, error) {
	if err := e.phaseHealth(ctx, "provider-check", nil, "", 60, false); err != nil {
		return provider.Health{}, model, err
	}
	health, err := e.Executor.Check(ctx, name)
	observedErr := e.providerHealth(ctx, name, model, health, err)
	if err != nil {
		if !permissionFailure(err) {
			deferWorkUntil(ctx, e.Now().Add(time.Duration(c.PollSeconds)*time.Second))
		}
		blocker := "provider-unavailable"
		if permissionFailure(err) {
			blocker = "provider-permission"
			err = e.permissionBlocked(ctx, c, Launch{Task: task}, "provider-permission", err)
		}
		return health, model, errors.Join(err, observedErr, e.blockHealth(ctx, blocker))
	}
	if observedErr != nil {
		return health, model, observedErr
	}
	if health.Authentication != "authenticated" && health.Authentication != "credentials-present-unverified" {
		deferWorkUntil(ctx, e.Now().Add(time.Duration(c.PollSeconds)*time.Second))
		if health.Authentication != "login-required" && health.Authentication != "credentials-rejected" {
			return health, model, errors.Join(domain.Fail(domain.Prerequisite, "provider authentication is unknown; inspect provider status without starting inference"), e.blockHealth(ctx, "authentication-unknown"))
		}
		if err := e.blockHealth(ctx, "authentication-required"); err != nil {
			return health, model, err
		}
		request, err := e.requestAuthentication(ctx, c, name, task)
		if err != nil {
			return health, model, err
		}
		if request.State == "expired" {
			return health, model, domain.Fail(domain.Prerequisite, "provider login is required; protected sign-in request "+request.ID+" expired; Founder must explicitly request a fresh sign-in notice")
		}
		return health, model, domain.Fail(domain.Prerequisite, "provider login is required; protected sign-in request "+request.ID+" was delivered and awaits human action")
	}
	if err := e.resolveAuthentication(ctx, name); err != nil {
		return health, model, err
	}
	if model != "" && len(health.Models) > 0 && !slices.Contains(health.Models, model) {
		return health, model, errors.Join(domain.Fail(domain.Prerequisite, "configured required model is absent from the fresh provider catalog"), e.blockHealth(ctx, "provider-model-unavailable"))
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
	ctx, launch = actionContext(ctx, launch)
	if err := e.phaseHealth(ctx, "preparing", &launch, "", 120, false); err != nil {
		return local.Journal{}, err
	}
	if err := e.textResponseBlocked(ctx, c, launch); err != nil {
		return local.Journal{}, err
	}
	health, model, err := e.selected(ctx, c, launch)
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
	invocationCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if err := e.guardAuthority(invocationCtx, cancel, &authoritySubscription{modeOnly: true}, func(ctx context.Context) error {
		return e.validateRunMode(ctx, manual)
	}); err != nil {
		return err
	}
	if err := e.phaseHealth(ctx, "invoking", &launch, j.ID, min(c.InvocationTimeoutSeconds, 300), true); err != nil {
		return err
	}
	j.State = "invoking"
	if err := e.State.SaveJournal(ctx, *j); err != nil {
		return err
	}
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
		saveErr := e.State.SaveJournal(saveCtx, *j)
		return errors.Join(err, saveErr, e.invocationBlocked(saveCtx, c, *j, launch, err))
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	j.Reply, j.State = data, "reply-prepared"
	if err := e.State.SaveJournal(saveCtx, *j); err != nil {
		return err
	}
	if err := e.resolvePermission(saveCtx, launch.ActionKey, "provider-permission"); err != nil {
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
	if err := e.State.SaveJournal(ctx, *j); err != nil {
		return err
	}
	return e.completedHealth(ctx)
}

func checkCleanup(err error) bool { var failure *process.CleanupError; return errors.As(err, &failure) }

func (e *Engine) workspace(ctx context.Context, c config.Worker, task domain.Task) (domain.Task, local.Binding, error) {
	if task.WorkspaceRef != "" {
		binding, err := e.State.Workspace(ctx, task.WorkspaceRef)
		if err != nil {
			err = e.permissionBlocked(ctx, c, Launch{Task: &task}, "workspace", err)
		} else {
			err = e.resolvePermission(ctx, requestedAction(ctx, Launch{Task: &task}), "workspace")
		}
		return task, binding, err
	}
	binding, err := e.projectWorkspace(ctx, c, task.ProjectID)
	if err != nil {
		return task, binding, e.permissionBlocked(ctx, c, Launch{Task: &task}, "workspace", err)
	}
	if err := e.resolvePermission(ctx, requestedAction(ctx, Launch{Task: &task}), "workspace"); err != nil {
		return task, binding, err
	}
	task, err = e.taskChange(ctx, task, domain.NewID("op"), "workspace", "worker prepared an owned reusable local workspace", func(r *domain.TaskChange) { r.WorkspaceRef = binding.Ref })
	return task, binding, err
}

func (e *Engine) projectWorkspace(ctx context.Context, c config.Worker, projectID string) (local.Binding, error) {
	if !c.AllowWorkspaceTools {
		return local.Binding{}, domain.Fail(domain.Prerequisite, "automatic project workspaces require explicit allow_workspace_tools permission")
	}
	path := filepath.Join(c.WorkspaceRoot, projectID)
	if err := secure.Dir(path); err != nil {
		return local.Binding{}, err
	}
	forbidden := []string{filepath.Dir(c.CredentialFile), filepath.Dir(c.CAFile)}
	if e.ConfigPath != "" {
		forbidden = append(forbidden, filepath.Dir(e.ConfigPath))
	}
	return e.State.BindWorkspace(ctx, path, true, c.InvocationTimeoutSeconds, forbidden)
}

func (e *Engine) recordProblem(ctx context.Context, key string, err error) {
	fault := domain.AsFault(err)
	if saveErr := e.observeProblem(ctx, key, err); saveErr != nil {
		e.Log.Error("cannot persist worker health diagnostic", "component", healthComponent(key), "code", domain.AsFault(saveErr).Code)
	}
	if saveErr := e.State.Put(ctx, key, fault); saveErr != nil {
		e.Log.Error("cannot persist worker diagnostic", "error", saveErr)
	}
	e.Log.Warn("worker operation blocked", "code", fault.Code, "message", fault.Message)
}
