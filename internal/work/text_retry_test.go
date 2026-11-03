package work

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
)

type stoppedTextExecutor struct {
	provider.Executor
	err   error
	calls int
}

func (executor *stoppedTextExecutor) Invoke(_ context.Context, invocation provider.Invocation) (provider.Reply, error) {
	executor.calls++
	if invocation.Coding || invocation.Workspace != "" {
		return provider.Reply{}, &process.CleanupError{Err: errors.New("fixture must remain tool-disabled")}
	}
	if invocation.Started != nil {
		if err := invocation.Started(12345); err != nil {
			return provider.Reply{}, err
		}
	}
	if executor.err == nil {
		return provider.Reply{Outcome: "responded", Summary: "Reply after a settled transient interruption"}, nil
	}
	return provider.Reply{}, executor.err
}

func TestStoppedTextRetrySuccessClearsOnlyItsSourceWithoutRepeatingInference(t *testing.T) {
	f := sharedAuthorityFixture(t)
	x := &stoppedTextExecutor{Executor: f.executor, err: context.DeadlineExceeded}
	f.engine.Executor = x
	now := time.Now().UTC()
	f.engine.Now = func() time.Time { return now }
	message := domain.Message{ID: "message_retry_success", Revision: 1, SenderID: "agent_founder",
		RecipientID: f.me.ID, Kind: "direct", State: "pending", ResponseRequest: true}
	f.messages[message.ID] = message
	launch := Launch{Phase: "message", AgentID: f.me.ID, Message: &message}
	if _, err := f.engine.text(context.Background(), f.engine.Config, launch, false); err == nil {
		t.Fatal("fixture transient failure was hidden")
	}
	blocks, err := f.engine.textResponseBlocks(context.Background())
	if err != nil || len(blocks) != 1 {
		t.Fatal(blocks, err)
	}
	now = time.Unix(blocks[0].RetryAt, 0)
	x.err = nil
	journal, err := f.engine.text(context.Background(), f.engine.Config, launch, false)
	if err != nil || journal.State != "done" || x.calls != 2 {
		t.Fatal("bounded retry did not finish its original source", journal, x.calls, err)
	}
	blocks, err = f.engine.textResponseBlocks(context.Background())
	if err != nil || len(blocks) != 0 || f.messages[message.ID].State != "handled" {
		t.Fatal("successful retry left its source pending or blocked", blocks, err)
	}
	for range 2 {
		acted, err := f.engine.messages(context.Background(), f.engine.Config, false, false)
		if err != nil || acted || x.calls != 2 {
			t.Fatal("handled source repeated inference", acted, x.calls, err)
		}
	}
}

func TestStoppedTextRetriesAreBoundedSourceScopedAndRestartDurable(t *testing.T) {
	f := sharedAuthorityFixture(t)
	x := &stoppedTextExecutor{Executor: f.executor, err: context.Canceled}
	f.engine.Executor = x
	now := time.Now().UTC()
	f.engine.Now = func() time.Time { return now }
	launch := Launch{Phase: "message", AgentID: f.me.ID, Message: &domain.Message{
		ID: "message_transient", Revision: 1, SenderID: "agent_founder", RecipientID: f.me.ID, Kind: "direct", State: "pending", ResponseRequest: true}}
	var last local.Journal
	for attempt := 1; attempt <= maxTextAttempts; attempt++ {
		schedule := &workDeadline{}
		ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
		journal, err := f.engine.text(ctx, f.engine.Config, launch, false)
		var fenced textResponseFenced
		if !errors.As(err, &fenced) || journal.State != "text-failed" || x.calls != attempt {
			t.Fatal("settled text failure was not retained at its source", journal, x.calls, err)
		}
		last = journal
		pending, err := f.engine.State.Journals(ctx, true)
		if err != nil || len(pending) != 0 {
			t.Fatal("settled tool-disabled failure poisoned global work admission", pending, err)
		}
		blocks, err := f.engine.textResponseBlocks(ctx)
		if err != nil || len(blocks) != 1 || blocks[0].Attempts != attempt || blocks[0].JournalID != journal.ID {
			t.Fatal("retry budget or private evidence reference was not retained", blocks, err)
		}
		unrelated := launch
		copy := *launch.Message
		copy.ID = "message_unrelated"
		unrelated.Message = &copy
		if err := f.engine.textResponseBlocked(ctx, f.engine.Config, unrelated); err != nil {
			t.Fatal("another message inherited the failed source's retry fence", err)
		}
		previous := f.engine
		f.engine = New(previous.Config, "", previous.Client, previous.State, x)
		f.engine.Now, f.engine.Log = previous.Now, previous.Log
		f.engine.feedRunning, f.engine.feedOnline = true, true
		for range 2 {
			_, err := f.engine.text(WithRequestedAction(ctx, domain.NewID("manual")), f.engine.Config, launch, false)
			if !errors.As(err, &fenced) || x.calls != attempt {
				t.Fatal("poll, restart or a new operation bypassed retained backoff/budget", x.calls, err)
			}
		}
		if attempt < maxTextAttempts {
			if blocks[0].State != "retry-wait" || schedule.at.Unix() != blocks[0].RetryAt {
				t.Fatal("the exact bounded retry did not retain its due wake", blocks, schedule.at)
			}
			now = time.Unix(blocks[0].RetryAt, 0)
		} else if blocks[0].State != "blocked" || blocks[0].RetryAt != 0 {
			t.Fatal("exhausted planning continued retrying automatically", blocks)
		}
	}
	now = now.Add(24 * time.Hour)
	for range 3 {
		_, _ = f.engine.text(context.Background(), f.engine.Config, launch, false)
	}
	if x.calls != maxTextAttempts {
		t.Fatal("time or repeated dispatch reset the exact source's retry budget", x.calls)
	}
	retained, err := f.engine.State.Journal(context.Background(), last.ID)
	if err != nil || retained.State != "text-failed" || retained.PID != 12345 {
		t.Fatal("source isolation erased the original invocation evidence", retained, err)
	}
}

func TestTextCleanupUncertaintyStillGloballyFencesWithoutAutomaticRetry(t *testing.T) {
	f := sharedAuthorityFixture(t)
	x := &stoppedTextExecutor{Executor: f.executor, err: &process.CleanupError{Err: context.Canceled}}
	f.engine.Executor = x
	launch := Launch{Phase: "message", AgentID: f.me.ID, Message: &domain.Message{ID: "message_uncertain", Revision: 1}}
	journal, err := f.engine.text(context.Background(), f.engine.Config, launch, false)
	if err == nil || journal.State != "uncertain" || x.calls != 1 {
		t.Fatal("unproved native cleanup was mistaken for a settled text call", journal, err)
	}
	for range 2 {
		if _, err := f.engine.reconcile(context.Background(), false); err == nil {
			t.Fatal("genuine native uncertainty was cleared automatically")
		}
	}
	if x.calls != 1 {
		t.Fatal("unproved provider cleanup retried inference")
	}
}
