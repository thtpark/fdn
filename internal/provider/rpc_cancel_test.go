package provider

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/process"
)

type cancellationProbeRunner struct {
	request chan struct{}
	stopped chan struct{}
}

func (r cancellationProbeRunner) Run(ctx context.Context, spec process.Spec) error {
	defer close(r.stopped)
	reader := bufio.NewReader(spec.Stdin)
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			break
		}
		if value, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return err
			}
		}
	}
	if length < 1 || length > 4096 {
		return errors.New("unexpected status probe frame")
	}
	if _, err := io.CopyN(io.Discard, reader, int64(length)); err != nil {
		return err
	}
	close(r.request)
	<-ctx.Done()
	// exec.Cmd.Wait also joins its stdin copier. A killed process cannot unblock
	// that copier unless the owner closes the source pipe.
	_, _ = io.Copy(io.Discard, reader)
	return ctx.Err()
}

func TestCopilotAuthenticationCancellationClosesOwnedPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := cancellationProbeRunner{request: make(chan struct{}), stopped: make(chan struct{})}
	manager := &Manager{Runner: runner}
	finished := make(chan error, 1)
	go func() {
		_, _, err := manager.copilotAuthentication(ctx, "/fixture/copilot")
		finished <- err
	}()
	select {
	case <-runner.request:
	case <-time.After(time.Second):
		t.Fatal("status probe did not send its initial request")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("status-probe cancellation changed the failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("status probe waited for a killed child's pipe indefinitely")
	}
	select {
	case <-runner.stopped:
	default:
		t.Fatal("status probe returned before joining its owned process")
	}
}
