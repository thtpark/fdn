package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filetransfer"
)

type fileUploadProposal struct {
	Request domain.FileUploadRequest `json:"request"`
	File    domain.SharedFile        `json:"file"`
}

func fileRoot(path string) (*os.Root, string, error) {
	return filetransfer.Root(path)
}

func openTransferSource(root *os.Root, name string, private bool) (*os.File, error) {
	return filetransfer.OpenSource(root, name, private)
}

func transferDigest(file *os.File) (int64, string, error) {
	return filetransfer.Digest(file)
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

func (a *app) downloadFile(ctx context.Context, r *resources, id string, args []string) (value any, err error) {
	fs := flag.NewFlagSet("file download", flag.ContinueOnError)
	path := fs.String("output", "", "explicit destination; existing files are never overwritten")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *path == "" {
		return nil, domain.Fail(domain.Invalid, "file download requires --output FILE")
	}
	return filetransfer.Download(ctx, r.client, r.state, a.operation, id, *path, nil)
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
