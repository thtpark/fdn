package work

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
)

func TestRequestReviewHasExplicitVoluntaryDecisionsAndNoCrossPhaseLeakage(t *testing.T) {
	for _, decision := range []string{"accept", "defer", "decline", "answer"} {
		reply := provider.Reply{Outcome: "responded", RequestDecision: decision, Summary: "Reasoned decision"}
		if err := reply.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := validateTextPhase(Launch{Phase: "request"}, reply); err != nil {
			t.Fatal(err)
		}
		if err := validateTextPhase(Launch{Phase: "message"}, reply); err == nil {
			t.Fatal("request decision leaked into ordinary text phase")
		}
	}
	if err := validateTextPhase(Launch{Phase: "request"}, provider.Reply{Outcome: "responded", Summary: "No explicit decision"}); err == nil {
		t.Fatal("request was handled without a voluntary decision")
	}
}

func TestPreparedRequestReplyReplaysWithoutAnotherModelOrRepeatedResult(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	source := domain.Message{ID: "message_request_answer", SenderID: "agent_founder", RecipientID: f.me.ID,
		Kind: "request", State: "pending", Revision: 1, Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "work"}}}
	original := f.engine.Client.HTTP.Transport
	effects := map[string]domain.Message{}
	var states []string
	lost := true
	f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages/"+source.ID && r.URL.Path != "/v1/messages/"+source.ID+"/request" {
			return original.RoundTrip(r)
		}
		respond := func(value domain.Message) (*http.Response, error) {
			body, _ := json.Marshal(map[string]any{"ok": true, "data": value, "warnings": []string{}})
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Fdn-Api-Version": {strconv.Itoa(domain.APIVersion)}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}
		if r.Method == "GET" {
			return respond(source)
		}
		var request domain.MessageActionReport
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if saved, ok := effects[request.OperationID]; ok {
			return respond(saved)
		}
		if request.ExpectedRevision != source.Revision {
			t.Fatal("replayed decision changed its original revision", request)
		}
		source.Request.State = request.State
		source.Revision++
		copy := source
		state := *source.Request
		copy.Request = &state
		effects[request.OperationID] = copy
		states = append(states, request.State)
		if lost {
			lost = false
			return nil, io.ErrUnexpectedEOF
		}
		return respond(copy)
	})
	launch := Launch{Phase: "request", Message: &source}
	journal := local.Journal{ID: "invocation_request_answer", Kind: "request", State: "reply-prepared"}
	reply := provider.Reply{Outcome: "responded", RequestDecision: "answer", Summary: "The answer is provided; no tool action was performed."}
	if err := f.engine.applyText(ctx, &journal, launch, reply); err == nil {
		t.Fatal("lost decision acknowledgement was hidden")
	}
	if err := f.engine.applyText(ctx, &journal, launch, reply); err != nil {
		t.Fatal("exact prepared decision did not reconcile", err)
	}
	if !reflect.DeepEqual(states, []string{"accepted", "completed"}) || f.executor.invocations.Load() != 0 {
		t.Fatal("reply replay duplicated side effects or inference", states, f.executor.invocations.Load())
	}
}

func TestTaskRequestReconciliationReportsActualProgressAndStoppedResults(t *testing.T) {
	for _, scenario := range []struct {
		name, want string
		change     func(*domain.Task)
	}{
		{"running", "executing", func(task *domain.Task) { task.Status = domain.Running }},
		{"completed", "completed", func(task *domain.Task) {
			task.ActiveRun, task.Status, task.LastRunID = nil, domain.Completed, "run_completed"
			task.ResultGeneration++
			task.Result = &domain.Result{RunID: task.LastRunID, SpecGeneration: task.SpecGeneration}
		}},
		{"failed", "failed", func(task *domain.Task) {
			task.ActiveRun, task.Status = nil, domain.Paused
			task.Waiting = &domain.Waiting{Kind: "retry", Reason: "An actual execution failed."}
		}},
		{"paused", "failed", func(task *domain.Task) { task.ActiveRun, task.Status = nil, domain.Paused }},
		{"blocked", "failed", func(task *domain.Task) {
			task.ActiveRun = nil
			task.Waiting = &domain.Waiting{Kind: "answers", Reason: "A prerequisite is unresolved."}
		}},
		{"cancelled", "failed", func(task *domain.Task) { task.ActiveRun, task.Status = nil, domain.CancelledStatus }},
		{"reassigned", "failed", func(task *domain.Task) { task.AssigneeID = "agent_new_assignee" }},
		{"changed-specification", "failed", func(task *domain.Task) { task.SpecGeneration++ }},
		{"archived", "failed", func(task *domain.Task) { task.ArchivedAt = 1 }},
		{"old-result-after-new-failure", "failed", func(task *domain.Task) {
			task.ActiveRun, task.Status, task.LastRunID = nil, domain.Paused, "run_latest_failed"
			task.ResultGeneration++
			task.Result = &domain.Result{RunID: "run_earlier_completed", SpecGeneration: task.SpecGeneration}
		}},
		{"verification-is-not-execution", "", func(task *domain.Task) { task.ActiveRun.Kind = domain.VerificationRun }},
		{"idle", "", func(task *domain.Task) { task.ActiveRun = nil }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newPushFixture(t)
			ctx := context.Background()
			source := domain.Message{ID: "message_task_request", RecipientID: f.me.ID, Revision: 2,
				Request: &domain.MessageActionState{State: "accepted", SpecGeneration: f.task.SpecGeneration, ResultGeneration: f.task.ResultGeneration,
					Action: domain.MessageAction{Kind: "work", ProjectID: f.project.ID, TaskID: f.task.ID}}}
			scenario.change(&f.task)
			original := f.engine.Client.HTTP.Transport
			reports := 0
			f.engine.Client.HTTP.Transport = pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" && r.URL.Path == api.Path("tasks", f.task.ID) {
					return f.response(f.task, nil)
				}
				if r.URL.Path == api.Path("messages", source.ID, "request") {
					var report domain.MessageActionReport
					if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
						t.Fatal(err)
					}
					if report.State != scenario.want || report.Reason == "" || report.ExpectedRevision != source.Revision {
						t.Fatal("request did not reflect the exact actual task outcome", report)
					}
					reports++
					source.Request.State, source.Revision = report.State, source.Revision+1
					return f.response(source, nil)
				}
				return original.RoundTrip(r)
			})
			handled, err := f.engine.reconcileMessageAction(ctx, source)
			if err != nil || handled != (scenario.want != "") || reports != map[bool]int{true: 1, false: 0}[handled] || f.executor.invocations.Load() != 0 {
				t.Fatal("task result reconciliation repeated inference or omitted the result", handled, reports, err)
			}
		})
	}
}
