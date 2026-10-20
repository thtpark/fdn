//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type executor struct {
	mu      sync.Mutex
	phases  []string
	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (x *executor) Check(_ context.Context, name string) (provider.Health, error) {
	return provider.Health{Provider: name, Version: "1.2.3", Path: "/synthetic/provider", Authentication: "authenticated", Models: []string{"fixture-model"}}, nil
}
func (x *executor) Invoke(ctx context.Context, inv provider.Invocation) (provider.Reply, error) {
	_, raw, _ := strings.Cut(inv.Prompt, "\nFDN_CONTEXT_JSON\n")
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	x.mu.Lock()
	x.phases = append(x.phases, launch.Phase)
	x.mu.Unlock()
	if inv.Started != nil {
		if err := inv.Started(123456); err != nil {
			return provider.Reply{}, err
		}
	}
	if x.entered != nil {
		x.once.Do(func() { close(x.entered) })
	}
	if x.block != nil {
		select {
		case <-x.block:
		case <-ctx.Done():
			return provider.Reply{}, ctx.Err()
		}
	}
	reply := provider.Reply{Outcome: "clear", Summary: "exact context reviewed", Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}}
	switch launch.Phase {
	case "onboarding":
		reply.Outcome = "reviewed"
	case "execution":
		reply.Outcome = "completed"
		reply.Evidence = []string{"synthetic check"}
	case "message", "attention":
		reply.Outcome = "responded"
	case "research":
		reply.Outcome = "researched"
	}
	return reply, nil
}
func (x *executor) count(phase string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	count := 0
	for _, p := range x.phases {
		if p == phase {
			count++
		}
	}
	return count
}

func newEngine(t *testing.T, h *harness, x *executor) *work.Engine {
	t.Helper()
	root := filepath.Join(t.TempDir(), "worker")
	c := config.DefaultWorker(root)
	c.CAFile = filepath.Join(h.root, "ca.pem")
	c.URL = h.server.URL
	c.AutomaticProviderUpdates = false
	c.AllowWorkspaceTools = true
	c.PollSeconds = 1
	state, err := local.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	e := work.New(c, "", h.worker, state, x)
	e.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return e
}

func readyForEngine(t *testing.T, h *harness, e *work.Engine) domain.Task {
	t.Helper()
	ctx := context.Background()
	task := h.ready(t)
	path := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	binding, err := e.State.BindWorkspace(ctx, path, true, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	task = h.change(t, h.worker, task.ID, "workspace", func(r *domain.TaskChange) { r.WorkspaceRef = binding.Ref })
	return h.change(t, h.founder, task.ID, "approve", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
}

func TestSharedWorkReviewClaimReportAndNoIdleInference(t *testing.T) {
	h := newHarness(t)
	x := &executor{}
	e := newEngine(t, h, x)
	task := readyForEngine(t, h, e)
	ctx := context.Background()
	for range 3 {
		if _, err := e.Step(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || current.Status != domain.Completed || current.ResultGeneration != 1 {
		t.Fatal(current, err)
	}
	if x.count("onboarding") != 1 || x.count("preflight") != 1 || x.count("execution") != 1 {
		t.Fatal(x.phases)
	}
	for range 5 {
		if _, err := e.Step(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(x.phases) != 3 {
		t.Fatal("idle loop performed inference", x.phases)
	}
}

type lostAck struct {
	base http.RoundTripper
	lost atomic.Bool
}

func (l *lostAck) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := l.base.RoundTrip(r)
	if err == nil && strings.HasSuffix(r.URL.Path, "/report") && l.lost.CompareAndSwap(false, true) {
		_, copyErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		return nil, errors.Join(errors.New("synthetic lost acknowledgment"), copyErr, closeErr)
	}
	return response, err
}

func TestLostReportAcknowledgmentReplaysWithoutInference(t *testing.T) {
	h := newHarness(t)
	x := &executor{}
	e := newEngine(t, h, x)
	task := readyForEngine(t, h, e)
	ctx := context.Background()
	original := h.worker.HTTP.Transport
	h.worker.HTTP.Transport = &lostAck{base: original}
	defer func() { h.worker.HTTP.Transport = original }()
	for range 2 {
		if _, err := e.Step(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Step(ctx, true); err == nil || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("lost acknowledgment did not remain pending", err)
	}
	outbox, err := e.State.Outbox(ctx)
	if err != nil || len(outbox) != 1 {
		t.Fatal(outbox, err)
	}
	restarted := work.New(e.Config, "", h.worker, e.State, x)
	restarted.Log = e.Log
	if _, err := restarted.Step(ctx, false); err != nil {
		t.Fatal(err)
	}
	if x.count("execution") != 1 {
		t.Fatal("provider reinvoked", x.phases)
	}
	outbox, err = e.State.Outbox(ctx)
	if err != nil || len(outbox) != 0 {
		t.Fatal(outbox, err)
	}
	current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || current.ResultGeneration != 1 || current.Attempts != 1 {
		t.Fatal(current, err)
	}
}

func TestOrphanServerClaimBlocksEvenInitialReview(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task := h.ready(t)
	req := domain.ClaimRequest{Command: command(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}
	req.ExpectedRevision = task.Revision
	if _, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), req); err != nil {
		t.Fatal(err)
	}
	x := &executor{}
	engine := newEngine(t, h, x)
	if _, err := engine.Step(ctx, true); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("orphan claim did not fence inference", err)
	}
	if x.count("onboarding") != 0 || x.count("execution") != 0 {
		t.Fatal("replacement state launched inference while original claim remained active")
	}
}

func TestManualServiceRaceAndInterruptedFence(t *testing.T) {
	h := newHarness(t)
	x := &executor{entered: make(chan struct{}), block: make(chan struct{})}
	e := newEngine(t, h, x)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { _, err := e.Step(ctx, true); done <- err }()
	select {
	case <-x.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("review never started")
	}

	if _, err := e.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("manual/service double admission", err)
	}
	close(x.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	x.block = nil
	if x.count("onboarding") != 1 {
		t.Fatal(x.phases)
	}
	task := readyForEngine(t, h, e)
	task = h.change(t, h.worker, task.ID, "clarity", func(r *domain.TaskChange) { r.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: command(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	run, err := api.Call[domain.Run](ctx, h.worker, "POST", api.Path("tasks", task.ID, "claim"), claim)
	if err != nil {
		t.Fatal(err)
	}
	j := local.Journal{ID: "interrupted-fixture", Kind: "execution", State: "invoking", TaskID: task.ID, RunID: run.ID, SpecGeneration: task.SpecGeneration, Token: token}
	if err := e.State.SaveJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := e.Step(ctx, true); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal("interrupted execution not fenced", err)
		}
	}
	if x.count("execution") != 0 {
		t.Fatal("interrupted process relaunched")
	}
	if err := e.Recover(ctx, j.ID, false); err == nil {
		t.Fatal("recovery omitted stopped acknowledgment")
	}
	if err := e.Recover(ctx, j.ID, true); err != nil {
		t.Fatal(err)
	}
	current, err := api.Call[domain.Task](ctx, h.worker, "GET", api.Path("tasks", task.ID), nil)
	if err != nil || !current.UnsafeRetry || current.ActiveRun != nil || current.Status != domain.Paused {
		t.Fatal(current, err)
	}
}
