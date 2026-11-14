package domain

import (
	"strings"
	"testing"
)

func catalogFixture() ProviderCatalogObservation {
	return ProviderCatalogObservation{Version: ProviderCatalogVersion, ObservedAt: 1000,
		Providers: []ProviderModelCatalog{{Provider: "copilot", Version: "1.0.1", Authentication: "authenticated",
			ObservedAt: 999, Models: []string{"model-a", "model-b"}}}}
}

func TestProviderCatalogOptionsRequireFreshAuthenticatedComputerFacts(t *testing.T) {
	for _, auth := range []string{"authenticated", "credentials-present-unverified", "login-required", "unknown", "signed-in", "credentials-rejected"} {
		c := ComputerModelOptions{BindingID: "agent_worker", Generation: 1, ReceivedAt: 2000, Observation: catalogFixture()}
		c.Observation.Providers[0].Authentication = auth
		if auth != "authenticated" {
			c.Observation.Providers[0].Models = nil
		}
		if err := ValidateProviderCatalog(c.Observation); err != nil {
			t.Fatal(auth, err)
		}
		if c.Allows("copilot", "", 2001) != (auth == "authenticated") ||
			c.Allows("copilot", "model-a", 2001) != (auth == "authenticated") {
			t.Fatal("unverified or signed-out provider became selectable", auth)
		}
		if c.Allows("copilot", "not-in-catalog", 2001) || c.Allows("copilot", "", 2091) {
			t.Fatal("unknown model or stale observations became selectable", auth)
		}
	}
	c := ComputerModelOptions{BindingID: "agent_worker", Generation: 1, ReceivedAt: 2000, Observation: catalogFixture()}
	c.Observation.Providers[0].ModelProblem = Unavailable
	c.Observation.Providers[0].Models = nil
	if !c.Allows("copilot", "", 2001) || c.Allows("copilot", "model-a", 2001) {
		t.Fatal("catalog failure invented model options or hid explicit provider selection")
	}
}

func TestProviderCatalogRejectsUnsafeAndContradictoryObservations(t *testing.T) {
	for name, change := range map[string]func(*ProviderCatalogObservation){
		"unsupported provider":  func(o *ProviderCatalogObservation) { o.Providers[0].Provider = "unsupported" },
		"raw version":           func(o *ProviderCatalogObservation) { o.Providers[0].Version = "/private/provider" },
		"raw model":             func(o *ProviderCatalogObservation) { o.Providers[0].Models = []string{"private\ntranscript"} },
		"unverified options":    func(o *ProviderCatalogObservation) { o.Providers[0].Authentication = "credentials-present-unverified" },
		"failed auth":           func(o *ProviderCatalogObservation) { o.Providers[0].ProblemCode = Unavailable },
		"failed catalog":        func(o *ProviderCatalogObservation) { o.Providers[0].ModelProblem = Unavailable },
		"duplicate provider":    func(o *ProviderCatalogObservation) { o.Providers = append(o.Providers, o.Providers[0]) },
		"duplicate model":       func(o *ProviderCatalogObservation) { o.Providers[0].Models = []string{"model-a", "model-a"} },
		"oversized model":       func(o *ProviderCatalogObservation) { o.Providers[0].Models = []string{strings.Repeat("x", 257)} },
		"future provider clock": func(o *ProviderCatalogObservation) { o.Providers[0].ObservedAt++; o.Providers[0].ObservedAt++ },
		"unsupported schema":    func(o *ProviderCatalogObservation) { o.Version++ },
	} {
		t.Run(name, func(t *testing.T) {
			o := catalogFixture()
			change(&o)
			if err := ValidateProviderCatalog(o); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("unsafe provider facts were accepted", err)
			}
		})
	}
}
