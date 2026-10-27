package cli

import (
	"context"
	"encoding/json"
	"flag"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/platform"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

func (a *app) localCommand(ctx context.Context, r *resources, group string, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "leaf command required")
	}
	switch group {
	case "config":
		if args[0] == "show" {
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return r.config, nil
		}
		if args[0] == "set" {
			var patch struct {
				PollSeconds              *int              `json:"poll_seconds"`
				InvocationTimeoutSeconds *int              `json:"invocation_timeout_seconds"`
				ResearchIntervalHours    *int              `json:"research_interval_hours"`
				AutomaticProviderUpdates *bool             `json:"automatic_provider_updates"`
				AllowWorkspaceTools      *bool             `json:"allow_workspace_tools"`
				ProviderPaths            map[string]string `json:"provider_paths"`
				Screens                  *[]config.Screen  `json:"screens"`
				SSH                      *config.SSH       `json:"ssh"`
			}
			if err := a.input(args[1:], &patch); err != nil {
				return nil, err
			}
			lock, err := secure.Acquire(a.configFile + ".lock")
			if err != nil {
				return nil, err
			}
			defer lock.Close()
			c, err := config.LoadWorker(a.configFile)
			if err != nil {
				return nil, err
			}
			if patch.PollSeconds != nil {
				c.PollSeconds = *patch.PollSeconds
			}
			if patch.InvocationTimeoutSeconds != nil {
				c.InvocationTimeoutSeconds = *patch.InvocationTimeoutSeconds
			}
			if patch.ResearchIntervalHours != nil {
				c.ResearchIntervalHours = *patch.ResearchIntervalHours
			}
			if patch.AutomaticProviderUpdates != nil {
				c.AutomaticProviderUpdates = *patch.AutomaticProviderUpdates
			}
			if patch.AllowWorkspaceTools != nil {
				c.AllowWorkspaceTools = *patch.AllowWorkspaceTools
			}
			if patch.ProviderPaths != nil {
				c.ProviderPaths = patch.ProviderPaths
			}
			if patch.Screens != nil {
				c.Screens = *patch.Screens
			}
			if patch.SSH != nil {
				c.SSH = patch.SSH
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			return map[string]any{"updated": true, "transport_and_provider_path_changes_require_restart": true}, config.Write(a.configFile, c)
		}
	case "mode":
		if args[0] == "show" {
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return map[string]string{"mode": r.config.Mode}, nil
		}
		if args[0] == "set" {
			if err := requireArgs(args, 2); err != nil {
				return nil, err
			}
			me, err := api.Call[domain.Agent](ctx, r.client, "GET", "/v1/me", nil)
			if err != nil {
				return nil, err
			}
			if !me.Founder {
				return nil, domain.Fail(domain.Denied, "only Founder may request manual/ai mode; ask Founder to use agent mode AGENT")
			}
			req := domain.ModeRequest{Mode: args[1], Reason: "Founder explicitly changed its own automation mode"}
			if err := a.metadata(&req.Command); err != nil {
				return nil, err
			}
			data, err := a.write(ctx, r, api.Path("agents", me.ID, "mode"), req, "/v1/me")
			if err != nil {
				return nil, err
			}
			var updated domain.Agent
			if err := json.Unmarshal(data, &updated); err != nil {
				return nil, err
			}
			c, err := work.ReconcileMode(ctx, r.state, r.config, a.configFile, updated)
			return map[string]string{"mode": c.Mode}, err
		}
	case "preferences":
		if args[0] == "show" {
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return map[string]string{"provider": r.config.Provider, "model": r.config.Model}, nil
		}
		if args[0] == "set" {
			var patch struct {
				Provider *string `json:"provider"`
				Model    *string `json:"model"`
			}
			if err := a.input(args[1:], &patch); err != nil {
				return nil, err
			}
			if patch.Provider == nil && patch.Model == nil {
				return nil, domain.Fail(domain.Invalid, "provide a provider or model preference")
			}
			lock, err := secure.Acquire(a.configFile + ".lock")
			if err != nil {
				return nil, err
			}
			defer lock.Close()
			c, err := config.LoadWorker(a.configFile)
			if err != nil {
				return nil, err
			}
			if patch.Provider != nil && *patch.Provider != c.Provider {
				c.Provider = *patch.Provider
				c.Model = ""
			}
			if patch.Model != nil {
				c.Model = *patch.Model
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			return map[string]string{"provider": c.Provider, "model": c.Model}, config.Write(a.configFile, c)
		}
	case "knowledge":
		switch args[0] {
		case "put":
			var req local.KnowledgeChange
			if err := a.input(args[1:], &req); err != nil {
				return nil, err
			}
			return r.state.PutKnowledge(ctx, req)
		case "list":
			fs := flag.NewFlagSet("knowledge list", flag.ContinueOnError)
			archived := fs.Bool("include-archived", false, "include archives")
			if err := parse(fs, args[1:]); err != nil {
				return nil, err
			}
			return r.state.KnowledgeList(ctx, *archived)
		case "show", "history":
			if err := requireArgs(args, 2); err != nil {
				return nil, err
			}
			if args[0] == "history" {
				return r.state.KnowledgeHistory(ctx, args[1])
			}
			return r.state.Knowledge(ctx, args[1])
		}
	case "inventory":
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		if args[0] == "refresh" {
			paths, err := provider.New(r.config).Paths()
			if err != nil {
				return nil, err
			}
			return r.state.RefreshInventory(ctx, paths)
		}
		if args[0] == "show" {
			return r.state.Inventory(ctx)
		}
	case "workspace":
		switch args[0] {
		case "list":
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return r.state.Bindings(ctx)
		case "bind":
			fs := flag.NewFlagSet("workspace bind", flag.ContinueOnError)
			path := fs.String("path", "", "owned workspace")
			allow := fs.Bool("allow-tools", false, "explicit local tool authorization")
			timeout := fs.Int("timeout", r.config.InvocationTimeoutSeconds, "maximum execution seconds")
			if err := parse(fs, args[1:]); err != nil {
				return nil, err
			}
			if *path == "" {
				return nil, domain.Fail(domain.Invalid, "--path is required")
			}
			return r.state.BindWorkspace(ctx, *path, *allow, *timeout, []string{filepath.Dir(a.configFile), filepath.Dir(r.config.CredentialFile), filepath.Dir(r.config.CAFile)})
		case "enable", "disable":
			if err := requireArgs(args, 2); err != nil {
				return nil, err
			}
			err := r.state.EnableWorkspace(ctx, args[1], args[0] == "enable")
			return map[string]bool{"enabled": args[0] == "enable"}, err
		}
	case "provider":
		return a.providerCommand(ctx, r, args)
	case "service":
		if args[0] == "status" {
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return a.localStatus(ctx, r)
		}
	case "work":
		switch args[0] {
		case "status":
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return a.localStatus(ctx, r)
		case "run":
			if err := requireArgs(args, 1); err != nil {
				return nil, err
			}
			return a.engine(r).Step(work.WithRequestedAction(ctx, a.operation), true)
		case "recover":
			fs := flag.NewFlagSet("work recover", flag.ContinueOnError)
			journal := fs.String("journal", "", "interrupted invocation ID")
			session := fs.String("session", "", "interrupted interactive session ID")
			stopped := fs.Bool("acknowledge-stopped", false, "confirm all owned processes have stopped")
			if err := parse(fs, args[1:]); err != nil {
				return nil, err
			}
			if (*journal == "") == (*session == "") || !*stopped {
				return nil, domain.Fail(domain.Prerequisite, "choose --journal or --session and explicitly --acknowledge-stopped")
			}
			if *journal != "" {
				err := a.engine(r).Recover(ctx, *journal, *stopped)
				return map[string]bool{"reconciled": err == nil}, err
			}
			slot, err := secure.Acquire(filepath.Join(r.config.StateDir, "work.lock"))
			if err != nil {
				return nil, err
			}
			defer slot.Close()
			var fence remote.Fence
			found, err := r.state.Get(ctx, "session-fence", &fence)
			if err != nil {
				return nil, err
			}
			if !found || !fence.Active || fence.ID != *session {
				return nil, domain.Fail(domain.Conflict, "exact active interactive fence does not match")
			}
			err = r.state.Put(ctx, "session-fence", remote.Fence{})
			return map[string]bool{"stopped_acknowledged": err == nil}, err
		}
	}
	return nil, domain.Fail(domain.Invalid, "unknown local command")
}

func (a *app) nativeService(ctx context.Context, args []string, role string) (any, error) {
	if len(args) == 0 || !slices.Contains([]string{"plan", "install"}, args[0]) {
		return nil, domain.Fail(domain.Invalid, "service requires plan or install")
	}
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	input := fs.String("input", "", "native service options JSON")
	apply := fs.Bool("apply", false, "explicitly register/start the OS service")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if *input == "" {
		return nil, domain.Fail(domain.Invalid, "--input FILE is required")
	}
	var options platform.Options
	if err := inputFile(*input, a.in, &options); err != nil {
		return nil, err
	}
	if options.Role != "" && options.Role != role {
		return nil, domain.Fail(domain.Invalid, "service role disagrees with this binary")
	}
	options.Role = role
	if options.OS == "" {
		options.OS = runtime.GOOS
	}
	if options.ConfigFile == "" {
		options.ConfigFile = a.configFile
	}
	definition, err := platform.Render(options)
	if err != nil {
		return nil, err
	}
	if args[0] == "plan" {
		if *apply {
			return nil, domain.Fail(domain.Invalid, "plan never applies host changes")
		}
		return definition, nil
	}
	if !*apply {
		return nil, domain.Fail(domain.Prerequisite, "service registration changes the host; use service plan, then explicit --apply")
	}
	return definition, platform.Install(ctx, options, process.Native{})
}
