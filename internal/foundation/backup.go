package foundation

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/secure"
)

func (s *Store) Snapshot(ctx context.Context, path string) (err error) {
	if err := secure.Create(path, nil); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "VACUUM INTO ?", absolute)
	return err
}
