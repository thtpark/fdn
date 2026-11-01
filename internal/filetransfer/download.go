package filetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Journal interface {
	Get(context.Context, string, any) (bool, error)
	Put(context.Context, string, any) error
}

type Proposal struct {
	Destination string                      `json:"destination"`
	File        domain.SharedFile           `json:"file"`
	Complete    bool                        `json:"complete"`
	Receipt     *domain.FileDownloadReceipt `json:"receipt,omitempty"`
	Stage       string                      `json:"stage,omitempty"`
	StageDevice uint64                      `json:"stage_device,omitempty"`
	StageInode  uint64                      `json:"stage_inode,omitempty"`
	Automatic   bool                        `json:"automatic,omitempty"`
}

func Root(path string) (*os.Root, string, error) {
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

func OpenSource(root *os.Root, name string, private bool) (*os.File, error) {
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

func Digest(file *os.File) (int64, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, domain.MaxFileSize+1))
	return size, hex.EncodeToString(hash.Sum(nil)), err
}

func removeStage(root *os.Root, proposal Proposal) error {
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

// Download keeps the existing CLI journal format and attests only after durable
// local finalization. Guard rechecks current admission before each remote chunk
// and immediately before acknowledgement; nil is ordinary explicit CLI access.
func Download(ctx context.Context, client *api.Client, journal Journal, operation, id, destination string, guard func(context.Context) error) (receipt domain.FileDownloadReceipt, err error) {
	if !domain.ValidOperation(operation) || destination == "" {
		return receipt, domain.Fail(domain.Invalid, "download requires an exact operation and destination")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return receipt, err
	}
	root, name, err := Root(destination)
	if err != nil {
		return receipt, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	key := "file-download:" + operation
	var proposal Proposal
	found, err := journal.Get(ctx, key, &proposal)
	if err != nil {
		return receipt, err
	}
	if found && (proposal.Destination != destination || proposal.File.ID != id) {
		return receipt, domain.Fail(domain.Conflict, "download operation belongs to a different exact destination/file")
	}
	info, statErr := root.Lstat(name)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return receipt, statErr
	}
	if !found {
		if statErr == nil {
			return receipt, domain.Fail(domain.Conflict, "download destination already exists; it will not be adopted or overwritten")
		}
		if guard != nil {
			if err := guard(ctx); err != nil {
				return receipt, err
			}
		}
		file, err := api.Call[domain.SharedFile](ctx, client, "GET", api.Path("files", id), nil)
		if err != nil {
			return receipt, err
		}
		proposal = Proposal{Destination: destination, File: file, Automatic: guard != nil}
		if err := journal.Put(ctx, key, proposal); err != nil {
			return receipt, err
		}
	}
	defer func() { err = errors.Join(err, removeStage(root, proposal)) }()
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return receipt, domain.Fail(domain.Denied, "download destination is not the retained regular file")
		}
		local, err := OpenSource(root, name, true)
		if err != nil {
			return receipt, err
		}
		size, digest, err := Digest(local)
		err = errors.Join(err, local.Sync(), local.Close())
		if err != nil {
			return receipt, err
		}
		if size != proposal.File.Size || digest != proposal.File.SHA256 {
			return receipt, domain.Fail(domain.Conflict, "retained destination does not match the verified download proposal")
		}
		proposal.Complete = true
	} else {
		if proposal.Complete {
			return receipt, domain.Fail(domain.Prerequisite, "verified local destination is missing; do not acknowledge or replay deletion")
		}
		if err := removeStage(root, proposal); err != nil {
			return receipt, err
		}
		proposal.Stage = ".fdn-download-" + domain.Hash(operation)
		stage, err := root.OpenFile(proposal.Stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return receipt, err
		}
		stageInfo, err := stage.Stat()
		if err != nil {
			return receipt, errors.Join(err, stage.Close())
		}
		stat, ok := stageInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return receipt, errors.Join(domain.Fail(domain.Internal, "download staging ownership is unavailable"), stage.Close())
		}
		proposal.StageDevice, proposal.StageInode = uint64(stat.Dev), stat.Ino
		if err := journal.Put(ctx, key, proposal); err != nil {
			return receipt, errors.Join(err, stage.Close())
		}
		file, downloadErr := client.DownloadFileGuarded(ctx, id, stage, guard)
		err = errors.Join(downloadErr, stage.Sync(), stage.Close())
		if err != nil {
			return receipt, err
		}
		if file.SHA256 != proposal.File.SHA256 || file.Size != proposal.File.Size {
			return receipt, domain.Fail(domain.Internal, "download changed its immutable declaration")
		}
		pinned, err := root.Lstat(proposal.Stage)
		if err != nil {
			return receipt, err
		}
		if !os.SameFile(stageInfo, pinned) || !pinned.Mode().IsRegular() || pinned.Mode().Perm()&0077 != 0 {
			return receipt, domain.Fail(domain.Denied, "verified staging file changed before local finalization")
		}
		if err := root.Link(proposal.Stage, name); err != nil {
			return receipt, err
		}
		finalized, err := root.Lstat(name)
		if err != nil {
			return receipt, err
		}
		if !os.SameFile(stageInfo, finalized) || !finalized.Mode().IsRegular() {
			return receipt, domain.Fail(domain.Denied, "local destination is not the verified staging file")
		}
		proposal.Complete = true
	}
	directory, err := root.Open(".")
	if err != nil {
		return receipt, err
	}
	if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
		return receipt, err
	}
	if err := journal.Put(ctx, key, proposal); err != nil {
		return receipt, err
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if guard != nil {
		if err := guard(ctx); err != nil {
			return receipt, err
		}
	}
	request := domain.FileDownloadRequest{Command: domain.Command{OperationID: operation},
		Size: proposal.File.Size, SHA256: proposal.File.SHA256, Automatic: proposal.Automatic}
	receipt, err = api.Call[domain.FileDownloadReceipt](ctx, client, "POST", api.Path("files", id, "downloaded"), request)
	if err != nil {
		return receipt, err
	}
	if receipt.FileID != id || receipt.ReaderID != client.Credential.AgentID || receipt.Size != proposal.File.Size ||
		receipt.SHA256 != proposal.File.SHA256 || receipt.ReceivedAt <= 0 ||
		(proposal.File.OwnerID != receipt.ReaderID && !domain.ValidOperation(receipt.NotificationID)) {
		return receipt, domain.Fail(domain.Internal, "download acknowledgement did not match this reader's exact verified file")
	}
	proposal.Receipt = &receipt
	err = journal.Put(ctx, key, proposal)
	return receipt, err
}
