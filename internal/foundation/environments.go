package foundation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

const environmentReportKey = "environment.report:"

func (s *Store) PublishEnvironment(ctx context.Context, actor string, req domain.EnvironmentRequest) (domain.ComputerEnvironment, error) {
	return mutate(ctx, s, actor, "agent.environment", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ComputerEnvironment, error) {
		var result domain.ComputerEnvironment
		if err := domain.ValidateEnvironment(req.Observation); err != nil {
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
			return result, domain.Fail(domain.Denied, "environment reporter must be this computer's current enrolled identity")
		}
		name := identity.DeviceName
		if name == "" {
			name = a.Name
		}
		result = domain.ComputerEnvironment{BindingID: binding, DeviceName: name, Generation: identity.Generation,
			ReceivedAt: s.timestamp(), Observation: req.Observation}
		return result, saveHealthSetting(ctx, tx, environmentReportKey+binding, result)
	})
}

func (s *Store) AgentEnvironment(ctx context.Context, actor, target string) (result domain.AgentEnvironment, err error) {
	result = domain.AgentEnvironment{AgentID: target, ServerObservedAt: s.timestamp(), Computers: []domain.ComputerEnvironment{}}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	reader, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return result, err
	}
	agent, err := get[domain.Agent](ctx, snapshot, "agents", target)
	if err != nil {
		return result, err
	}
	if agent.Founder && !reader.Founder {
		return result, domain.Fail(domain.Denied, "Founder computer discovery remains Founder-only")
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
		if identity.State != "active" || !agent.Active || agent.Banned {
			continue
		}
		report, err := healthSetting[domain.ComputerEnvironment](ctx, snapshot, environmentReportKey+identity.BindingID())
		if err != nil {
			return result, err
		}
		if report.BindingID == "" || report.Generation != identity.Generation {
			continue
		}
		if report.BindingID != identity.BindingID() {
			return result, domain.Fail(domain.Internal, "environment report is bound to another computer")
		}
		report.Stale = result.ServerObservedAt-report.ReceivedAt > domain.EnvironmentFreshSeconds
		result.Computers = append(result.Computers, report)
	}
	return result, nil
}
