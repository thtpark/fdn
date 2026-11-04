//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

type interruptedAuthorityTransport struct {
	base        http.RoundTripper
	interrupted <-chan struct{}
}

func (t interruptedAuthorityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.Path != "/v1/updates" || request.URL.Query().Get("wait") != "true" {
		return t.base.RoundTrip(request)
	}
	select {
	case <-t.interrupted:
		return nil, errors.New("synthetic authority stream interruption")
	default:
	}
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	go func() {
		select {
		case <-t.interrupted:
			cancel()
		case <-ctx.Done():
		}
	}()
	return t.base.RoundTrip(request.WithContext(ctx))
}

func TestInterruptedExecutionReportsAuthorityChannelInsteadOfProviderFailure(t *testing.T) {
	h := newHarness(t)
	x := &executor{}
	engine := newEngine(t, h, x)
	task := readyForEngine(t, h, engine)
	skipHealthOnboarding(t, engine)
	if _, err := engine.Step(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	x.entered, x.block = make(chan struct{}), make(chan struct{})
	interrupted := make(chan struct{})
	h.worker.HTTP.Transport = interruptedAuthorityTransport{h.worker.HTTP.Transport, interrupted}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Serve(ctx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("controller did not join after authority interruption")
		}
	})
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("coding provider did not start")
	}
	close(interrupted)
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := api.Call[domain.Task](context.Background(), h.founder, "GET", api.Path("tasks", task.ID), nil)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.Paused {
			if !current.UnsafeRetry {
				t.Fatal("interrupted coding was made automatically retryable")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authority interruption did not stop the admitted coding execution")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runs, err := api.Call[[]domain.Run](context.Background(), h.founder, "GET", api.Path("tasks", task.ID, "runs"), nil)
	if err != nil || len(runs) != 1 || runs[0].State != "stopped" {
		t.Fatal("coding interruption was not retained as exactly one stopped run", err)
	}
	if !strings.Contains(runs[0].Summary, "Foundation authority update channel was interrupted") ||
		strings.Contains(runs[0].Summary, "synthetic authority stream") || x.count("execution") != 1 {
		t.Fatal("public interruption report lost its safe specific cause or repeated inference", runs[0].Summary)
	}
}
