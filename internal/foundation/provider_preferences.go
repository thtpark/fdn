package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) AgentPreferences(ctx context.Context, actor, id string) (domain.ProviderPreferences, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.ProviderPreferences{}, err
	}
	target, err := activeIdentitySubject(ctx, s.db, id)
	if err != nil {
		return domain.ProviderPreferences{}, err
	}
	if target.Preferences == nil {
		return domain.ProviderPreferences{Revision: 1}, nil
	}
	return *target.Preferences, nil
}

func (s *Store) SetAgentPreferences(ctx context.Context, actor, id string, req domain.AgentPreferencesRequest) (domain.ProviderPreferences, error) {
	return mutate(ctx, s, actor, "agent.preferences:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ProviderPreferences, error) {
		if !a.Founder && a.ID != id {
			return domain.ProviderPreferences{}, domain.Fail(domain.Denied, "only Founder or the agent itself may change its preferred provider and model")
		}
		target, err := activeIdentitySubject(ctx, tx, id)
		if err != nil {
			return domain.ProviderPreferences{}, err
		}
		preferences := domain.ProviderPreferences{Revision: 1}
		if target.Preferences != nil {
			preferences = *target.Preferences
		}
		if err := expect(preferences.Revision, req.ExpectedRevision); err != nil {
			return preferences, err
		}
		if req.Provider == nil && req.Model == nil {
			return preferences, domain.Fail(domain.Invalid, "provide a provider or model preference")
		}
		if err := domain.CheckText(req.Reason, "preference change reason", 8192, true); err != nil {
			return preferences, err
		}
		if req.Provider != nil && *req.Provider != preferences.Provider {
			preferences.Provider, preferences.Model = *req.Provider, ""
		}
		if req.Model != nil {
			preferences.Model = *req.Model
		}
		if err := domain.ValidateProviderModel(preferences.Provider, preferences.Model); err != nil {
			return preferences, err
		}
		if req.Catalog != nil {
			if err := s.validateModelChoice(ctx, tx, id, *req.Catalog, preferences.Provider, preferences.Model); err != nil {
				return preferences, err
			}
		}
		preferences.Revision++
		preferences.UpdatedBy, preferences.UpdatedAt, preferences.Reason = a.ID, s.timestamp(), req.Reason
		target.Preferences = &preferences
		data, err := encode(target)
		if err != nil {
			return preferences, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET data=? WHERE id=?", data, target.ID); err != nil {
			return preferences, err
		}
		if err := s.history(ctx, tx, target.ID, a.ID, "preferences", target, req.Reason); err != nil {
			return preferences, err
		}
		return preferences, s.event(ctx, tx, "agent", target.ID, "", "")
	})
}
