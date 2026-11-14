package foundation

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/thtpark/fdn/internal/domain"
)

const providerCatalogKey = "provider.catalog:"

func (s *Store) PublishProviderCatalog(ctx context.Context, actor string, req domain.ProviderCatalogRequest) (domain.ComputerModelOptions, error) {
	return mutate(ctx, s, actor, "agent.model-options", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ComputerModelOptions, error) {
		var result domain.ComputerModelOptions
		if err := domain.ValidateProviderCatalog(req.Observation); err != nil {
			return result, err
		}
		binding := a.ID
		if ci, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok {
			binding = ci.BindingID()
		}
		identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", binding)
		if err != nil {
			return result, err
		}
		if identity.AgentID != a.ID || identity.Generation <= 0 || identity.State != "active" {
			return result, domain.Fail(domain.Denied, "provider catalogs require this computer's current enrolled identity")
		}
		old, err := healthSetting[domain.ComputerModelOptions](ctx, tx, providerCatalogKey+binding)
		if err != nil {
			return result, err
		}
		if old.Generation == identity.Generation && old.Observation.ObservedAt > req.Observation.ObservedAt {
			return result, domain.Fail(domain.Conflict, "provider catalog observation is older than the retained computer report")
		}
		if old.BindingID == binding && old.Generation == identity.Generation && reflect.DeepEqual(old.Observation, req.Observation) {
			old.Stale = s.timestamp()-old.ReceivedAt > domain.ProviderCatalogFreshSeconds
			return old, nil
		}
		name := identity.DeviceName
		if name == "" {
			name = a.Name
		}
		result = domain.ComputerModelOptions{BindingID: binding, DeviceName: name, Generation: identity.Generation,
			ReceivedAt: s.timestamp(), Observation: req.Observation}
		if err := saveHealthSetting(ctx, tx, providerCatalogKey+binding, result); err != nil {
			return result, err
		}
		return result, s.event(ctx, tx, "agent-model-options", a.ID, "", "")
	})
}

func (s *Store) AgentModelOptions(ctx context.Context, actor, target string) (result domain.AgentModelOptions, err error) {
	result = domain.AgentModelOptions{AgentID: target, ServerObservedAt: s.timestamp(), Computers: []domain.ComputerModelOptions{}}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	reader, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return result, err
	}
	agent, err := activeIdentitySubject(ctx, snapshot, target)
	if err != nil {
		return result, err
	}
	if agent.Founder && !reader.Founder {
		return result, domain.Fail(domain.Denied, "Founder computer model discovery remains Founder-only")
	}
	rows, err := snapshot.QueryContext(ctx, "SELECT data FROM agent_identities WHERE agent_id=? ORDER BY id", target)
	if err != nil {
		return result, err
	}
	identities, err := scanJSON[domain.AgentIdentity](rows)
	if err != nil {
		return result, err
	}
	for _, identity := range identities {
		if identity.State != "active" {
			continue
		}
		report, err := healthSetting[domain.ComputerModelOptions](ctx, snapshot, providerCatalogKey+identity.BindingID())
		if err != nil {
			return result, err
		}
		if report.BindingID == "" || report.Generation != identity.Generation {
			continue
		}
		if report.BindingID != identity.BindingID() {
			return result, domain.Fail(domain.Internal, "provider model catalog belongs to another computer")
		}
		report.Stale = result.ServerObservedAt-report.ReceivedAt > domain.ProviderCatalogFreshSeconds
		result.Computers = append(result.Computers, report)
	}
	return result, nil
}

func (s *Store) validateModelChoice(ctx context.Context, q queryer, target string, binding domain.ProviderCatalogBinding, provider, model string) error {
	if !domain.ValidOperation(binding.BindingID) || binding.Generation <= 0 || provider == "" {
		return domain.Fail(domain.Invalid, "authenticated model selection requires an exact enrolled computer and provider")
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", binding.BindingID)
	if err != nil {
		return err
	}
	if identity.AgentID != target || identity.Generation != binding.Generation || identity.State != "active" {
		return domain.Fail(domain.Denied, "the selected model catalog is not this agent's current computer")
	}
	report, err := healthSetting[domain.ComputerModelOptions](ctx, q, providerCatalogKey+binding.BindingID)
	if err != nil {
		return err
	}
	if report.BindingID != binding.BindingID || report.Generation != binding.Generation ||
		!report.Allows(provider, model, s.timestamp()) {
		return domain.Fail(domain.Prerequisite, "the selected provider/model has no fresh authenticated observation on that computer")
	}
	return nil
}
