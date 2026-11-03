package provider

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type invocationOutput struct {
	mu        sync.Mutex
	buffer    *process.BoundedBuffer
	file      *os.File
	remaining int
}

func newInvocationOutput(path string, limit int) (*invocationOutput, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return &invocationOutput{buffer: process.NewBuffer(limit), file: file, remaining: limit}, nil
}

func (output *invocationOutput) Write(data []byte) (int, error) {
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

func (output *invocationOutput) finish() ([]byte, error) {
	data, err := output.buffer.Bytes()
	return data, errors.Join(err, output.file.Sync(), output.file.Close(), secure.SyncDir(filepath.Dir(output.file.Name())))
}
