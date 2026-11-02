package foundation

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func (s *Store) Snapshot(ctx context.Context, path string) (err error) {
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := s.reconcileFilePermissions(ctx); err != nil {
		return err
	}
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
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return err
	}
	if s.files == nil {
		return nil
	}
	image, err := database.Open(ctx, absolute, serverSchemas(), 5)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	tx, err := image.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT json_set(data,'$.uploaded_bytes',uploaded_bytes) FROM files WHERE state<>'deleted' ORDER BY id")
	if err != nil {
		return err
	}
	files, err := scanJSON[domain.SharedFile](rows)
	if err != nil {
		return err
	}
	// Only the isolated archive image contains BLOBs. The live store continues
	// to have one filesystem copy, and retained manifest-2 backups stay usable.
	for _, file := range files {
		rows, err := tx.QueryContext(ctx, "SELECT offset,sha256 FROM file_chunks WHERE file_id=? ORDER BY offset", file.ID)
		if err != nil {
			return err
		}
		type descriptor struct {
			offset int64
			digest string
		}
		var chunks []descriptor
		for rows.Next() {
			var chunk descriptor
			if err := rows.Scan(&chunk.offset, &chunk.digest); err != nil {
				rows.Close()
				return err
			}
			chunks = append(chunks, chunk)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		var size int64
		for _, chunk := range chunks {
			if chunk.offset != size || size >= file.UploadedBytes {
				return domain.Fail(domain.Prerequisite, "backup file chunk metadata is inconsistent")
			}
			data, err := s.files.ReadChunk(ctx, file.ID, file.Name, chunk.offset, int(min(int64(domain.FileChunkSize), file.Size-chunk.offset)))
			if err != nil {
				return err
			}
			if domain.Hash(string(data)) != chunk.digest {
				return domain.Fail(domain.Prerequisite, "backup filesystem bytes failed integrity verification")
			}
			if _, err := tx.ExecContext(ctx, "UPDATE file_chunks SET data=? WHERE file_id=? AND offset=?", data, file.ID, chunk.offset); err != nil {
				return err
			}
			size += int64(len(data))
		}
		if size != file.UploadedBytes {
			return domain.Fail(domain.Prerequisite, "backup file content is incomplete")
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM file_store_state"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = image.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}
