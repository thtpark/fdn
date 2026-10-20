package foundation

import (
	"context"
	"database/sql"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) RotateBootstrap(ctx context.Context, hash string, expires int64, operation string) (domain.Invitation, error) {
	return transact(ctx, s, "local-administrator", "bootstrap.rotate", operation, struct {
		Hash    string
		Expires int64
	}{hash, expires}, func(tx *sql.Tx) (domain.Invitation, error) {
		if !domain.ValidHash(hash) || expires <= s.timestamp() || expires > s.timestamp()+86400 {
			return domain.Invitation{}, domain.Fail(domain.Invalid, "invalid bootstrap hash/expiry")
		}
		var founders int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE json_extract(data,'$.founder') = 1").Scan(&founders); err != nil {
			return domain.Invitation{}, err
		}
		if founders != 0 {
			return domain.Invitation{}, domain.Fail(domain.Denied, "bootstrap rotation is forbidden after Founder enrollment")
		}
		rows, err := tx.QueryContext(ctx, "SELECT data FROM invitations WHERE json_extract(data,'$.founder') = 1")
		if err != nil {
			return domain.Invitation{}, err
		}
		invitations, err := scanJSON[domain.Invitation](rows)
		if err != nil {
			return domain.Invitation{}, err
		}
		for _, inv := range invitations {
			inv.ExpiresAt = s.timestamp()
			data, err := encode(inv)
			if err != nil {
				return domain.Invitation{}, err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE invitations SET data = ? WHERE id = ?", data, inv.ID); err != nil {
				return domain.Invitation{}, err
			}
		}
		inv := domain.Invitation{ID: domain.NewID("invitation"), Founder: true, ExpiresAt: expires}
		data, err := encode(inv)
		if err != nil {
			return inv, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO invitations(id,data,token_hash) VALUES(?,?,?)", inv.ID, data, hash)
		return inv, err
	})
}
