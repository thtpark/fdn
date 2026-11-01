package foundation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

func readFile(ctx context.Context, q queryer, id string) (domain.SharedFile, error) {
	file, err := get[domain.SharedFile](ctx, q, "files", id)
	if err == nil {
		err = q.QueryRowContext(ctx, "SELECT uploaded_bytes FROM files WHERE id=?", id).Scan(&file.UploadedBytes)
	}
	return file, err
}

func requireFileReader(ctx context.Context, q queryer, actor string, file domain.SharedFile) error {
	if actor == file.OwnerID || (file.TeamReadable && file.State != "uploading") {
		return nil
	}
	var granted bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM file_grants WHERE file_id=? AND agent_id=?)", file.ID, actor).Scan(&granted); err != nil {
		return err
	}
	if granted && file.State != "uploading" {
		return nil
	}
	return domain.Fail(domain.Denied, "file is private to its owner and explicitly granted readers")
}

func requireFileOwner(actor string, file domain.SharedFile) error {
	if actor != file.OwnerID {
		return domain.Fail(domain.Denied, "only the file's owner may finalize, publish, grant, revoke or delete it")
	}
	return nil
}

func saveFile(ctx context.Context, tx *sql.Tx, file domain.SharedFile) error {
	data, err := encode(file)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO files(id,data,owner_id,team_readable,state,uploaded_bytes)
VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,team_readable=excluded.team_readable,
state=excluded.state,uploaded_bytes=excluded.uploaded_bytes`,
		file.ID, data, file.OwnerID, file.TeamReadable, file.State, file.UploadedBytes)
	return err
}

func fileUpdate(ctx context.Context, tx *sql.Tx, file domain.SharedFile) error {
	data, err := encode(file)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO update_changes(source_table,source_id,after_data) VALUES('files',?,?)
ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data`, file.ID, data)
	return err
}

func (s *Store) BeginFile(ctx context.Context, actor string, req domain.FileUploadRequest) (domain.SharedFile, error) {
	return mutate(ctx, s, actor, "file.begin", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SharedFile, error) {
		var file domain.SharedFile
		if err := req.Validate(); err != nil {
			return file, err
		}
		if req.TransferTo != "" {
			if req.TransferTo == a.ID {
				return file, domain.Fail(domain.Invalid, "transfer recipient must be a different agent")
			}
			if _, err := activeIdentitySubject(ctx, tx, req.TransferTo); err != nil {
				return file, err
			}
		}
		file = domain.SharedFile{FileReference: domain.FileReference{ID: domain.NewID("file"), Name: req.Name,
			MediaType: req.MediaType, Size: req.Size, SHA256: req.SHA256}, OwnerID: a.ID,
			State: "uploading", Revision: 1, CreatedAt: s.timestamp(), TransferTo: req.TransferTo,
			DeleteAfterDownload: req.DeleteAfterDownload}
		if err := saveFile(ctx, tx, file); err != nil {
			return file, err
		}
		return file, fileUpdate(ctx, tx, file)
	})
}

func (s *Store) PutFileChunk(ctx context.Context, actor, id string, req domain.FileChunkRequest) (domain.FileProgress, error) {
	return mutate(ctx, s, actor, "file.chunk:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.FileProgress, error) {
		progress := domain.FileProgress{FileID: id}
		if req.Offset < 0 || req.Offset%domain.FileChunkSize != 0 || len(req.Data) == 0 ||
			len(req.Data) > domain.FileChunkSize || !domain.ValidHash(req.SHA256) || domain.Hash(string(req.Data)) != req.SHA256 {
			return progress, domain.Fail(domain.Invalid, "chunk requires an aligned offset, bounded bytes and their exact SHA-256")
		}
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return progress, err
		}
		if err := requireFileOwner(a.ID, file); err != nil {
			return progress, err
		}
		if file.State != "uploading" {
			return progress, domain.Fail(domain.Prerequisite, "file is not an active staged upload")
		}
		if req.Offset+int64(len(req.Data)) > file.Size ||
			(len(req.Data) != domain.FileChunkSize && req.Offset+int64(len(req.Data)) != file.Size) {
			return progress, domain.Fail(domain.Invalid, "chunk must fit the declared file and only the last chunk may be short")
		}
		if req.Offset < file.UploadedBytes {
			var existing []byte
			if err := tx.QueryRowContext(ctx, "SELECT data FROM file_chunks WHERE file_id=? AND offset=?", id, req.Offset).Scan(&existing); err != nil {
				return progress, err
			}
			if !bytes.Equal(existing, req.Data) {
				return progress, domain.Fail(domain.Conflict, "committed chunk bytes are immutable")
			}
			progress.UploadedBytes = file.UploadedBytes
			return progress, nil
		}
		if req.Offset != file.UploadedBytes {
			return progress, domain.Fail(domain.Conflict, "upload offset changed; inspect the staged file before resuming")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO file_chunks(file_id,offset,data,sha256) VALUES(?,?,?,?)", id, req.Offset, req.Data, req.SHA256); err != nil {
			return progress, err
		}
		progress.UploadedBytes = req.Offset + int64(len(req.Data))
		_, err = tx.ExecContext(ctx, "UPDATE files SET uploaded_bytes=? WHERE id=?", progress.UploadedBytes, id)
		return progress, err
	})
}

