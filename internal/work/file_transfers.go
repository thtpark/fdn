package work

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/secure"
)

func (e *Engine) transferDestination(ctx context.Context, c config.Worker, file domain.SharedFile, retained filetransfer.Proposal, found bool) (string, error) {
	if found {
		if !filepath.IsAbs(retained.Destination) || retained.File.FileReference != file.FileReference || retained.File.OwnerID != file.OwnerID {
			return "", domain.Fail(domain.Conflict, "retained private transfer destination or immutable source changed; preserve its original journal")
		}
		return retained.Destination, nil
	}
	if !domain.ValidOperation(file.ID) || !domain.ValidOperation(file.OwnerID) {
		return "", domain.Fail(domain.Invalid, "private transfer file and sender require safe stable IDs")
	}
	declaration := domain.FileUploadRequest{Name: file.Name, MediaType: file.MediaType, Size: file.Size, SHA256: file.SHA256}
	if err := declaration.Validate(); err != nil {
		return "", err
	}
	sender, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, api.Path("agents", file.OwnerID), nil)
	if err != nil {
		return "", err
	}
	if sender.ID != file.OwnerID {
		return "", domain.Fail(domain.Internal, "private transfer sender observation belongs to another identity")
	}
	folder := sender.ID
	if label, err := domain.AgentUsername(sender.Name); err == nil {
		folder = label + "-" + sender.ID
	}
	if err := os.MkdirAll(c.WorkspaceRoot, 0700); err != nil {
		return "", err
	}
	root, _, err := filetransfer.Root(filepath.Join(c.WorkspaceRoot, "downloads"))
	if err != nil {
		return "", err
	}
	if err := root.Close(); err != nil {
		return "", err
	}
	if err := secure.SyncDir(filepath.Dir(c.WorkspaceRoot)); err != nil {
		return "", err
	}
	directory := c.WorkspaceRoot
	for _, name := range []string{"downloads", folder, file.ID} {
		parent := directory
		directory = filepath.Join(parent, name)
		if err := secure.Dir(directory); err != nil {
			return "", err
		}
		if err := secure.SyncDir(parent); err != nil {
			return "", err
		}
	}
	return filepath.Join(directory, file.Name), nil
}

func (e *Engine) transferAdmission(ctx context.Context, me domain.Agent) error {
	current, err := api.Call[domain.Agent](ctx, e.Client, http.MethodGet, "/v1/me", nil)
	if err != nil {
		return err
	}
	if current.ID != me.ID || !current.Active || current.Banned || current.Mode != "ai" || current.Revision != me.Revision {
		return domain.Fail(domain.Denied, "current identity or Founder mode changed during private transfer")
	}
	for _, key := range []string{"maintenance", "session-fence"} {
		var hold struct {
			Active bool `json:"active"`
		}
		if _, err := e.State.Get(ctx, key, &hold); err != nil {
			return err
		}
		if hold.Active {
			return domain.Fail(domain.Prerequisite, "current maintenance or session cleanup hold blocks private transfer")
		}
	}
	owned, err := api.Call[[]domain.Task](ctx, e.Client, http.MethodGet, "/v1/ownership", nil)
	if err != nil {
		return err
	}
	if len(owned) != 0 {
		return domain.Fail(domain.Prerequisite, "current server work ownership blocks private transfer")
	}
	sessions, err := api.Call[[]domain.Session](ctx, e.Client, http.MethodGet, "/v1/sessions", nil)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if (session.TargetID == me.ID || session.RequesterID == me.ID) && (session.State == "active" || (session.State == "consented" && session.OpenRequested)) {
			return domain.Fail(domain.Prerequisite, "current interactive session admission blocks private transfer")
		}
	}
	return nil
}

func (e *Engine) receiveFileTransfers(ctx context.Context, c config.Worker, me domain.Agent) (handled bool, blockers []Blocker, err error) {
	if me.Mode != "ai" {
		return false, nil, nil
	}
	cursor := ""
	for {
		path := "/v1/messages?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, http.MethodGet, path, nil)
		if err != nil {
			return false, blockers, err
		}
		for _, notice := range page.Items {
			if notice.Kind != "direct" || notice.Origin != "service" || notice.RecipientID != me.ID ||
				notice.State != "pending" || notice.Request != nil || len(notice.Attachments) != 1 {
				continue
			}
			reference := notice.Attachments[0]
			operation := "receive-" + reference.ID
			var retained filetransfer.Proposal
			found, err := e.State.Get(ctx, "file-download:"+operation, &retained)
			if err != nil {
				return false, blockers, err
			}
			file, err := api.Call[domain.SharedFile](ctx, e.Client, http.MethodGet, api.Path("files", reference.ID), nil)
			if err != nil {
				fault := domain.AsFault(err)
				if fault.Code == domain.Denied || fault.Code == domain.NotFound {
					blockers = append(blockers, Blocker{Code: fault.Code, Reason: "An attached service notice is no longer readable; no file receipt was sent."})
					continue
				}
				return false, blockers, err
			}
			if file.TransferTo != me.ID {
				continue
			}
			if file.FileReference != reference || file.OwnerID != notice.SenderID || file.TeamReadable ||
				(file.State != "ready" && !(found && retained.Complete && file.State == "deleted")) {
				blockers = append(blockers, Blocker{Code: domain.Conflict, Reason: "A private transfer no longer matches its exact sender, recipient or available immutable content; no file receipt was sent."})
				continue
			}
			destination, err := e.transferDestination(ctx, c, file, retained, found)
			if err != nil {
				return false, blockers, err
			}
			if err := e.phaseHealth(ctx, "checking", nil, "", 300, false); err != nil {
				return false, blockers, err
			}
			action, cancel := context.WithCancelCause(ctx)
			deadline, stop := context.WithTimeout(action, 300*time.Second)
			validate := func(check context.Context) error {
				if err := e.transferAdmission(check, me); err != nil {
					return err
				}
				latest, err := api.Call[domain.SharedFile](check, e.Client, http.MethodGet, api.Path("files", reference.ID), nil)
				if err != nil {
					return err
				}
				if latest.FileReference != reference || latest.TransferTo != me.ID || latest.OwnerID != notice.SenderID || latest.TeamReadable {
					return domain.Fail(domain.Denied, "private transfer grant, recipient or immutable declaration changed")
				}
				return nil
			}
			if err := e.guardAuthority(deadline, cancel, &authoritySubscription{modeOnly: true, messageID: notice.ID, fileID: reference.ID}, validate); err != nil {
				stop()
				cancel(err)
				return false, blockers, err
			}
			receipt, err := filetransfer.Download(deadline, e.Client, e.State, operation, reference.ID,
				destination, validate)
			err = errors.Join(context.Cause(deadline), err)
			stop()
			cancel(err)
			if err != nil {
				return false, blockers, err
			}
			if receipt.FileID != reference.ID {
				return false, blockers, domain.Fail(domain.Internal, "private transfer receipt belongs to another file")
			}
			request := domain.MessageReceipt{Command: domain.Command{OperationID: operation + ".notice", ExpectedRevision: notice.Revision}, State: "handled"}
			_, err = api.Call[domain.Message](ctx, e.Client, http.MethodPost, api.Path("messages", notice.ID, "receipt"), request)
			return true, blockers, err
		}
		cursor = page.NextCursor
		if cursor == "" {
			return false, blockers, nil
		}
	}
}
