package domain

import (
	"mime"
	"slices"
	"strings"
	"unicode"
)

const (
	MaxFileSize        int64 = 1 << 30
	FileChunkSize            = 256 << 10
	MaxAttachments           = 32
	MaxPublicationBody       = 65536
)

// A file ID is an immutable content version, never a filesystem path or URL.
type FileReference struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type SharedFile struct {
	FileReference
	OwnerID             string `json:"owner_id"`
	State               string `json:"state"`
	UploadedBytes       int64  `json:"uploaded_bytes"`
	TeamReadable        bool   `json:"team_readable"`
	TransferTo          string `json:"transfer_to,omitempty"`
	DeleteAfterDownload bool   `json:"delete_after_download"`
	Revision            int64  `json:"revision"`
	CreatedAt           int64  `json:"created_at"`
	FinalizedAt         int64  `json:"finalized_at,omitempty"`
	PublishedAt         int64  `json:"published_at,omitempty"`
	PublishedBy         string `json:"published_by,omitempty"`
	DeletedAt           int64  `json:"deleted_at,omitempty"`
}

type FileUploadRequest struct {
	Command
	Name                string `json:"name"`
	MediaType           string `json:"media_type"`
	Size                int64  `json:"size"`
	SHA256              string `json:"sha256"`
	TransferTo          string `json:"transfer_to,omitempty"`
	DeleteAfterDownload bool   `json:"delete_after_download"`
}

type FileChunkRequest struct {
	Command
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

type FileProgress struct {
	FileID        string `json:"file_id"`
	UploadedBytes int64  `json:"uploaded_bytes"`
}

type FileChunk struct {
	FileID string `json:"file_id"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

type FileChangeRequest struct {
	Command
	Reason string `json:"reason"`
}

type FileGrantRequest struct {
	Command
	AgentID string `json:"agent_id"`
	Revoke  bool   `json:"revoke"`
	Reason  string `json:"reason"`
}

type FileGrant struct {
	FileID    string `json:"file_id"`
	AgentID   string `json:"agent_id"`
	GrantedBy string `json:"granted_by"`
	GrantedAt int64  `json:"granted_at"`
}

type FileDownloadRequest struct {
	Command
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type FileDownloadReceipt struct {
	FileID         string `json:"file_id"`
	ReaderID       string `json:"reader_id"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	ReceivedAt     int64  `json:"received_at"`
	NotificationID string `json:"notification_id"`
	ContentDeleted bool   `json:"content_deleted"`
}

type Publication struct {
	ID                string          `json:"id"`
	VersionID         string          `json:"version_id"`
	Kind              string          `json:"kind"`
	Scope             string          `json:"scope"`
	TargetID          string          `json:"target_id,omitempty"`
	Title             string          `json:"title"`
	Body              string          `json:"body"`
	Attachments       []FileReference `json:"attachments"`
	SHA256            string          `json:"sha256"`
	CreatedBy         string          `json:"created_by"`
	CreatedAt         int64           `json:"created_at"`
	UpdatedBy         string          `json:"updated_by"`
	UpdatedAt         int64           `json:"updated_at"`
	Revision          int64           `json:"revision"`
	Archived          bool            `json:"archived"`
	AllowProjectEdits bool            `json:"allow_project_edits"`
}

type PublicationRequest struct {
	Command
	Kind              string   `json:"kind"`
	Scope             string   `json:"scope"`
	TargetID          string   `json:"target_id,omitempty"`
	Title             string   `json:"title"`
	Body              string   `json:"body"`
	AttachmentIDs     []string `json:"attachment_ids"`
	AllowProjectEdits bool     `json:"allow_project_edits"`
}

type PublicationVersion struct {
	ID            string      `json:"id"`
	PublicationID string      `json:"publication_id"`
	Publication   Publication `json:"publication"`
}

type PublicationChange struct {
	Command
	Title             *string   `json:"title,omitempty"`
	Body              *string   `json:"body,omitempty"`
	AttachmentIDs     *[]string `json:"attachment_ids,omitempty"`
	Archived          *bool     `json:"archived,omitempty"`
	AllowProjectEdits *bool     `json:"allow_project_edits,omitempty"`
	Reason            string    `json:"reason"`
}

func (r FileUploadRequest) Validate() error {
	if err := CheckText(r.Name, "file name", 255, true); err != nil {
		return err
	}
	if r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, `/\`) ||
		strings.IndexFunc(r.Name, unicode.IsControl) >= 0 {
		return Fail(Invalid, "file name must be a single safe name, not a path")
	}
	mediaType, parameters, err := mime.ParseMediaType(r.MediaType)
	if err != nil || len(parameters) != 0 || mediaType != r.MediaType || len(mediaType) > 128 ||
		!strings.Contains(mediaType, "/") {
		return Fail(Invalid, "media_type must be a canonical MIME type without parameters")
	}
	if r.Size < 0 || r.Size > MaxFileSize || !ValidHash(r.SHA256) {
		return Fail(Invalid, "file requires a SHA-256 and a size between zero and 1 GiB")
	}
	if r.DeleteAfterDownload && r.TransferTo == "" {
		return Fail(Invalid, "automatic deletion requires one explicit transfer recipient")
	}
	return nil
}

func InlineMedia(mediaType string) bool {
	return slices.Contains([]string{
		"image/png", "image/jpeg", "image/gif", "image/webp",
		"video/mp4", "video/webm", "video/quicktime",
	}, mediaType)
}
