package cli

import (
	"context"
	"flag"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

func isLocalOnlyModelCommand(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "status":
		return len(args) == 1
	case "init":
		rest := args[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return false
		}
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--agent" || strings.HasPrefix(rest[i], "--agent=") {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func (a *app) localModelCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "local-model requires init or status")
	}
	switch args[0] {
	case "status":
		return a.localModelStatus(ctx, r, args[1:])
	case "init":
		return a.localModelInit(ctx, r, args[1:])
	default:
		return nil, domain.Fail(domain.Invalid, "unknown local-model command")
	}
}

func (a *app) localModelStatus(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return provider.New(r.config).LocalStatus(ctx)
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return nil, domain.Fail(domain.Invalid, "local-model status accepts at most one AGENT_ID or OPERATION_ID")
	}
	target := args[0]
	if !domain.ValidOperation(target) {
		return nil, domain.Fail(domain.Invalid, "invalid identifier")
	}
	if strings.HasPrefix(target, "agent_operation_") {
		operation, err := api.Call[domain.AgentOperation](ctx, r.client, http.MethodGet, api.Path("agent-operations", target), nil)
		if err != nil {
			return nil, err
		}
		return agentOperationOutcome(operation)
	}
	options, err := api.Call[domain.AgentModelOptions](ctx, r.client, http.MethodGet, api.Path("agents", target, "model-options"), nil)
	if err != nil {
		return nil, err
	}
	prefs, err := api.Call[domain.ProviderPreferences](ctx, r.client, http.MethodGet, api.Path("agents", target, "preferences"), nil)
	if err != nil {
		return nil, err
	}
	var localComputers []map[string]any
	for _, comp := range options.Computers {
		for _, p := range comp.Observation.Providers {
			if p.Provider == "local" {
				localComputers = append(localComputers, map[string]any{
					"binding_id":     comp.BindingID,
					"device_name":    comp.DeviceName,
					"generation":     comp.Generation,
					"received_at":    comp.ReceivedAt,
					"stale":          comp.Stale,
					"version":        p.Version,
					"authentication": p.Authentication,
					"models":         p.Models,
					"problem_code":   p.ProblemCode,
				})
			}
		}
	}
	if localComputers == nil {
		localComputers = []map[string]any{}
	}
	return map[string]any{
		"agent_id":           options.AgentID,
		"server_observed_at": options.ServerObservedAt,
		"preferences":        prefs,
		"local_computers":    localComputers,
	}, nil
}