func verifyStagedFile(ctx context.Context, tx *sql.Tx, file domain.SharedFile) error {
	if file.UploadedBytes != file.Size {
		return domain.Fail(domain.Prerequisite, "upload is incomplete")
	}
	rows, err := tx.QueryContext(ctx, "SELECT offset,data,sha256 FROM file_chunks WHERE file_id=? ORDER BY offset", file.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	hash := sha256.New()
	var size int64
	var prefix []byte
	for rows.Next() {
		var offset int64
		var data []byte
		var digest string
		if err := rows.Scan(&offset, &data, &digest); err != nil {
			return err
		}
		if offset != size || domain.Hash(string(data)) != digest {
			return domain.Fail(domain.Internal, "staged chunk integrity failed")
		}
		if size == 0 {
			prefix = data[:min(len(data), 512)]
		}
		hash.Write(data)
		size += int64(len(data))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if size != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return domain.Fail(domain.Invalid, "uploaded file size or SHA-256 does not match its immutable declaration")
	}
	if domain.InlineMedia(file.MediaType) && http.DetectContentType(prefix) != file.MediaType {
		// QuickTime uses the ISO media container but is not recognized by net/http.
		quickTime := file.MediaType == "video/quicktime" && len(prefix) >= 12 &&
			string(prefix[4:8]) == "ftyp" && string(prefix[8:12]) == "qt  "
		if !quickTime {
			return domain.Fail(domain.Invalid, "inline media bytes do not match the declared supported media type")
		}
	}
	return nil
}

func (s *Store) FinalizeFile(ctx context.Context, actor, id string, req domain.FileChangeRequest) (domain.SharedFile, error) {
	return mutate(ctx, s, actor, "file.finalize:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SharedFile, error) {
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return file, err
		}
		if err := requireFileOwner(a.ID, file); err != nil {
			return file, err
		}
		if err := domain.CheckText(req.Reason, "finalization reason", 8192, true); err != nil {
			return file, err
		}
		if file.State != "uploading" {
			return file, domain.Fail(domain.Prerequisite, "only an active staged upload can be finalized")
		}
		if err := verifyStagedFile(ctx, tx, file); err != nil {
			return file, err
		}
		file.State, file.FinalizedAt, file.Revision = "ready", s.timestamp(), file.Revision+1
		if file.TransferTo != "" {
			if _, err := activeIdentitySubject(ctx, tx, file.TransferTo); err != nil {
				return file, err
			}
			grant := domain.FileGrant{FileID: id, AgentID: file.TransferTo, GrantedBy: a.ID, GrantedAt: s.timestamp()}
			if _, err := saveFileGrant(ctx, tx, grant); err != nil {
				return file, err
			}
			message := domain.Message{ID: domain.NewID("message"), SenderID: a.ID, RecipientID: file.TransferTo,
				Body:        "A private file is available in Foundation: " + file.Name + " (" + file.ID + "). Download and verify it to record receipt.",
				Attachments: []domain.FileReference{file.FileReference}, Origin: "service", Kind: "direct",
				State: "pending", Revision: 1, CreatedAt: s.timestamp()}
			message.ThreadID = message.ID
			if _, err := insertMessage(ctx, tx, message); err != nil {
				return file, err
			}
		}
		if err := saveFile(ctx, tx, file); err != nil {
			return file, err
		}
		return file, fileUpdate(ctx, tx, file)
	})
}

func saveFileGrant(ctx context.Context, tx *sql.Tx, grant domain.FileGrant) (bool, error) {
	data, err := encode(grant)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO file_grants(file_id,agent_id,data) VALUES(?,?,?) ON CONFLICT(file_id,agent_id) DO NOTHING",
		grant.FileID, grant.AgentID, data)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}

