package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) Agent(ctx context.Context, actor, id string) (domain.Agent, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Agent{}, err
	}
	return get[domain.Agent](ctx, s.db, "agents", id)
}

func (s *Store) SetMode(ctx context.Context, actor, id string, req domain.ModeRequest) (domain.Agent, error) {
	return mutate(ctx, s, actor, "agent.mode:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Agent, error) {
		if !a.Founder {
			return domain.Agent{}, domain.Fail(domain.Denied, "only Founder may explicitly pause or resume an agent; delegates cannot change automation mode")
		}
		target, err := s.actor(ctx, tx, id)
		if err != nil {
			return target, err
		}
		if err := expect(target.Revision, req.ExpectedRevision); err != nil {
			return target, err
		}
		if req.Mode != "ai" && req.Mode != "manual" {
			return target, domain.Fail(domain.Invalid, "mode must be ai or manual")
		}
		if err := domain.CheckText(req.Reason, "mode request reason", 8192, true); err != nil {
			return target, err
		}
		target.Mode, target.ModeRequestedBy, target.ModeReason, target.ModeChangedAt = req.Mode, a.ID, req.Reason, s.timestamp()
		target.Revision++
		data, err := encode(target)
		if err != nil {
			return target, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET data = ? WHERE id = ?", data, target.ID); err != nil {
			return target, err
		}
		if err := s.history(ctx, tx, target.ID, a.ID, "mode", target, req.Reason); err != nil {
			return target, err
		}
		return target, s.event(ctx, tx, "agent-mode", target.ID, "", "")
	})
}
