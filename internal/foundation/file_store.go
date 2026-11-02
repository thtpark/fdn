package foundation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
)

func (s *Store) openFileStore(ctx context.Context, databasePath string, options FileStoreOptions) error {
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
	directory := options.Directory
	if directory == "" {
		directory = filepath.Dir(databasePath) + "-files"
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	s.files, err = filestore.Open(directory, options.Permissions)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	var retained string
	err = s.db.QueryRowContext(ctx, "SELECT directory FROM file_store_state WHERE id=1").Scan(&retained)
	if err == nil && retained != canonical {
		return domain.Fail(domain.Conflict, "file_dir differs from this database's pinned live content store; retain the original store and use an explicit quarantined restore")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO file_store_state(id,directory) VALUES(1,?) ON CONFLICT(id) DO NOTHING", canonical); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT json_set(data,'$.uploaded_bytes',uploaded_bytes) FROM files ORDER BY id")
	if err != nil {
		return err
	}
	files, err := scanJSON[domain.SharedFile](rows)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.State != "deleted" {
			if err := s.materializeFile(ctx, file); err != nil {
				return fmt.Errorf("Foundation file materialization failed; retained content was not discarded: %w", err)
			}
		}
		if _, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO file_acl_pending VALUES(?)", file.ID); err != nil {
			return err
		}
	}
	return s.reconcileFilePermissions(ctx)
}

