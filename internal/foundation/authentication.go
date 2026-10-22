package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) RequestAuthentication(ctx context.Context, actor, target string, req domain.AuthenticationRefreshRequest) (domain.Message, error) {
	return mutate(ctx, s, actor, "authentication.request:"+target, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Message, error) {
		if !a.Founder {
			return domain.Message{}, domain.Fail(domain.Denied, "only Founder may request a provider sign-in refresh")
		}
		if _, err := activeIdentitySubject(ctx, tx, target); err != nil {
			return domain.Message{}, err
		}
		if !domain.ValidProvider(req.Provider) {
			return domain.Message{}, domain.Fail(domain.Invalid, "authentication refresh requires codex or copilot")
		}
		if err := domain.CheckText(req.Reason, "nonsecret authentication refresh reason", 8192, true); err != nil {
			return domain.Message{}, err
		}
		if req.ProjectID != "" {
			project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
			if err != nil {
				return domain.Message{}, err
			}
			if project.Archived {
				return domain.Message{}, domain.Fail(domain.Denied, "authentication refresh project is archived")
			}
		}
		now := s.timestamp()
		message := domain.Message{
			ID: domain.NewID("message"), SenderID: a.ID, RecipientID: target,
			Body:   "Founder requested a fresh " + req.Provider + " sign-in status notice if sign-in is required. This is not a sign-in, assistance grant, mode change, or inference request.",
			Origin: "service", Kind: "authentication-refresh", Urgent: true,
			State: "pending", CreatedAt: now, ExpiresAt: now + domain.AuthenticationRequestTTL, Revision: 1,
			AuthenticationRefresh: &domain.AuthenticationRefresh{Provider: req.Provider, ProjectID: req.ProjectID, Reason: req.Reason},
		}
		return insertMessage(ctx, tx, message)
	})
}
