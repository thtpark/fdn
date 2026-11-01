package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type fileUploadProposal struct {
	Request domain.FileUploadRequest `json:"request"`
	File    domain.SharedFile        `json:"file"`
}

type fileDownloadProposal struct {
	Destination string                      `json:"destination"`
	File        domain.SharedFile           `json:"file"`
	Complete    bool                        `json:"complete"`
	Receipt     *domain.FileDownloadReceipt `json:"receipt,omitempty"`
	Stage       string                      `json:"stage,omitempty"`
	StageDevice uint64                      `json:"stage_device,omitempty"`
	StageInode  uint64                      `json:"stage_inode,omitempty"`
}

func fileRoot(path string) (*os.Root, string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	name, parent := filepath.Base(path), filepath.Dir(path)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, "", domain.Fail(domain.Invalid, "choose a regular file, not a directory")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, "", domain.Fail(domain.Denied, "file parent must be a real directory without group/other write permission")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", err
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(info, pinned) {
		root.Close()
		if err != nil {
			return nil, "", err
		}
		return nil, "", domain.Fail(domain.Denied, "file parent changed while opening it")
	}
	return root, name, nil
}

func openTransferSource(root *os.Root, name string, private bool) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Size() > domain.MaxFileSize ||
		(private && info.Mode().Perm()&0077 != 0)) {
		err = domain.Fail(domain.Denied, "transfer file must be regular, within 1 GiB and downloaded files must be owner-only")
	}
	if err == nil {
		err = secure.CheckOwner(info)
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func transferDigest(file *os.File) (int64, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, domain.MaxFileSize+1))
	return size, hex.EncodeToString(hash.Sum(nil)), err
}

