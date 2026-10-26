package foundation

import (
	"context"
	"database/sql"
	"errors"
)

// database.Open uses immediate write transactions. Explicit deferred BEGIN on a
// leased connection gives readers one WAL snapshot without taking the writer lock.
type readSnapshot struct {
	*sql.Conn
}

func (s *Store) readSnapshot(ctx context.Context) (*readSnapshot, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return &readSnapshot{Conn: conn}, nil
}

func (r *readSnapshot) Close() error {
	_, err := r.Conn.ExecContext(context.Background(), "ROLLBACK")
	return errors.Join(err, r.Conn.Close())
}