func (s *Store) GrantFile(ctx context.Context, actor, id string, req domain.FileGrantRequest) (domain.SharedFile, error) {
	return mutate(ctx, s, actor, "file.grant:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SharedFile, error) {
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return file, err
		}
		if err := requireFileOwner(a.ID, file); err != nil {
			return file, err
		}
		if err := expect(file.Revision, req.ExpectedRevision); err != nil {
			return file, err
		}
		if err := domain.CheckText(req.Reason, "grant reason", 8192, true); err != nil {
			return file, err
		}
		if file.State != "ready" || req.AgentID == a.ID || file.TeamReadable ||
			(file.TransferTo != "" && file.TransferTo != req.AgentID) {
			return file, domain.Fail(domain.Invalid, "private ready files may grant exact readers; a transfer is limited to its designated recipient")
		}
		if _, err := activeIdentitySubject(ctx, tx, req.AgentID); err != nil {
			return file, err
		}
		grant := domain.FileGrant{FileID: id, AgentID: req.AgentID, GrantedBy: a.ID, GrantedAt: s.timestamp()}
		changed := false
		if req.Revoke {
			var result sql.Result
			result, err = tx.ExecContext(ctx, "DELETE FROM file_grants WHERE file_id=? AND agent_id=?", id, req.AgentID)
			if err == nil {
				var count int64
				count, err = result.RowsAffected()
				changed = count != 0
			}
		} else {
			changed, err = saveFileGrant(ctx, tx, grant)
		}
		if err != nil {
			return file, err
		}
		if !changed {
			return file, nil
		}
		file.Revision++
		if err := saveFile(ctx, tx, file); err != nil {
			return file, err
		}
		data, err := encode(grant)
		if err != nil {
			return file, err
		}
		// Preserve the withdrawn reader in the private invalidation as well.
		if _, err := tx.ExecContext(ctx, "INSERT INTO update_changes(source_table,source_id,after_data) VALUES('file-grants',?,?)", id, data); err != nil {
			return file, err
		}
		return file, fileUpdate(ctx, tx, file)
	})
}

func (s *Store) PublishFile(ctx context.Context, actor, id string, req domain.FileChangeRequest) (domain.SharedFile, error) {
	return mutate(ctx, s, actor, "file.publish:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SharedFile, error) {
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return file, err
		}
		if err := requireFileOwner(a.ID, file); err != nil {
			return file, err
		}
		if err := expect(file.Revision, req.ExpectedRevision); err != nil {
			return file, err
		}
		if err := domain.CheckText(req.Reason, "explicit team publication reason", 8192, true); err != nil {
			return file, err
		}
		if file.State != "ready" || file.TransferTo != "" {
			return file, domain.Fail(domain.Prerequisite, "only a ready retained file, not a designated private transfer, may be published to the team")
		}
		if file.TeamReadable {
			return file, nil
		}
		file.TeamReadable, file.Revision = true, file.Revision+1
		file.PublishedAt, file.PublishedBy = s.timestamp(), a.ID
		if err := saveFile(ctx, tx, file); err != nil {
			return file, err
		}
		if err := fileUpdate(ctx, tx, file); err != nil {
			return file, err
		}
		return file, s.event(ctx, tx, "file", id, "", "")
	})
}

func (s *Store) deleteFile(ctx context.Context, tx *sql.Tx, file *domain.SharedFile) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM file_chunks WHERE file_id=?", file.ID); err != nil {
		return err
	}
	file.State, file.DeletedAt, file.Revision = "deleted", s.timestamp(), file.Revision+1
	if err := saveFile(ctx, tx, *file); err != nil {
		return err
	}
	if err := fileUpdate(ctx, tx, *file); err != nil {
		return err
	}
	if file.TeamReadable {
		return s.event(ctx, tx, "file", file.ID, "", "")
	}
	return nil
}

func (s *Store) DeleteFile(ctx context.Context, actor, id string, req domain.FileChangeRequest) (domain.SharedFile, error) {
	return mutate(ctx, s, actor, "file.delete:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SharedFile, error) {
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return file, err
		}
		if err := requireFileOwner(a.ID, file); err != nil {
			return file, err
		}
		if err := expect(file.Revision, req.ExpectedRevision); err != nil {
			return file, err
		}
		if err := domain.CheckText(req.Reason, "deletion reason", 8192, true); err != nil {
			return file, err
		}
		if file.State == "deleted" {
			return file, domain.Fail(domain.Prerequisite, "file content is already deleted")
		}
		err = s.deleteFile(ctx, tx, &file)
		return file, err
	})
}

func (s *Store) File(ctx context.Context, actor, id string) (file domain.SharedFile, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return file, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err = s.actor(ctx, snapshot, actor); err != nil {
		return file, err
	}
	file, err = readFile(ctx, snapshot, id)
	if err == nil {
		err = requireFileReader(ctx, snapshot, actor, file)
	}
	return file, err
}

