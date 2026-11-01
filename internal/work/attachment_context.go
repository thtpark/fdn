package work

import (
	"encoding/json"

	"github.com/thtpark/fdn/internal/domain"
)

func attachmentContextSize(files []domain.FileReference) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	data, err := json.Marshal(files)
	return len(data), err
}

func messageContextSize(message domain.Message) (int, error) {
	size, err := attachmentContextSize(message.Attachments)
	if err != nil {
		return 0, err
	}
	if message.FileDownload != nil {
		data, err := json.Marshal(message.FileDownload)
		if err != nil {
			return 0, err
		}
		size += len(data)
	}
	return len(message.Body) + size, nil
}

func entryContextSize(entry domain.Entry) (int, error) {
	size, err := attachmentContextSize(entry.Attachments)
	return len(entry.Body) + size, err
}
