package work

import (
	"encoding/json"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAttachmentContextBudgetsMetadataWithoutFetchingPrivateBytes(t *testing.T) {
	message := domain.Message{Body: "original"}
	size, err := messageContextSize(message)
	if err != nil || size != len(message.Body) {
		t.Fatal("unchanged body-only context budget changed", size, err)
	}
	message.Attachments = []domain.FileReference{{ID: "file_fixture", Name: "private-reference", MediaType: "text/plain",
		Size: domain.MaxFileSize, SHA256: domain.Hash("never fetch the bytes")}}
	message.FileDownload = &domain.FileDownloadReceipt{FileID: "file_fixture", ReaderID: "agent_fixture", Size: domain.MaxFileSize}
	encodedFiles, err := json.Marshal(message.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	encodedReceipt, err := json.Marshal(message.FileDownload)
	if err != nil {
		t.Fatal(err)
	}
	size, err = messageContextSize(message)
	if err != nil || size != len(message.Body)+len(encodedFiles)+len(encodedReceipt) || size >= int(domain.MaxFileSize) {
		t.Fatal("file metadata was omitted from budget or bytes were implied", size, err)
	}
	entry := domain.Entry{Body: "worklog", Attachments: message.Attachments}
	size, err = entryContextSize(entry)
	if err != nil || size != len(entry.Body)+len(encodedFiles) {
		t.Fatal("discussion attachments escaped the bounded invocation context", size, err)
	}
}
