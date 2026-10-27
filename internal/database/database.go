package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
	_ "modernc.org/sqlite"
)

func Open(ctx context.Context, path string, migrations []string, minimumVersion int) (*sql.DB, error) {
	if minimumVersion < 1 || minimumVersion > len(migrations) {
		return nil, domain.Fail(domain.Invalid, "database requires a supported minimum schema version")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := secure.Dir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, domain.Fail(domain.Denied, "database must be a regular owner-only file")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	query := url.Values{
		"_pragma": {"busy_timeout(10000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"},
		"_txlock": {"immediate"},
	}
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := migrate(ctx, db, migrations, minimumVersion); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB, migrations []string, minimumVersion int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return domain.Fail(domain.Prerequisite, "database schema is newer than this binary; upgrade without resetting state")
	}
	if version != 0 && version < minimumVersion {
		return domain.Fail(domain.Prerequisite, "database schema predates the supported baseline; use a current-format database without resetting retained state")
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func Check(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return domain.Fail(domain.Internal, "database integrity check failed")
	}
	return nil
}
