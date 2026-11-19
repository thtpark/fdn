package process

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/thtpark/fdn/internal/secure"
)

type PrivateOutput struct {
	mu        sync.Mutex
	buffer    *BoundedBuffer
	file      *os.File
	remaining int
}

func NewPrivateOutput(path string, limit int) (*PrivateOutput, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return &PrivateOutput{buffer: NewBuffer(limit), file: file, remaining: limit}, nil
}

func (output *PrivateOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	length := len(data)
	if _, err := output.buffer.Write(data); err != nil {
		return 0, err
	}
	part := data[:min(length, output.remaining)]
	written, err := output.file.Write(part)
	output.remaining -= written
	if err != nil {
		return written, err
	}
	if written != len(part) {
		return written, io.ErrShortWrite
	}
	return length, nil
}

func (output *PrivateOutput) Finish() ([]byte, error) {
	data, err := output.buffer.Bytes()
	return data, errors.Join(err, output.file.Sync(), output.file.Close(), secure.SyncDir(filepath.Dir(output.file.Name())))
}
