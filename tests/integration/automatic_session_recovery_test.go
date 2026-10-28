//go:build integration

package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/remote"
)

type recoveringSessionOpener struct {
	opened atomic.Int64
	closes atomic.Int64
}

func (o *recoveringSessionOpener) Open(context.Context, domain.Session, config.Worker) (io.ReadWriteCloser, error) {
	o.opened.Add(1)
	left, right := net.Pipe()
	go func() {
		defer right.Close()
		_, _ = io.Copy(io.Discard, right)
	}()
	return &recoveringSessionEndpoint{ReadWriteCloser: left, owner: o}, nil
}

type recoveringSessionEndpoint struct {
	io.ReadWriteCloser
	owner *recoveringSessionOpener
	once  sync.Once
}

func (e *recoveringSessionEndpoint) Close() error {
	var problem error
	e.once.Do(func() { problem = e.ReadWriteCloser.Close() })
	if e.owner.closes.Add(1) <= 2 {
		return errors.Join(problem, &process.CleanupError{Err: errors.New("temporary owned session cleanup failure")})
	}
	return problem
}

func TestAutomaticSessionCleanupResumesExistingAIWorkWithoutHumanRecoveryOrInferenceRetry(t *testing.T) {
	h := newHarness(t)
	x := &executor{}
	engine := newEngine(t, h, x)
	skipHealthOnboarding(t, engine)
	ctx, cancel := context.WithCancel(context.Background())
	opener := &recoveringSessionOpener{}
	manager := &remote.Manager{Client: h.worker, State: engine.State, Config: engine.Config,
		Opener: opener, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- engine.Serve(ctx, manager) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	grant := grantFor(t, h, h.founderAgent.ID, h.workerAgent.ID, "tmux", "terminal", "")
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", domain.SessionRequest{
		Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, Mode: "tmux", Purpose: "terminal",
		Reason: "Isolated automatic-cleanup fixture", ExpiresAt: time.Now().Unix() + 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: session.Revision}, Action: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for opener.opened.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("authorized interactive endpoint did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Body: "Reply after the exact interactive endpoint is safely cleaned.",
		Kind: "direct", Origin: "human", ResponseRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if x.count("message") != 0 {
		t.Fatal("live interactive session did not exclude automatic inference")
	}
	session, err = api.Call[domain.Session](ctx, h.founder, "GET", api.Path("sessions", session.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: command().OperationID, ExpectedRevision: session.Revision}, Action: "close",
	}); err != nil {
		t.Fatal(err)
	}
	for {
		current, err := api.Call[domain.Message](ctx, h.founder, "GET", api.Path("messages", message.ID), nil)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == "handled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic work did not resume after owned cleanup retry", opener.closes.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	var fence remote.Fence
	if _, err := engine.State.Get(ctx, "session-fence", &fence); err != nil || fence.Active {
		t.Fatal("automatic work bypassed rather than retired the fence", fence, err)
	}
	me, err := api.Call[domain.Agent](ctx, h.worker, "GET", "/v1/me", nil)
	if err != nil || me.Mode != "ai" || me.Revision != h.workerAgent.Revision {
		t.Fatal("local cleanup mutated authoritative mode or revision", me, err)
	}
	if opener.opened.Load() != 1 || opener.closes.Load() != 3 || x.count("message") != 1 {
		t.Fatal("automatic recovery reopened tools or replayed inference", opener.opened.Load(), opener.closes.Load(), x.count("message"))
	}
}
