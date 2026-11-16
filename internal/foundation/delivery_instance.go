package foundation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
)

func deliveryInstance(ctx context.Context, q queryer) (string, error) {
	var instance string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='instance_id'").Scan(&instance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", domain.Fail(domain.Prerequisite, "Foundation instance must be initialized before software operations")
		}
		return "", err
	}
	if instance == "" {
		return "", domain.Fail(domain.Internal, "Foundation instance identity is missing")
	}
	return instance, nil
}
