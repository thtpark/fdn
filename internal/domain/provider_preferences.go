package domain

type ProviderPreferences struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Revision  int64  `json:"revision"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type AgentPreferencesRequest struct {
	Command
	Provider *string                 `json:"provider,omitempty"`
	Model    *string                 `json:"model,omitempty"`
	Reason   string                  `json:"reason"`
	Catalog  *ProviderCatalogBinding `json:"catalog,omitempty"`
}

func ValidateProviderModel(provider, model string) error {
	if provider != "" && !ValidProvider(provider) {
		return Fail(Invalid, "provider must be codex, copilot, antigravity, or empty to inherit")
	}
	if model != "" && provider == "" {
		return Fail(Invalid, "a model preference requires an explicit provider")
	}
	return CheckText(model, "preferred model", 256, false)
}
