package work

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

type bootstrapTransport func(*http.Request) (*http.Response, error)

func (transport bootstrapTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func clarificationFixture() Launch {
	task := domain.Task{ID: "task_clarification", SpecGeneration: 2, DiscussionGeneration: 4,
		Waiting: &domain.Waiting{Kind: "clarification", QuestionIDs: []string{"question_one", "question_two"},
			RequestedIDs: []string{"agent_founder"}}}
	return Launch{Phase: "preflight", Task: &task, Discussion: []domain.Entry{
		{ID: "answer_one", TaskID: task.ID, Kind: "comment", ParentID: "question_one", AuthorID: "agent_founder"},
		{ID: "answer_two", TaskID: task.ID, Kind: "comment", ParentID: "question_two", AuthorID: "agent_founder"},
		{ID: "foreign", TaskID: "task_other", Kind: "comment", ParentID: "question_two", AuthorID: "agent_founder"},
		{ID: "wrong_actor", TaskID: task.ID, Kind: "comment", ParentID: "question_two", AuthorID: "agent_other"},
	}}
}

func TestPreparationUsesActualCurrentAnswerReferencesBeforeEffects(t *testing.T) {
	launch := clarificationFixture()
	for _, test := range []struct {
		name string
		ids  []string
		ok   bool
	}{
		{"complete", []string{"answer_one", "answer_two"}, true},
		{"missing", nil, false},
		{"partial", []string{"answer_one"}, false},
		{"question-not-answer", []string{"question_one", "answer_two"}, false},
		{"foreign-task", []string{"answer_one", "foreign"}, false},
		{"foreign-actor", []string{"answer_one", "wrong_actor"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePreparationReply(launch, provider.Reply{Outcome: "clear", AnswerIDs: test.ids})
			if (err == nil) != test.ok || err != nil && domain.AsFault(err).Code != domain.Provider {
				t.Fatal("wrong preparation answer decision", err)
			}
		})
	}
	if !hasAnswers(*launch.Task, launch.Discussion) || hasAnswers(*launch.Task, launch.Discussion[:1]) {
		t.Fatal("controller question matching differs from the prompt's source-bound review")
	}
	data, _ := json.Marshal(launch)
	prompt, err := invocationPrompt(data)
	if err != nil {
		t.Fatal(err)
	}
	_, tail, found := strings.Cut(prompt, "FDN_CURRENT_CLARIFICATION_REVIEW\n")
	if !found {
		t.Fatal("actual answer references were not surfaced in the preparation contract")
	}
	line, _, _ := strings.Cut(tail, "\n")
	var review clarificationContext
	if err := json.Unmarshal([]byte(line), &review); err != nil || !review.Required ||
		!slices.Equal(review.AnswersByQuestion["question_two"], []string{"answer_two"}) || len(review.MissingQuestionIDs) != 0 {
		t.Fatal("prompt invented, omitted or broadened an actual answer", review, err)
	}
	if !strings.HasSuffix(prompt, "\nFDN_CONTEXT_JSON\n"+string(data)) {
		t.Fatal("derived review changed the retained original context")
	}
	launch.Task.Waiting = nil
	if err := validatePreparationReply(launch, provider.Reply{Outcome: "clear"}); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparationReply(launch, provider.Reply{Outcome: "clear", AnswerIDs: []string{"answer_one"}}); err == nil {
		t.Fatal("historical clarification references revived an already resolved question")
	}
	if err := validatePreparationReply(launch, provider.Reply{Outcome: "blocked"}); err == nil {
		t.Fatal("non-actionable blocked response was allowed to wedge global replay")
	}
}

func TestMissingPreparationAnswerRetainsRejectedReplyWithoutPoisoningOtherSources(t *testing.T) {
	f := newPermissionFixture(t)
	ctx := context.Background()
	launch := clarificationFixture()
	launch.Task.ID, launch.Task.ProjectID = f.task.ID, f.project.ID
	for i := range launch.Discussion {
		if launch.Discussion[i].TaskID == "task_clarification" {
			launch.Discussion[i].TaskID = f.task.ID
		}
	}
	launch.Project = &f.project
	reply := provider.Reply{Outcome: "clear", Summary: "Private review with missing answer references"}
	contextData, _ := json.Marshal(launch)
	replyData, _ := json.Marshal(reply)
	journal := local.Journal{ID: "invocation_missing_answers", Kind: "preflight", State: "reply-prepared",
		TaskID: f.task.ID, Context: contextData, Reply: replyData, PromptHash: domain.Hash("pinned prompt"), Provider: "codex"}
	if err := f.state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.applyText(ctx, &journal, launch, reply); err == nil || domain.AsFault(err).Code != domain.Provider {
		t.Fatal("missing answer did not fail before task mutations", err)
	}
	stored, err := f.state.Journal(ctx, journal.ID)
	if err != nil || stored.State != "reply-rejected" || string(stored.Reply) != string(replyData) {
		t.Fatal("original reply was changed or left globally applying", stored, err)
	}
	pending, err := f.state.Journals(ctx, true)
	if err != nil || len(pending) != 0 {
		t.Fatal("known unapplied rejection occupied the global slot", pending, err)
	}
	for range 2 {
		var fenced textResponseFenced
		if err := f.engine.textResponseBlocked(ctx, f.c, launch); !errors.As(err, &fenced) {
			t.Fatal("unchanged review lost its semantic source fence", err)
		}
	}
	other := Launch{Phase: "message", Message: &domain.Message{ID: "message_unrelated"}}
	if err := f.engine.textResponseBlocked(ctx, f.c, other); err != nil {
		t.Fatal("unrelated requested work was fenced by a rejected preflight", err)
	}
	f.reopen(t)
	if err := f.engine.textResponseBlocked(ctx, f.c, launch); err == nil {
		t.Fatal("restart renewed inference authority")
	}
}

func TestAlreadyApplyingPreparationIsNotAutomaticallyAdoptedAsUnapplied(t *testing.T) {
	f := newPermissionFixture(t)
	launch := clarificationFixture()
	launch.Task.ID, launch.Task.ProjectID, launch.Task.DiscussionGeneration = f.task.ID, f.project.ID, f.task.DiscussionGeneration
	f.task.Waiting = launch.Task.Waiting
	launch.Project = &f.project
	f.engine.Client.HTTP.Transport = bootstrapTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == "POST" && request.URL.Path == api.Path("tasks", f.task.ID, "change") {
			return f.response(nil, domain.Fail(domain.Prerequisite, "retained original clarity is not accepted"))
		}
		return f.RoundTrip(request)
	})
	reply := provider.Reply{Outcome: "clear", Summary: "Retained possibly partially applied review"}
	data, _ := json.Marshal(launch)
	raw, _ := json.Marshal(reply)
	journal := local.Journal{ID: "invocation_already_applying", Kind: "preflight", State: "applying",
		Context: data, Reply: raw, TaskID: f.task.ID, Provider: "codex"}
	if err := f.state.SaveJournal(context.Background(), journal); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.applyText(context.Background(), &journal, launch, reply); err == nil {
		t.Fatal("possibly applied decision was treated as successful")
	}
	stored, err := f.state.Journal(context.Background(), journal.ID)
	if err != nil || stored.State != "applying" || string(stored.Reply) != string(raw) {
		t.Fatal("legacy uncertainty was automatically retired or rewritten", stored, err)
	}
}

