package domain

import "slices"

const ProviderCatalogVersion = 1
const ProviderCatalogFreshSeconds int64 = 90
const MaxProviderModels = 1000

func ValidModelID(value string) bool { return healthModelPattern.MatchString(value) }

type ProviderModelCatalog struct {
	Provider       string   `json:"provider"`
	Version        string   `json:"version,omitempty"`
	Authentication string   `json:"authentication"`
	ObservedAt     int64    `json:"observed_at"`
	Models         []string `json:"models"`
	ProblemCode    Code     `json:"problem_code,omitempty"`
	ModelProblem   Code     `json:"model_problem,omitempty"`
}

type ProviderCatalogObservation struct {
	Version    int                    `json:"version"`
	ObservedAt int64                  `json:"observed_at"`
	Providers  []ProviderModelCatalog `json:"providers"`
}

type ProviderCatalogRequest struct {
	Command
	Observation ProviderCatalogObservation `json:"observation"`
}

type ComputerModelOptions struct {
	BindingID   string                     `json:"binding_id"`
	DeviceName  string                     `json:"device_name"`
	Generation  int64                      `json:"generation"`
	ReceivedAt  int64                      `json:"received_at"`
	Stale       bool                       `json:"stale"`
	Observation ProviderCatalogObservation `json:"observation"`
}

type AgentModelOptions struct {
	AgentID          string                 `json:"agent_id"`
	ServerObservedAt int64                  `json:"server_observed_at"`
	Computers        []ComputerModelOptions `json:"computers"`
}

type ProviderCatalogBinding struct {
	BindingID  string `json:"binding_id"`
	Generation int64  `json:"generation"`
}

func ValidProviderAuthentication(value string) bool {
	return slices.Contains([]string{"unknown", "authenticated", "login-required", "credentials-present-unverified", "credentials-rejected", "signed-in", "sign-in-required"}, value)
}

func ValidateProviderCatalog(o ProviderCatalogObservation) error {
	if o.Version != ProviderCatalogVersion || o.ObservedAt <= 0 || o.ObservedAt > maxHealthTimestamp ||
		len(o.Providers) > len(ProviderNames()) {
		return Fail(Invalid, "provider catalog requires a supported version, bounded clock and provider count")
	}
	seen := make(map[string]bool)
	total := 0
	for _, p := range o.Providers {
		if !ValidProvider(p.Provider) || seen[p.Provider] || !ValidHealthVersion(p.Version) ||
			!ValidProviderAuthentication(p.Authentication) || p.ObservedAt <= 0 || p.ObservedAt > o.ObservedAt ||
			(p.ProblemCode != "" && !safeObservationProblem(p.ProblemCode)) ||
			(p.ModelProblem != "" && !safeObservationProblem(p.ModelProblem)) {
			return Fail(Invalid, "provider catalog observations must be unique, bounded safe provider facts")
		}
		seen[p.Provider] = true
		if p.ProblemCode != "" && p.Authentication != "unknown" {
			return Fail(Invalid, "a failed provider probe cannot assert authentication")
		}
		if len(p.Models) != 0 && (p.Authentication != "authenticated" || p.ModelProblem != "" || p.ProblemCode != "") {
			return Fail(Invalid, "model options require an authenticated successful provider catalog")
		}
		models := make(map[string]bool)
		for _, model := range p.Models {
			if !ValidModelID(model) || models[model] {
				return Fail(Invalid, "provider model options must be unique safe model identifiers")
			}
			models[model] = true
		}
		total += len(p.Models)
		if total > MaxProviderModels {
			return Fail(Invalid, "provider model options exceed the bounded catalog")
		}
	}
	return nil
}

func (c ComputerModelOptions) Allows(provider, model string, now int64) bool {
	if c.Stale || c.ReceivedAt <= 0 || now < c.ReceivedAt ||
		now-c.ReceivedAt > ProviderCatalogFreshSeconds {
		return false
	}
	for _, p := range c.Observation.Providers {
		if p.Provider == provider && p.Authentication == "authenticated" && p.ProblemCode == "" &&
			HealthAge(c.Observation.ObservedAt, p.ObservedAt, c.ReceivedAt, now) <= ProviderCatalogFreshSeconds {
			return model == "" || (p.ModelProblem == "" && slices.Contains(p.Models, model))
		}
	}
	return false
}

var copilotLongContextModels = []string{
	"claude-fable-5", "claude-fable-5.1", "claude-haiku-5.5", "claude-opus-5.5", "claude-sonnet-5.5",
	"gemini-3.8-flash", "gemini-4-argon", "gemini-4-carbon", "gemini-4-neon",
	"gpt-5.6-sol", "gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6.2-astra", "gpt-6.2-sol",
	"grok-4.6", "grok-4.7",
}

func SupportsCopilotLongContextTier(model string) bool {
	return slices.Contains(copilotLongContextModels, model)
}

func MaxPromptBytesForModel(provider, model string) int {
	if provider == "antigravity" || len(model) >= 6 && model[:6] == "gemini" {
		return 8 << 20
	}
	if SupportsCopilotLongContextTier(model) {
		return 8 << 20
	}
	if provider == "local" {
		return 4 << 20
	}
	return 4 << 20
}
