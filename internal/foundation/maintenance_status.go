package foundation

import (
	"context"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) MaintenanceReservation(ctx context.Context, actor, id string) (domain.MaintenanceStatus, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.MaintenanceStatus{}, err
	}
	reservation, err := get[domain.MaintenanceReservation](ctx, s.db, "maintenance_reservations", id)
	if err != nil {
		return domain.MaintenanceStatus{}, err
	}
	if reservation.AgentID != actor {
		return domain.MaintenanceStatus{}, domain.Fail(domain.Denied, "maintenance reservation is private to its admitted actor")
	}
	return domain.MaintenanceStatus{Reservation: reservation, ServerObservedAt: s.timestamp()}, nil
}