func TestMaintenanceWakeupUsesServerWindowWithoutChangingCharges(t *testing.T) {
	f := newPermissionFixture(t)
	base := domain.MaintenanceUsage{WindowStart: 3600, ServerObservedAt: 3600,
		Policy: domain.ImprovementPolicy{Enabled: true, WindowSeconds: 3600, SliceSeconds: 300, PerAgentSeconds: 600, FleetSeconds: 1800}}
	for _, test := range []struct {
		name     string
		agent    int
		fleet    int
		observed int64
		occupied bool
		delay    time.Duration
	}{
		{"actor-full", 600, 600, 3600, false, time.Hour},
		{"fleet-full", 0, 1800, 7199, false, time.Second},
		{"paced-share", 300, 300, 5399, false, time.Second},
		{"occupied-upper-bound", 0, 0, 3600, true, 300 * time.Second},
		{"occupied-window-bound", 0, 0, 7190, true, 10 * time.Second},
		{"eligible-now", 0, 0, 3600, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := base
			usage.AgentSeconds, usage.FleetSeconds, usage.ServerObservedAt = test.agent, test.fleet, test.observed
			before, _ := json.Marshal(usage)
			schedule := &workDeadline{}
			ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
			f.engine.scheduleMaintenance(ctx, usage, test.occupied)
			want := time.Time{}
			if test.delay != 0 {
				want = f.engine.Now().Add(test.delay)
			}
			if !schedule.at.Equal(want) {
				t.Fatal("wrong eligible wakeup with a skewed worker clock", schedule.at, want)
			}
			after, _ := json.Marshal(usage)
			if string(before) != string(after) {
				t.Fatal("scheduling refunded or modified a durable reservation")
			}
		})
	}
	schedule := &workDeadline{}
	ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
	base.AgentSeconds = 600
	f.engine.scheduleMaintenance(ctx, base, false)
	if err := f.engine.finishUpdateWork(ctx, updateCheckpoint{}, Status{}, domain.Fail(domain.Prerequisite, "exhausted"), schedule.at); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	checkpoint, err := f.engine.updateCheckpoint(ctx, func(*updateCheckpoint) {})
	if err != nil || checkpoint.WorkDueAt != schedule.at.UnixNano() {
		t.Fatal("restart lost the timed wakeup while no new task/message exists", checkpoint, err)
	}
}

