package work

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func sharedAuthorityFixture(t *testing.T) *pushFixture {
	t.Helper()
	f := newPushFixture(t)
	f.engine.feedRunning, f.engine.feedOnline = true, true
	return f
}

func awaitAuthorityCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("priority authority cancellation exceeded the isolated one-second harness bound")
	}
}

func TestAuthorityGuardHasInitialValidationAndNoPeriodicRemoteQueries(t *testing.T) {
	f := sharedAuthorityFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if err := f.engine.guardTask(ctx, cancel, f.task, f.task.ActiveRun.ID, false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	taskQueries := f.counts["GET /v1/tasks/task_push"]
	projectQueries := f.counts["GET /v1/projects/project_push"]
	meQueries := f.counts["GET /v1/me"]
	f.mu.Unlock()
	f.engine.invalidateAuthority(domain.Update{Topic: "task", SourceID: "task_unrelated", TaskID: "task_unrelated", ProjectID: f.project.ID})
	time.Sleep(600 * time.Millisecond)
	f.mu.Lock()
	if taskQueries != 1 || projectQueries != 1 || meQueries != 1 ||
		f.counts["GET /v1/tasks/task_push"] != taskQueries || f.counts["GET /v1/projects/project_push"] != projectQueries || f.counts["GET /v1/me"] != meQueries {
		t.Error("active guard polled remote authority or processed another task's invalidation", f.counts)
	}
	f.task.ActiveRun.StopRequested = true
	f.mu.Unlock()
	start := time.Now()
	f.engine.invalidateAuthority(domain.Update{Topic: "run", SourceID: "run_push", TaskID: f.task.ID})
	awaitAuthorityCancellation(t, ctx)
	if domain.AsFault(context.Cause(ctx)).Code != domain.Conflict || time.Since(start) >= time.Second {
		t.Fatal("run stop was not promptly fenced", context.Cause(ctx))
	}
}

func TestExecutionAuthorityUpdateCategoriesCancelWithoutPolling(t *testing.T) {
	for _, item := range []struct {
		name, topic string
		mutate      func(*pushFixture)
	}{
		{"specification", "task", func(f *pushFixture) { f.task.SpecGeneration++ }},
		{"assignee", "task", func(f *pushFixture) { f.task.AssigneeID = "agent_replacement" }},
		{"approval", "task", func(f *pushFixture) { f.task.Readiness = nil }},
		{"run", "run", func(f *pushFixture) { f.task.ActiveRun.ID = "run_replacement" }},
		{"project-epoch", "project", func(f *pushFixture) { f.project.Epoch++ }},
		{"project-archive", "project", func(f *pushFixture) { f.project.Archived = true }},
		{"founder-pause", "agent-mode", func(f *pushFixture) { f.me.Mode = "manual" }},
		{"identity-disabled", "agent", func(f *pushFixture) { f.me.Active = false }},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := sharedAuthorityFixture(t)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if err := f.engine.guardTask(ctx, cancel, f.task, f.task.ActiveRun.ID, false); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			item.mutate(f)
			f.mu.Unlock()
			f.engine.invalidateAuthority(domain.Update{Topic: item.topic, SourceID: f.me.ID, TaskID: f.task.ID, ProjectID: f.project.ID})
			awaitAuthorityCancellation(t, ctx)
		})
	}
}

func TestVerificationAuthorityUpdateCategoriesCancelWithoutPolling(t *testing.T) {
	for _, item := range []struct {
		name, topic string
		mutate      func(*pushFixture)
	}{
		{"result", "task", func(f *pushFixture) { f.task.ResultGeneration++ }},
		{"discussion", "comment", func(f *pushFixture) { f.task.DiscussionGeneration++ }},
		{"worklog", "worklog", func(f *pushFixture) { f.task.DiscussionGeneration++ }},
		{"run-stop", "run", func(f *pushFixture) { f.task.ActiveRun.StopRequested = true }},
		{"project-authority", "project", func(f *pushFixture) { f.project.Delegates = nil }},
		{"mode-revision", "agent-mode", func(f *pushFixture) { f.me.Revision++ }},
		{"no-self-verification", "task", func(f *pushFixture) { f.task.AssigneeID = f.me.ID }},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := sharedAuthorityFixture(t)
			f.task.Status, f.task.AssigneeID = domain.Completed, "agent_author"
			task, project := f.task, f.project
			run := domain.Run{ID: task.ActiveRun.ID, AgentID: f.me.ID, AgentRevision: f.me.Revision}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if err := f.engine.guardVerification(ctx, cancel, Launch{Task: &task, Project: &project}, run); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			item.mutate(f)
			f.mu.Unlock()
			f.engine.invalidateAuthority(domain.Update{Topic: item.topic, SourceID: f.me.ID, TaskID: f.task.ID, ProjectID: f.project.ID})
			awaitAuthorityCancellation(t, ctx)
		})
	}
}

func TestAuthorityDisconnectStaysConservativeAcrossFastReconnect(t *testing.T) {
	f := sharedAuthorityFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if err := f.engine.guardTask(ctx, cancel, f.task, f.task.ActiveRun.ID, false); err != nil {
		t.Fatal(err)
	}
	f.engine.setFeedOnline(false)
	f.engine.setFeedOnline(true)
	awaitAuthorityCancellation(t, ctx)
	if domain.AsFault(context.Cause(ctx)).Code != domain.Unavailable {
		t.Fatal("a reconnect erased an interrupted authority boundary", context.Cause(ctx))
	}
	f.engine.setFeedOnline(false)
	next, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	if err := f.engine.guardTask(next, stop, f.task, f.task.ActiveRun.ID, false); err == nil {
		t.Fatal("disconnected authority admitted a new provider invocation")
	}
}