func (s *Store) Files(ctx context.Context, actor, cursor string, limit int, includeDeleted bool) (page domain.Page[domain.SharedFile], err error) {
	page = domain.Page[domain.SharedFile]{Items: []domain.SharedFile{}}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return page, err
	}
	limit, err = limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "files"
	if includeDeleted {
		key += ":deleted"
	}
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	rows, err := snapshot.QueryContext(ctx, `SELECT json_set(f.data,'$.uploaded_bytes',f.uploaded_bytes)
FROM files f WHERE f.id>? AND (? OR f.state<>'deleted') AND
(f.owner_id=? OR (f.team_readable=1 AND f.state<>'uploading') OR
 (f.state<>'uploading' AND EXISTS(SELECT 1 FROM file_grants g WHERE g.file_id=f.id AND g.agent_id=?)))
ORDER BY f.id LIMIT ?`, last, includeDeleted, actor, actor, limit+1)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.SharedFile](rows)
	if err == nil && len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].ID)
	}
	return page, err
}

func (s *Store) FileChunk(ctx context.Context, actor, id string, offset int64) (chunk domain.FileChunk, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return chunk, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return chunk, err
	}
	file, err := readFile(ctx, snapshot, id)
	if err != nil {
		return chunk, err
	}
	if err := requireFileReader(ctx, snapshot, actor, file); err != nil {
		return chunk, err
	}
	if file.State != "ready" {
		return chunk, domain.Fail(domain.Prerequisite, "file content is not available")
	}
	if offset < 0 || offset%domain.FileChunkSize != 0 || offset >= file.Size {
		return chunk, domain.Fail(domain.Invalid, "download offset must select an existing aligned chunk")
	}
	chunk.FileID, chunk.Offset = id, offset
	err = snapshot.QueryRowContext(ctx, "SELECT data,sha256 FROM file_chunks WHERE file_id=? AND offset=?", id, offset).Scan(&chunk.Data, &chunk.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.Fail(domain.Internal, "finalized file is missing a committed chunk")
	}
	return chunk, err
}

func (s *Store) AcknowledgeFileDownload(ctx context.Context, actor, id string, req domain.FileDownloadRequest) (domain.FileDownloadReceipt, error) {
	return mutate(ctx, s, actor, "file.downloaded:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.FileDownloadReceipt, error) {
		var receipt domain.FileDownloadReceipt
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return receipt, err
		}
		if err := requireFileReader(ctx, tx, a.ID, file); err != nil {
			return receipt, err
		}
		if req.Size != file.Size || req.SHA256 != file.SHA256 {
			return receipt, domain.Fail(domain.Invalid, "download receipt must attest the exact verified size and SHA-256")
		}
		var prior []byte
		err = tx.QueryRowContext(ctx, "SELECT data FROM file_downloads WHERE file_id=? AND agent_id=?", id, a.ID).Scan(&prior)
		if err == nil {
			err = json.Unmarshal(prior, &receipt)
			return receipt, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return receipt, err
		}
		if file.State != "ready" {
			return receipt, domain.Fail(domain.Prerequisite, "unavailable content cannot acquire a new download receipt")
		}
		receipt = domain.FileDownloadReceipt{FileID: id, ReaderID: a.ID, Size: file.Size, SHA256: file.SHA256, ReceivedAt: s.timestamp()}
		if a.ID != file.OwnerID {
			message := domain.Message{ID: domain.NewID("message"), SenderID: a.ID, RecipientID: file.OwnerID,
				Body:   "Verified download completed: " + file.Name + " (" + file.ID + ").",
				Origin: "service", Kind: "direct", State: "pending", Revision: 1, CreatedAt: s.timestamp()}
			message.ThreadID = message.ID
			if file.DeleteAfterDownload && file.TransferTo == a.ID {
				if err := s.deleteFile(ctx, tx, &file); err != nil {
					return receipt, err
				}
				receipt.ContentDeleted = true
				message.Body += " Foundation automatically deleted the private transfer content; its receipt and references are retained."
			}
			receipt.NotificationID = message.ID
			message.FileDownload = &receipt
			if _, err := insertMessage(ctx, tx, message); err != nil {
				return receipt, err
			}
		}
		data, err := encode(receipt)
		if err != nil {
			return receipt, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO file_downloads(file_id,agent_id,data) VALUES(?,?,?)", id, a.ID, data)
		return receipt, err
	})
}

func fileIDs(references []domain.FileReference) []string {
	ids := make([]string, len(references))
	for i, reference := range references {
		ids[i] = reference.ID
	}
	return ids
}

func inlineFileIDs(body string) []string {
	var ids []string
	for _, tail := range strings.Split(body, "foundation-file://")[1:] {
		id := strings.FieldsFunc(tail, func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_'
		})
		if len(id) != 0 {
			ids = append(ids, id[0])
		}
	}
	return ids
}