func (a *app) uploadFile(ctx context.Context, r *resources, args []string, transfer bool) (value any, err error) {
	fs := flag.NewFlagSet("file upload", flag.ContinueOnError)
	path := fs.String("path", "", "explicit local regular file; never sent as a workspace path")
	media := fs.String("media-type", "", "canonical MIME type; omitted detects the first 512 bytes")
	to := fs.String("to", "", "exact private transfer recipient")
	remove := fs.Bool("delete-after-download", transfer, "delete designated private transfer content after verified recipient receipt")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *path == "" || (transfer && *to == "") {
		return nil, domain.Fail(domain.Invalid, "file upload requires --path; file transfer also requires --to AGENT")
	}
	root, name, err := fileRoot(*path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	source, err := openTransferSource(root, name, false)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	size, digest, err := transferDigest(source)
	if err != nil {
		return nil, err
	}
	if *media == "" {
		var prefix [512]byte
		count, err := source.ReadAt(prefix[:], 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		*media = http.DetectContentType(prefix[:count])
		if at := len("text/plain"); len(*media) >= at && (*media)[:at] == "text/plain" {
			*media = "text/plain"
		}
	}
	request := domain.FileUploadRequest{Command: domain.Command{OperationID: a.operation}, Name: name, MediaType: *media,
		Size: size, SHA256: digest, TransferTo: *to, DeleteAfterDownload: *remove}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	key := "file-upload:" + a.operation
	var proposal fileUploadProposal
	found, err := r.state.Get(ctx, key, &proposal)
	if err != nil {
		return nil, err
	}
	if found && proposal.Request != request {
		return nil, domain.Fail(domain.Conflict, "upload operation belongs to a different exact file declaration")
	}
	proposal.Request = request
	if err := r.state.Put(ctx, key, proposal); err != nil {
		return nil, err
	}
	proposal.File, err = r.client.UploadFile(ctx, request, source)
	saveErr := r.state.Put(context.WithoutCancel(ctx), key, proposal)
	return proposal.File, errors.Join(err, saveErr)
}

func removeDownloadStage(root *os.Root, proposal fileDownloadProposal) error {
	if proposal.Stage == "" {
		return nil
	}
	info, err := root.Lstat(proposal.Stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 ||
		uint64(stat.Dev) != proposal.StageDevice || stat.Ino != proposal.StageInode {
		return domain.Fail(domain.Denied, "download staging resource changed; retain evidence for explicit recovery")
	}
	if err := secure.CheckOwner(info); err != nil {
		return err
	}
	return root.Remove(proposal.Stage)
}

func (a *app) downloadFile(ctx context.Context, r *resources, id string, args []string) (value any, err error) {
	fs := flag.NewFlagSet("file download", flag.ContinueOnError)
	path := fs.String("output", "", "explicit destination; existing files are never overwritten")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *path == "" {
		return nil, domain.Fail(domain.Invalid, "file download requires --output FILE")
	}
	destination, err := filepath.Abs(*path)
	if err != nil {
		return nil, err
	}
	root, name, err := fileRoot(destination)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	key := "file-download:" + a.operation
	var proposal fileDownloadProposal
	found, err := r.state.Get(ctx, key, &proposal)
	if err != nil {
		return nil, err
	}
	if found && (proposal.Destination != destination || proposal.File.ID != id) {
		return nil, domain.Fail(domain.Conflict, "download operation belongs to a different exact destination/file")
	}
	info, statErr := root.Lstat(name)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if !found {
		if statErr == nil {
			return nil, domain.Fail(domain.Conflict, "download destination already exists; it will not be adopted or overwritten")
		}
		file, err := api.Call[domain.SharedFile](ctx, r.client, "GET", api.Path("files", id), nil)
		if err != nil {
			return nil, err
		}
		proposal = fileDownloadProposal{Destination: destination, File: file}
		if err := r.state.Put(ctx, key, proposal); err != nil {
			return nil, err
		}
	}
	defer func() { err = errors.Join(err, removeDownloadStage(root, proposal)) }()
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, domain.Fail(domain.Denied, "download destination is not the retained regular file")
		}
		local, err := openTransferSource(root, name, true)
		if err != nil {
			return nil, err
		}
		size, digest, err := transferDigest(local)
		err = errors.Join(err, local.Close())
		if err != nil {
			return nil, err
		}
		if size != proposal.File.Size || digest != proposal.File.SHA256 {
			return nil, domain.Fail(domain.Conflict, "retained destination does not match the verified download proposal")
		}
		proposal.Complete = true
	} else {
		if proposal.Complete {
			return nil, domain.Fail(domain.Prerequisite, "verified local destination is missing; do not acknowledge or replay deletion")
		}
		if err := removeDownloadStage(root, proposal); err != nil {
			return nil, err
		}
		proposal.Stage = ".fdn-download-" + domain.Hash(a.operation)
		stage, err := root.OpenFile(proposal.Stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, err
		}
		stageInfo, err := stage.Stat()
		if err != nil {
			return nil, errors.Join(err, stage.Close())
		}
		stat, ok := stageInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errors.Join(domain.Fail(domain.Internal, "download staging ownership is unavailable"), stage.Close())
		}
		proposal.StageDevice, proposal.StageInode = uint64(stat.Dev), stat.Ino
		if err := r.state.Put(ctx, key, proposal); err != nil {
			return nil, errors.Join(err, stage.Close())
		}
		file, downloadErr := r.client.DownloadFile(ctx, id, stage)
		err = errors.Join(downloadErr, stage.Sync(), stage.Close())
		if err != nil {
			return nil, err
		}
		if file.SHA256 != proposal.File.SHA256 || file.Size != proposal.File.Size {
			return nil, domain.Fail(domain.Internal, "download changed its immutable declaration")
		}
		if err := root.Link(proposal.Stage, name); err != nil {
			return nil, err
		}
		directory, err := root.Open(".")
		if err != nil {
			return nil, err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return nil, err
		}
		proposal.Complete = true
	}
	if err := r.state.Put(ctx, key, proposal); err != nil {
		return nil, err
	}
	request := domain.FileDownloadRequest{Command: domain.Command{OperationID: a.operation}, Size: proposal.File.Size, SHA256: proposal.File.SHA256}
	receipt, err := api.Call[domain.FileDownloadReceipt](ctx, r.client, "POST", api.Path("files", id, "downloaded"), request)
	if err != nil {
		return nil, err
	}
	if receipt.FileID != id || receipt.ReaderID != r.client.Credential.AgentID || receipt.Size != proposal.File.Size ||
		receipt.SHA256 != proposal.File.SHA256 || receipt.ReceivedAt <= 0 ||
		(proposal.File.OwnerID != receipt.ReaderID && !domain.ValidOperation(receipt.NotificationID)) {
		return nil, domain.Fail(domain.Internal, "download acknowledgement did not match this reader's exact verified file")
	}
	proposal.Receipt = &receipt
	if err := r.state.Put(ctx, key, proposal); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (a *app) fileCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "file requires upload, transfer, upload-status, begin, put-chunk, finalize, publish, grant, delete, downloaded, list, show, chunk or download")
	}
	switch args[0] {
	case "upload", "transfer":
		return a.uploadFile(ctx, r, args[1:], args[0] == "transfer")
	case "upload-status":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		var proposal fileUploadProposal
		found, err := r.state.Get(ctx, "file-upload:"+args[1], &proposal)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.Fail(domain.NotFound, "no retained local upload proposal")
		}
		return proposal, nil
	case "show":
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		return api.Call[domain.SharedFile](ctx, r.client, "GET", api.Path("files", args[1]), nil)
	case "list":
		fs := flag.NewFlagSet("file list", flag.ContinueOnError)
		cursor := fs.String("cursor", "", "actor-bound cursor")
		limit := fs.Int("limit", 50, "page size")
		deleted := fs.Bool("include-deleted", false, "include retained tombstones")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		query := url.Values{"cursor": {*cursor}, "limit": {strconv.Itoa(*limit)}, "include_deleted": {strconv.FormatBool(*deleted)}}
		return api.Call[domain.Page[domain.SharedFile]](ctx, r.client, "GET", "/v1/files?"+query.Encode(), nil)
	case "begin":
		var req domain.FileUploadRequest
		if err := a.input(args[1:], &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, "/v1/files", req, "")
	}
	id, tail, err := idArgs(args[1:])
	if err != nil {
		return nil, err
	}
	switch args[0] {
	case "download":
		return a.downloadFile(ctx, r, id, tail)
	case "chunk":
		fs := flag.NewFlagSet("file chunk", flag.ContinueOnError)
		offset := fs.Int64("offset", 0, "aligned byte offset")
		if err := parse(fs, tail); err != nil {
			return nil, err
		}
		return api.Call[domain.FileChunk](ctx, r.client, "GET", api.Path("files", id, "chunks")+"?offset="+strconv.FormatInt(*offset, 10), nil)
	case "put-chunk":
		var req domain.FileChunkRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		// Chunk bytes stay in the staged file, not a second local proposal copy.
		return api.Call[domain.FileProgress](ctx, r.client, "POST", api.Path("files", id, "chunks"), req)
	case "grant":
		var req domain.FileGrantRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("files", id, "grants"), req, api.Path("files", id))
	case "downloaded":
		var req domain.FileDownloadRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		return a.write(ctx, r, api.Path("files", id, "downloaded"), req, "")
	case "finalize", "publish", "delete":
		var req domain.FileChangeRequest
		if err := a.input(tail, &req); err != nil {
			return nil, err
		}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		revision := ""
		if args[0] != "finalize" {
			revision = api.Path("files", id)
		}
		return a.write(ctx, r, api.Path("files", id, args[0]), req, revision)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown file command")
	}
}