func TestManualAuthoritySubscriptionClosesValidationRaceAndCancelsOnDisconnect(t *testing.T) {
	f := newPushFixture(t)
	f.firstMe = func() {
		f.mu.Lock()
		f.task.ActiveRun.StopRequested = true
		f.mu.Unlock()
		f.publish(domain.Update{Topic: "run", SourceID: "run_push", TaskID: f.task.ID})
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	if err := f.engine.guardTask(ctx, cancel, f.task, f.task.ActiveRun.ID, false); err != nil {
		t.Fatal(err)
	}
	awaitAuthorityCancellation(t, ctx)
	if domain.AsFault(context.Cause(ctx)).Code != domain.Conflict {
		t.Fatal("manual run missed an update racing its initial validation", context.Cause(ctx))
	}

	f.mu.Lock()
	f.task.ActiveRun.StopRequested = false
	f.mu.Unlock()
	next, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	if err := f.engine.guardTask(next, stop, f.task, f.task.ActiveRun.ID, false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.disconnect = true
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
	awaitAuthorityCancellation(t, next)
	if domain.AsFault(context.Cause(next)).Code != domain.Unavailable {
		t.Fatal("manual scoped subscription continued after transport loss", context.Cause(next))
	}
}

func TestPushFounderPauseCancelsBusyTextWithoutReadOrReply(t *testing.T) {
	f := newPushFixture(t)
	f.executor.entered = make(chan struct{}, 1)
	f.executor.release = make(chan struct{})
	f.messages["message_busy"] = domain.Message{ID: "message_busy", SenderID: "agent_founder", RecipientID: f.me.ID, Kind: "direct",
		State: "pending", Revision: 1, Urgent: true, ResponseRequest: true}
	stop := startPushService(t, f.engine, nil)
	select {
	case <-f.executor.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic provider was not busy")
	}
	started := time.Now()
	f.mu.Lock()
	f.me.Mode, f.me.ModeRequestedBy = "manual", "agent_founder"
	f.me.Revision++
	f.mu.Unlock()
	f.publish(domain.Update{Topic: "agent-mode", SourceID: f.me.ID, Urgent: true})
	eventuallyPush(t, func() bool {
		journals, err := f.engine.State.Journals(context.Background(), true)
		return err == nil && len(journals) == 1 && journals[0].State == "uncertain"
	})
	if time.Since(started) >= time.Second {
		t.Fatal("priority pause cancellation exceeded the isolated one-second harness bound")
	}
	stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.executor.invocations.Load() != 1 || f.readEffects != 0 || len(f.results) != 0 || f.messages["message_busy"].State != "pending" {
		t.Fatal("priority cancellation repeated inference or fabricated a reply/read receipt")
	}
}

func TestPendingRetryAndResearchDeadlinesDoNotRequireDiscoveryPolling(t *testing.T) {
	f := newPushFixture(t)
	schedule := &workDeadline{}
	ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
	f.task.Waiting = &domain.Waiting{Kind: "retry"}
	f.task.NextAttemptAt = time.Now().Add(time.Hour).Unix()
	acted, err := f.engine.prepare(ctx, f.engine.Config, f.task, false)
	if acted || err == nil || !schedule.at.Equal(time.Unix(f.task.NextAttemptAt, 0)) {
		t.Fatal("retry backoff lost its exact due wake", schedule.at, acted, err)
	}
	schedule.at = time.Time{}
	last := time.Now().Unix()
	if err := f.engine.State.Put(ctx, "last-research", last); err != nil {
		t.Fatal(err)
	}
	backlog := domain.Task{Status: domain.Backlogged}
	acted, err = f.engine.research(ctx, f.engine.Config, []domain.Task{backlog}, false)
	if acted || err != nil || !schedule.at.Equal(time.Unix(last, 0).Add(time.Duration(f.engine.Config.ResearchIntervalHours)*time.Hour)) {
		t.Fatal("known backlog research lost its due wake", schedule.at, acted, err)
	}
	schedule.at = time.Time{}
	if _, err := f.engine.research(ctx, f.engine.Config, nil, false); err != nil || !schedule.at.IsZero() {
		t.Fatal("truly idle worker scheduled a business-discovery timer", schedule.at, err)
	}
}

func TestBusyLocalWorkSlotSchedulesOnlyPendingRetry(t *testing.T) {
	f := newPushFixture(t)
	slot, err := secure.Acquire(filepath.Join(f.engine.State.Root, "work.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Close()
	schedule := &workDeadline{}
	ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
	start := time.Now()
	if _, err := f.engine.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Conflict ||
		schedule.at.Before(start.Add(time.Second)) || schedule.at.After(time.Now().Add(time.Second)) {
		t.Fatal("one-slot contention lost its actual local retry deadline", schedule.at, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.counts) != 0 || f.executor.invocations.Load() != 0 {
		t.Fatal("busy slot performed remote business discovery or inference", f.counts)
	}
}