func TestMaintenanceRefusalSchedulesItsOriginalFutureEligibility(t *testing.T) {
	f := newPermissionFixture(t)
	usage := domain.MaintenanceUsage{WindowStart: 3600, ServerObservedAt: 7199, AgentSeconds: 600, FleetSeconds: 600,
		Policy: domain.ImprovementPolicy{Enabled: true, WindowSeconds: 3600, SliceSeconds: 300, PerAgentSeconds: 600, FleetSeconds: 1800}}
	reservations := 0
	f.engine.Client.HTTP.Transport = bootstrapTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/improvement-policy":
			return f.response(usage.Policy, nil)
		case "/v1/maintenance-usage":
			return f.response(usage, nil)
		case "/v1/maintenance-reservations":
			reservations++
			return f.response(nil, domain.Fail(domain.Prerequisite, "durable allowance exhausted"))
		default:
			return f.RoundTrip(request)
		}
	})
	schedule := &workDeadline{}
	ctx := context.WithValue(context.Background(), workDeadlineContext{}, schedule)
	launch := Launch{Phase: "preflight", Project: &domain.Project{ID: domain.FoundationProjectID}, Task: &f.task}
	if err := f.engine.reserveImprovement(ctx, &launch); err == nil || domain.AsFault(err).Code != domain.Prerequisite ||
		!schedule.at.Equal(f.engine.Now().Add(time.Second)) || reservations != 1 || launch.Maintenance != nil {
		t.Fatal("refusal was hidden, repeatedly reserved or left without a future deadline", err, schedule.at, reservations)
	}
}

func TestTerminalImplicitAttentionDefersAndPreservesExplicitFollowup(t *testing.T) {
	for _, status := range []domain.Status{domain.Done, domain.WontDo, domain.CancelledStatus, domain.Todo} {
		t.Run(string(status), func(t *testing.T) {
			f := newPushFixture(t)
			f.task.Status, f.task.ActiveRun, f.task.Revision = status, nil, 7
			if status == domain.Todo {
				f.task.ArchivedAt = 1
			}
			implicit := domain.Entry{ID: "entry_implicit", TaskID: f.task.ID, AuthorID: "agent_founder", Kind: "comment", Generation: 1}
			explicit := domain.Entry{ID: "entry_explicit", TaskID: f.task.ID, AuthorID: "agent_founder", Kind: "comment",
				Body: "Please explain the checked result.", Generation: 1, Mentions: []string{f.me.ID}}
			receipts := []domain.AttentionRequest{}
			f.engine.Client.HTTP.Transport = bootstrapTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/attention":
					return f.response(domain.Page[domain.Attention]{Items: []domain.Attention{
						{EntryID: implicit.ID, TaskID: f.task.ID, Generation: 1}, {EntryID: explicit.ID, TaskID: f.task.ID, Generation: 1},
					}}, nil)
				case api.Path("entries", implicit.ID):
					return f.response(implicit, nil)
				case api.Path("entries", explicit.ID):
					return f.response(explicit, nil)
				case "/v1/improvement-policy":
					return f.response(domain.ImprovementPolicy{}, nil)
				case api.Path("attention", implicit.ID), api.Path("attention", explicit.ID):
					var receipt domain.AttentionRequest
					if err := json.NewDecoder(request.Body).Decode(&receipt); err != nil {
						t.Fatal(err)
					}
					receipts = append(receipts, receipt)
					return f.response(domain.Attention{State: receipt.State, Generation: receipt.Generation}, nil)
				case api.Path("tasks", f.task.ID, "entries"):
					if request.Method == "POST" {
						return f.response(domain.Entry{ID: "entry_reply", TaskID: f.task.ID}, nil)
					}
					return f.response(domain.Page[domain.Entry]{Items: []domain.Entry{implicit, explicit}}, nil)
				}
				return f.RoundTrip(request)
			})
			acted, err := f.engine.attention(context.Background(), f.engine.Config, false)
			if err != nil || !acted || f.executor.invocations.Load() != 1 || len(receipts) != 2 ||
				receipts[0].State != "deferred" || receipts[0].ExpectedRevision != 7 || receipts[1].State != "handled" {
				t.Fatal("obsolete attention invoked a provider or suppressed an explicit followup", acted, err, receipts, f.executor.invocations.Load())
			}
		})
	}
}

