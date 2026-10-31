package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
)

func fileOperation(operation, stage string) string {
	return "op_" + domain.Hash(operation+":"+stage)
}

// UploadFile resumes only the original immutable declaration and exact operations.
// It never retries a request after uncertain delivery.
func (c *Client) UploadFile(ctx context.Context, req domain.FileUploadRequest, source io.ReadSeeker) (domain.SharedFile, error) {
	var zero domain.SharedFile
	if err := req.Validate(); err != nil {
		return zero, err
	}
	if !domain.ValidOperation(req.OperationID) {
		return zero, domain.Fail(domain.Invalid, "upload requires an exact operation_id")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(source, domain.MaxFileSize+1))
	if err != nil {
		return zero, err
	}
	if size != req.Size || hex.EncodeToString(hash.Sum(nil)) != req.SHA256 {
		return zero, domain.Fail(domain.Conflict, "source file no longer matches this upload proposal")
	}
	file, err := Call[domain.SharedFile](ctx, c, "POST", "/v1/files", req)
	if err != nil {
		return file, err
	}
	if file.Name != req.Name || file.MediaType != req.MediaType || file.Size != req.Size || file.SHA256 != req.SHA256 ||
		file.TransferTo != req.TransferTo || file.DeleteAfterDownload != req.DeleteAfterDownload || file.UploadedBytes < 0 ||
		file.UploadedBytes > file.Size || (file.UploadedBytes != file.Size && file.UploadedBytes%domain.FileChunkSize != 0) {
		return zero, domain.Fail(domain.Internal, "staged upload response did not match its exact immutable proposal")
	}
	if file.State == "ready" {
		return file, nil
	}
	if file.State != "uploading" {
		return file, domain.Fail(domain.Prerequisite, "original upload content is no longer available; no new upload was created")
	}
	if _, err := source.Seek(file.UploadedBytes, io.SeekStart); err != nil {
		return file, err
	}
	buffer := make([]byte, domain.FileChunkSize)
	for offset := file.UploadedBytes; offset < file.Size; {
		if err := ctx.Err(); err != nil {
			return file, err
		}
		count := int(min(int64(len(buffer)), file.Size-offset))
		if _, err := io.ReadFull(source, buffer[:count]); err != nil {
			return file, err
		}
		chunk := domain.FileChunkRequest{Command: domain.Command{OperationID: fileOperation(req.OperationID, "chunk:"+strconv.FormatInt(offset, 10))},
			Offset: offset, Data: buffer[:count], SHA256: domain.Hash(string(buffer[:count]))}
		progress, err := Call[domain.FileProgress](ctx, c, "POST", Path("files", file.ID, "chunks"), chunk)
		if err != nil {
			return file, err
		}
		if progress.FileID != file.ID || progress.UploadedBytes != offset+int64(count) {
			return file, domain.Fail(domain.Internal, "upload chunk receipt did not match the exact offset")
		}
		offset = progress.UploadedBytes
	}
	finalize := domain.FileChangeRequest{Command: domain.Command{OperationID: fileOperation(req.OperationID, "finalize")},
		Reason: "finalize the explicitly requested immutable upload"}
	finalized, err := Call[domain.SharedFile](ctx, c, "POST", Path("files", file.ID, "finalize"), finalize)
	if err != nil {
		return file, err
	}
	if finalized.ID != file.ID || finalized.State != "ready" || finalized.SHA256 != req.SHA256 || finalized.Size != req.Size ||
		finalized.Name != req.Name || finalized.MediaType != req.MediaType || finalized.UploadedBytes != finalized.Size {
		return file, domain.Fail(domain.Internal, "file finalization response did not match the exact verified upload")
	}
	return finalized, nil
}

// DownloadFile verifies all bytes without acknowledging or deleting server content.
// The caller must durably finalize its local destination before sending a receipt.
func (c *Client) DownloadFile(ctx context.Context, id string, destination io.Writer) (domain.SharedFile, error) {
	file, err := Call[domain.SharedFile](ctx, c, "GET", Path("files", id), nil)
	if err != nil {
		return file, err
	}
	if file.ID != id || file.State != "ready" || file.Size < 0 || file.Size > domain.MaxFileSize || !domain.ValidHash(file.SHA256) {
		return file, domain.Fail(domain.Prerequisite, "file metadata is invalid or content is unavailable")
	}
	hash := sha256.New()
	for offset := int64(0); offset < file.Size; {
		if err := ctx.Err(); err != nil {
			return file, err
		}
		query := url.Values{"offset": {strconv.FormatInt(offset, 10)}}
		chunk, err := Call[domain.FileChunk](ctx, c, "GET", Path("files", id, "chunks")+"?"+query.Encode(), nil)
		if err != nil {
			return file, err
		}
		expected := min(int64(domain.FileChunkSize), file.Size-offset)
		if chunk.FileID != id || chunk.Offset != offset || int64(len(chunk.Data)) != expected ||
			!domain.ValidHash(chunk.SHA256) || domain.Hash(string(chunk.Data)) != chunk.SHA256 {
			return file, domain.Fail(domain.Internal, "download chunk identity, size or SHA-256 verification failed")
		}
		count, err := io.MultiWriter(destination, hash).Write(chunk.Data)
		if err != nil {
			return file, err
		}
		if count != len(chunk.Data) {
			return file, io.ErrShortWrite
		}
		offset += int64(count)
	}
	if hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return file, domain.Fail(domain.Internal, "downloaded file SHA-256 verification failed; no receipt was sent")
	}
	return file, nil
}
