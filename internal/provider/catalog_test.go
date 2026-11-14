package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type catalogExecutor struct{ calls []string }

func (e *catalogExecutor) Check(_ context.Context, name string) (Health, error) {
	e.calls = append(e.calls, name)
	switch name {
	case "copilot":
		return Health{Provider: name, Version: "1.0.1", Authentication: "authenticated", Models: []string{"authenticated-model"}}, nil
	case "codex":
		return Health{Provider: name, Version: "1.0.1", Authentication: "credentials-present-unverified", Models: []string{"not-an-authenticated-option"}}, nil
	default:
		return Health{}, domain.Fail(domain.Prerequisite, "PRIVATE_PROVIDER_PATH")
	}
}

func (*catalogExecutor) Invoke(context.Context, Invocation) (Reply, error) {
	return Reply{}, errors.New("catalog must never invoke a model")
}

func TestReadOnlyCatalogIncludesOnlyAuthenticatedOptionsAndSafeFailures(t *testing.T) {
	executor := &catalogExecutor{}
	catalog, err := CollectCatalog(context.Background(), executor, func() time.Time { return time.Unix(1000, 0) })
	if err != nil || len(catalog.Providers) != len(domain.ProviderNames()) {
		t.Fatal("read-only provider observation failed", catalog, err)
	}
	for _, p := range catalog.Providers {
		if p.Provider == "copilot" {
			if len(p.Models) != 1 || p.Models[0] != "authenticated-model" {
				t.Fatal("authenticated model catalog was omitted", p)
			}
		} else if len(p.Models) != 0 {
			t.Fatal("unverified/missing provider became a model option", p)
		}
	}
	if catalog.Providers[2].ProblemCode != domain.Prerequisite || catalog.Providers[2].Authentication != "unknown" {
		t.Fatal("missing provider was hidden or treated as authenticated")
	}
}
