package foundation

import (
	"context"
	"database/sql"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) resolveAttachments(ctx context.Context, tx *sql.Tx, actor, recipient string, ids []string) ([]domain.FileReference, error) {
	if len(ids) > domain.MaxAttachments {
		return nil, domain.Fail(domain.Invalid, "at most 32 files may be attached")
	}
	references := make([]domain.FileReference, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, domain.Fail(domain.Invalid, "duplicate attachment")
		}
		seen[id] = true
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if err := requireFileReader(ctx, tx, actor, file); err != nil {
			return nil, err
		}
		if file.State != "ready" {
			return nil, domain.Fail(domain.Prerequisite, "only finalized available content may be attached")
		}
		if recipient == "" {
			if !file.TeamReadable {
				return nil, domain.Fail(domain.Denied, "shared-record attachments require the owner's explicit team publication first")
			}
		} else if !file.TeamReadable {
			if file.TransferTo != "" && recipient != file.TransferTo && recipient != file.OwnerID {
				return nil, domain.Fail(domain.Denied, "private transfer attachment must stay with its designated recipient")
			}
			if actor == file.OwnerID && recipient != actor {
				grant := domain.FileGrant{FileID: id, AgentID: recipient, GrantedBy: actor, GrantedAt: s.timestamp()}
				changed, err := saveFileGrant(ctx, tx, grant)
				if err != nil {
					return nil, err
				}
				if changed {
					file.Revision++
					if err := saveFile(ctx, tx, file); err != nil {
						return nil, err
					}
					if err := fileUpdate(ctx, tx, file); err != nil {
						return nil, err
					}
				}
			} else if err := requireFileReader(ctx, tx, recipient, file); err != nil {
				if domain.AsFault(err).Code != domain.Denied {
					return nil, err
				}
				return nil, domain.Fail(domain.Denied, "a reader cannot re-share private content without an owner-issued grant for the message recipient")
			}
		}
		references = append(references, file.FileReference)
	}
	return references, nil
}

func checkInlineAttachments(body string, references []domain.FileReference) error {
	ids := fileIDs(references)
	for _, id := range inlineFileIDs(body) {
		if !slices.Contains(ids, id) {
			return domain.Fail(domain.Invalid, "Foundation Markdown file references must identify an attached file")
		}
	}
	return nil
}
