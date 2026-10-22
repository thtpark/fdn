//go:build integration

package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/work"
)

type echoEndpoint struct{ opened atomic.Int64 }

func (e *echoEndpoint) Open(_ context.Context, _ domain.Session, _ config.Worker) (io.ReadWriteCloser, error) {
	e.opened.Add(1)
	left, right := net.Pipe()
	go func() { defer right.Close(); io.Copy(right, right) }()
	return left, nil
}

func TestAutonomousTargetUsesRetainedGrantAndExplicitOpenWithoutLocalConsent(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := filepath.Join(t.TempDir(), "worker")
	c := config.DefaultWorker(root)
	c.URL = h.server.URL
	c.CAFile = filepath.Join(h.root, "ca.pem")
	c.AutomaticProviderUpdates = false
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	endpoint := &echoEndpoint{}
	manager := &remote.Manager{Client: h.worker, State: state, Config: c, Opener: endpoint, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	task := h.ready(t)
	grant := grantFor(t, h, h.founderAgent.ID, h.workerAgent.ID, "tmux", "terminal", task.ProjectID)
	session, err := api.Call[domain.Session](ctx, h.founder, "POST", "/v1/sessions", domain.SessionRequest{Command: command(), GrantID: grant.ID, TargetID: h.workerAgent.ID, ProjectID: task.ProjectID, Mode: "tmux", Purpose: "terminal", Reason: "recorded endpoint adapter", ExpiresAt: time.Now().Unix() + 90})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(550 * time.Millisecond)
	if endpoint.opened.Load() != 0 {
		t.Fatal("request launched endpoint")
	}
	if session.State != "consented" {
		t.Fatal("retained authorization did not eliminate repeated local consent")
	}
	change := domain.SessionChange{Command: command(), Action: "open"}
	change.ExpectedRevision = session.Revision
	session, err = api.Call[domain.Session](ctx, h.founder, "POST", api.Path("sessions", session.ID), change)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for endpoint.opened.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if endpoint.opened.Load() != 1 {
		t.Fatal("explicit open plus consent did not start exactly one endpoint")
	}
	stream, err := remote.Dial(ctx, h.founder, session.ID, "requester")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("human-only bytes")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("human-only bytes"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "human-only bytes" {
		t.Fatal(string(buffer), err)
	}
	engine := work.New(c, "", h.worker, state, &executor{})
	if _, err := engine.Step(ctx, true); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("interactive endpoint did not reserve the shared worker slot", err)
	}
	session, err = api.Call[domain.Session](ctx, h.worker, "GET", api.Path("sessions", session.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	change = domain.SessionChange{Command: command(), Action: "revoke"}
	change.ExpectedRevision = session.Revision
	if _, err := api.Call[domain.Session](ctx, h.worker, "POST", api.Path("sessions", session.ID), change); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		var fence remote.Fence
		if _, err := state.Get(ctx, "session-fence", &fence); err != nil {
			t.Fatal(err)
		}
		if !fence.Active {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("closed endpoint retained its work-slot fence after verified cleanup")
}