func (a *app) localModelInit(ctx context.Context, r *resources, args []string) (any, error) {
	targetID := ""
	tail := args
	if len(tail) > 0 && !strings.HasPrefix(tail[0], "-") {
		if !domain.ValidOperation(tail[0]) {
			return nil, domain.Fail(domain.Invalid, "invalid target agent ID")
		}
		targetID = tail[0]
		tail = tail[1:]
	}

	fs := flag.NewFlagSet("local-model init", flag.ContinueOnError)
	agentFlag := fs.String("agent", "", "optional target AGENT_ID for a Founder-authorized remote local model initialization")
	inputFlag := fs.String("input", "", "optional JSON LocalModelInitRequest file or -")
	modelFlag := fs.String("model", "", "local model identifier (default qwen3.8:27b)")
	quantizedFlag := fs.Bool("quantized", false, "select a quantized model variant to fit hardware memory constraints")
	quantizationFlag := fs.String("quantization", "", "explicit quantization: auto, q4_K_M, q8_0, or bf16")
	setPrefFlag := fs.Bool("set-preference", false, "expose and set this local model as the agent's preferred model")
	reasonFlag := fs.String("reason", "", "nonsecret reason for Founder request or preference update")
	if err := parse(fs, tail); err != nil {
		return nil, err
	}
	if *agentFlag != "" {
		if targetID != "" && targetID != *agentFlag {
			return nil, domain.Fail(domain.Invalid, "conflicting positional AGENT_ID and --agent flag")
		}
		if !domain.ValidOperation(*agentFlag) {
			return nil, domain.Fail(domain.Invalid, "invalid --agent identifier")
		}
		targetID = *agentFlag
	}

	var req domain.LocalModelInitRequest
	if *inputFlag != "" {
		if *modelFlag != "" || *quantizedFlag || *quantizationFlag != "" || *setPrefFlag || *reasonFlag != "" {
			return nil, domain.Fail(domain.Invalid, "do not combine --input with individual model flags")
		}
		if err := inputFile(*inputFlag, a.in, &req); err != nil {
			return nil, err
		}
	} else {
		model := *modelFlag
		if strings.TrimSpace(model) == "" {
			model = domain.Qwen38Family
		}
		reason := strings.TrimSpace(*reasonFlag)
		if reason == "" {
			reason = "Initialize local model " + model
		}
		req = domain.LocalModelInitRequest{
			Model:         model,
			Quantized:     *quantizedFlag,
			Quantization:  *quantizationFlag,
			SetPreference: *setPrefFlag,
			Reason:        reason,
		}
	}

	if targetID != "" {
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		if _, err := domain.ValidateLocalModelInitRequest(req); err != nil {
			return nil, err
		}
		return a.writeAgentOperation(ctx, r, api.Path("agents", targetID, "local-models"), req)
	}

	spec, err := domain.ParseLocalModelSpec(req.Model, req.Quantized, req.Quantization, req.SetPreference)
	if err != nil {
		return nil, err
	}
	manager := provider.New(r.config)
	result, err := manager.InitializeLocalModel(ctx, spec)
	if err != nil {
		return nil, err
	}

	if r.authenticate(ctx) == nil && r.client != nil {
		catalog, catErr := provider.CollectCatalog(ctx, manager, func() time.Time { return time.Now().UTC() })
		if catErr == nil {
			computer, pubErr := api.Call[domain.ComputerModelOptions](ctx, r.client, http.MethodPost,
				api.Path("agents", r.client.Credential.AgentID, "model-options"),
				domain.ProviderCatalogRequest{
					Command:     domain.Command{OperationID: a.operation + ".model-catalog"},
					Observation: catalog,
				})
			if pubErr == nil {
				_ = r.state.Put(ctx, "provider-model-catalog", catalog)
				result.CatalogPublished = true
				if spec.SetPreference {
					prefPath := api.Path("agents", r.client.Credential.AgentID, "preferences")
					current, getErr := api.Call[domain.ProviderPreferences](ctx, r.client, http.MethodGet, prefPath, nil)
					if getErr == nil {
						provName := "local"
						modelName := result.Model
						reason := strings.TrimSpace(req.Reason)
						if reason == "" {
							reason = "Select initialized local model " + modelName
						}
						saved, setErr := api.Call[domain.ProviderPreferences](ctx, r.client, http.MethodPost, prefPath, domain.AgentPreferencesRequest{
							Command:  domain.Command{OperationID: a.operation + ".preferences", ExpectedRevision: current.Revision},
							Provider: &provName,
							Model:    &modelName,
							Reason:   reason,
							Catalog:  &domain.ProviderCatalogBinding{BindingID: computer.BindingID, Generation: computer.Generation},
						})
						if setErr == nil {
							result.PreferenceApplied = true
							result.Preferences = &saved
						}
					}
				}
			}
		}
	}

	if spec.SetPreference && !result.PreferenceApplied {
		lock, lockErr := secure.Acquire(filepath.Clean(a.configFile) + ".lock")
		if lockErr == nil {
			defer lock.Close()
			var currentCfg config.Worker
			if config.Read(a.configFile, &currentCfg) == nil {
				currentCfg.Provider = "local"
				currentCfg.Model = result.Model
				if config.Write(a.configFile, currentCfg) == nil {
					result.PreferenceApplied = true
				}
			}
		}
	}
	return result, nil
}
