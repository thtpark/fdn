package provider

import (
	"context"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func CollectCatalog(ctx context.Context, executor Executor, now func() time.Time) (domain.ProviderCatalogObservation, error) {
	catalog := domain.ProviderCatalogObservation{Version: domain.ProviderCatalogVersion, Providers: []domain.ProviderModelCatalog{}}
	for _, name := range domain.ProviderNames() {
		if err := ctx.Err(); err != nil {
			return catalog, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		health, err := executor.Check(probeCtx, name)
		cancel()
		if ctx.Err() != nil {
			return catalog, ctx.Err()
		}
		observation := domain.ProviderModelCatalog{Provider: name, Authentication: "unknown", ObservedAt: now().UTC().Unix(), Models: []string{}}
		if err != nil {
			observation.ProblemCode = domain.AsFault(err).Code
		} else {
			if !domain.ValidProviderAuthentication(health.Authentication) || !domain.ValidHealthVersion(health.Version) {
				return catalog, domain.Fail(domain.Invalid, "provider returned an unsupported authentication or version observation")
			}
			observation.Authentication, observation.Version = health.Authentication, health.Version
			if health.ModelCatalogError != "" {
				observation.ModelProblem = domain.Unavailable
			} else if health.Authentication == "authenticated" {
				observation.Models = append(observation.Models, health.Models...)
			}
		}
		catalog.Providers = append(catalog.Providers, observation)
	}
	catalog.ObservedAt = now().UTC().Unix()
	return catalog, domain.ValidateProviderCatalog(catalog)
}