func TestTerminalAttentionSourceDriftCannotProduceAFalseReceipt(t *testing.T) {
	for _, kind := range []string{"generation", "task"} {
		t.Run(kind, func(t *testing.T) {
			f := newPermissionFixture(t)
			f.task.Status, f.task.Revision = domain.CancelledStatus, 7
			entry := domain.Entry{ID: "entry_old", TaskID: f.task.ID, Generation: 1}
			if kind == "generation" {
				entry.Generation++
			} else {
				entry.TaskID = "task_foreign"
			}
			f.engine.Client.HTTP.Transport = bootstrapTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/attention":
					return f.response(domain.Page[domain.Attention]{Items: []domain.Attention{{EntryID: entry.ID, TaskID: f.task.ID, Generation: 1}}}, nil)
				case api.Path("entries", entry.ID):
					return f.response(entry, nil)
				}
				return f.RoundTrip(request)
			})
			if _, err := f.engine.attention(context.Background(), f.c, false); err == nil || domain.AsFault(err).Code != domain.Conflict {
				t.Fatal("changed source was silently disposed", err)
			}
		})
	}
}

func TestImprovementSkipsRejectedPreparationWithoutRenewingInference(t *testing.T) {
	f := newPermissionFixture(t)
	f.task.ProjectID, f.project.ID = domain.FoundationProjectID, domain.FoundationProjectID
	f.engine.Client.HTTP.Transport = bootstrapTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/me":
			return f.response(domain.Agent{ID: "agent_target", Active: true, Mode: "ai"}, nil)
		case "/v1/projects":
			return f.response([]domain.Project{}, nil)
		case api.Path("tasks", f.task.ID, "entries"):
			return f.response(domain.Page[domain.Entry]{Items: []domain.Entry{}}, nil)
		}
		return f.RoundTrip(request)
	})
	ctx := context.Background()
	launch, err := f.engine.launch(ctx, "scope", &f.task)
	if err != nil {
		t.Fatal(err)
	}
	journal := local.Journal{ID: "invocation_rejected_improvement", Kind: "scope", State: "reply-rejected"}
	hash := textActionHash(launch)
	journal.TextActionHash = hash
	block := TextResponseBlock{Version: textResponseFormat, ActionHash: hash, JournalID: journal.ID,
		Phase: "scope", TaskID: f.task.ID, State: "blocked", CreatedAt: f.now}
	if err := f.state.SaveTextResponse(ctx, journal, hash, block); err != nil {
		t.Fatal(err)
	}
	acted, err := f.engine.improvementStep(ctx, f.c, false, domain.ImprovementPolicy{Enabled: true}, []domain.Task{f.task})
	if err != nil || acted {
		t.Fatal("a fenced improvement prevented the controller from considering ordinary work", acted, err)
	}
	pending, err := f.state.Journals(ctx, true)
	if err != nil || len(pending) != 0 {
		t.Fatal("checking the rejected source created a provider invocation", pending, err)
	}
}