// SQL9 chunks (and self-contained encrypted backup snapshots) are verified into
// the sole live filesystem copy before their database payloads are retired.
func (s *Store) materializeFile(ctx context.Context, file domain.SharedFile) (err error) {
	source, err := s.files.OpenFile(file.ID, file.Name)
	if errors.Is(err, os.ErrNotExist) {
		if err = s.files.Create(file.ID, file.Name); err != nil {
			return err
		}
		source, err = s.files.OpenFile(file.ID, file.Name)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if file.UploadedBytes < 0 || file.UploadedBytes > file.Size || file.Size > domain.MaxFileSize ||
		info.Size() > file.Size || (file.State == "ready" && file.UploadedBytes != file.Size) {
		return domain.Fail(domain.Prerequisite, "file storage progress does not match its retained declaration")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT offset,data,sha256 FROM file_chunks WHERE file_id=? ORDER BY offset", file.ID)
	if err != nil {
		return err
	}
	var size int64
	hash := sha256.New()
	for rows.Next() {
		var offset int64
		var legacy []byte
		var digest string
		if err = rows.Scan(&offset, &legacy, &digest); err != nil {
			break
		}
		length := int(min(int64(domain.FileChunkSize), file.Size-offset))
		if offset != size || length <= 0 || !domain.ValidHash(digest) || offset+int64(length) > file.UploadedBytes {
			err = domain.Fail(domain.Prerequisite, "retained file chunk metadata is inconsistent")
			break
		}
		existing := make([]byte, length)
		count, readErr := source.ReadAt(existing, offset)
		if len(legacy) != 0 {
			if len(legacy) != length || domain.Hash(string(legacy)) != digest {
				err = domain.Fail(domain.Prerequisite, "retained database file bytes failed integrity verification")
				break
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				err = readErr
				break
			}
			if !bytes.Equal(existing[:count], legacy[:count]) {
				err = domain.Fail(domain.Conflict, "interrupted file materialization contains different bytes; recovery evidence was preserved")
				break
			}
			if count != length {
				if err = s.files.WriteChunk(ctx, file.ID, file.Name, offset, legacy, offset); err != nil {
					break
				}
			}
			hash.Write(legacy)
		} else if readErr != nil || count != length || domain.Hash(string(existing)) != digest {
			err = domain.Fail(domain.Prerequisite, "live filesystem file bytes are missing or failed integrity verification")
			break
		} else {
			hash.Write(existing)
		}
		size += int64(length)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	info, err = source.Stat()
	if err != nil {
		return err
	}
	if size != file.UploadedBytes || info.Size() < size || (file.State == "ready" && info.Size() != file.Size) {
		return domain.Fail(domain.Prerequisite, "materialized file does not match its committed upload progress")
	}
	if file.State == "ready" && hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return domain.Fail(domain.Prerequisite, "materialized file SHA-256 differs from its retained immutable declaration")
	}
	if err := source.Sync(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE file_chunks SET data=X'' WHERE file_id=? AND length(data)>0", file.ID)
	return err
}

func pendingFiles(ctx context.Context, q queryer) ([]domain.SharedFile, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_set(f.data,'$.uploaded_bytes',f.uploaded_bytes)
FROM files f JOIN file_acl_pending p ON p.file_id=f.id ORDER BY f.id`)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.SharedFile](rows)
}

func fileReaders(ctx context.Context, q queryer, file domain.SharedFile) ([]uint32, error) {
	if file.State == "deleted" {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT i.uid FROM agent_identities i
JOIN agents a ON a.id=i.agent_id JOIN identity_accounts c ON c.agent_id=i.id AND c.uid=i.uid
WHERE a.active=1 AND COALESCE(json_extract(a.data,'$.banned'),0)=0
AND json_extract(i.data,'$.state')='active' AND
(a.id=? OR (? AND (? OR EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=? AND g.agent_id=a.id))))
ORDER BY i.uid`, file.OwnerID, file.State == "ready", file.TeamReadable, file.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var readers []uint32
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		if uid <= 0 || uid > 1<<32-1 {
			return nil, domain.Fail(domain.Prerequisite, "file reader has no valid enrolled Foundation-host UID")
		}
		readers = append(readers, uint32(uid))
	}
	return readers, rows.Err()
}

func (s *Store) restrictFiles(ctx context.Context, files []domain.SharedFile) error {
	for _, file := range files {
		err := s.files.SetReaders(ctx, file.ID, file.Name, nil)
		if file.State == "deleted" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("Foundation could not withdraw file ACLs; mutation was not confirmed: %w", err)
		}
	}
	return nil
}

func (s *Store) syncFilePermissions(ctx context.Context) error {
	files, err := pendingFiles(ctx, s.db)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.State == "deleted" {
			err = s.files.Remove(file.ID, file.Name)
		} else {
			var readers []uint32
			readers, err = fileReaders(ctx, s.db, file)
			if err == nil {
				err = s.files.SetReaders(ctx, file.ID, file.Name, readers)
			}
		}
		if err != nil {
			return fmt.Errorf("Foundation file ACL/content reconciliation is pending; replay the exact operation after fixing storage: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM file_acl_pending WHERE file_id=?", file.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileFilePermissions(ctx context.Context) error {
	if s.files == nil {
		return nil
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return s.syncFilePermissions(ctx)
}

func (s *Store) restoreFilePermissions(ctx context.Context, files []domain.SharedFile) error {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	for _, file := range files {
		if _, err := s.db.ExecContext(recovery, "INSERT OR IGNORE INTO file_acl_pending SELECT id FROM files WHERE id=?", file.ID); err != nil {
			return err
		}
	}
	return s.syncFilePermissions(recovery)
}

func (s *Store) commitFileEffects(ctx context.Context, tx *sql.Tx) (bool, error) {
	if s.files == nil {
		err := tx.Commit()
		return err == nil, err
	}
	files, err := pendingFiles(ctx, tx)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		err := tx.Commit()
		return err == nil, err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	// No new reader is installed before committed authority. A crash during a
	// withdrawal can only deny access; startup reconciles from the durable DB.
	if err := s.restrictFiles(ctx, files); err != nil {
		rollbackErr := tx.Rollback()
		return false, errors.Join(err, rollbackErr, s.restoreFilePermissions(ctx, files))
	}
	if err := tx.Commit(); err != nil {
		rollbackErr := tx.Rollback()
		if errors.Is(rollbackErr, sql.ErrTxDone) {
			rollbackErr = nil
		}
		return false, errors.Join(err, rollbackErr, s.restoreFilePermissions(ctx, files))
	}
	return true, s.syncFilePermissions(ctx)
}

func requireFileContentReady(ctx context.Context, q queryer, id string) error {
	var pending bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM file_acl_pending WHERE file_id=?)", id).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return domain.Fail(domain.Unavailable, "Foundation file permissions are pending reconciliation; content and receipts remain fenced")
	}
	return nil
}
