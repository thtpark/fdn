package upgrade

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeMessageRequestIsDecidedExecutedAndReportedByTarget(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	source := domain.Message{ID: "message_upgrade", RecipientID: native.Policy.TargetID, Revision: 1,
		Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit}}}
	var states []string
	native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if req.Report == nil {
			items := []domain.Message{}
			if source.Request.State != "completed" {
				items = append(items, source)
			}
			return domain.MessageWorkerReply{Messages: items}, nil
		}
		if req.MessageID != source.ID || req.Report.ExpectedRevision != source.Revision || req.Report.Reason == "" {
			t.Fatal("native result omitted exact request/revision/reason", req)
		}
		source.Request.State = req.Report.State
		source.Revision++
		states = append(states, req.Report.State)
		updated := source
		return domain.MessageWorkerReply{Message: &updated}, nil
	}
	handled, err := native.MessageStep(context.Background())
	if err != nil || !handled || !reflect.DeepEqual(states, []string{"accepted", "executing", "completed"}) {
		t.Fatal("target did not own its request lifecycle", states, err)
	}
	count := len(recorder.calls)
	handled, err = native.MessageStep(context.Background())
	if err != nil || handled || len(recorder.calls) != count {
		t.Fatal("completed request repeated native effects", handled, err)
	}
}

func TestNativeMessageTargetCanDeclineRevokedAuthorityWithoutEffects(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	source := domain.Message{ID: "message_decline", RecipientID: native.Policy.TargetID, Revision: 1,
		Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit}}}
	declined := false
	native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if req.Report == nil {
			return domain.MessageWorkerReply{Messages: []domain.Message{source}}, nil
		}
		if req.Report.State == "accepted" {
			return domain.MessageWorkerReply{}, domain.Fail(domain.Denied, "issuer lost current authority")
		}
		if req.Report.State != "declined" || req.Report.Reason == "" {
			t.Fatal("revocation did not produce an explicit target reason", req)
		}
		declined = true
		source.Request.State = "declined"
		source.Revision++
		return domain.MessageWorkerReply{Message: &source}, nil
	}
	handled, err := native.MessageStep(context.Background())
	if err != nil || !handled || !declined {
		t.Fatal("target refusal failed", handled, declined, err)
	}
	for _, spec := range recorder.calls {
		if strings.Contains(strings.Join(spec.Args, " "), "stop ") || spec.Path == "/bin/sh" {
			t.Fatal("request denial still mutated native services")
		}
	}

}

func TestNativeMessageLostDecisionAndResultAcknowledgementsReplayWithoutEffects(t *testing.T) {
	for _, lostState := range []string{"accepted", "executing", "completed"} {
		t.Run(lostState, func(t *testing.T) {
			native, recorder, signer, key := nativeFixture(t)
			permit := preparePermit(t, native, recorder, signer, key)
			source := domain.Message{ID: "message_lost_ack", RecipientID: native.Policy.TargetID, Revision: 1,
				Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit}}}
			receipts := map[string]domain.Message{}
			proposals := map[string]domain.MessageActionReport{}
			var states []string
			lost := true
			native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
				if req.Report == nil {
					if !lost {
						t.Fatal("pending result delivery queried a new inbox instead of replaying the exact operation")
					}
					return domain.MessageWorkerReply{Messages: []domain.Message{source}}, nil
				}
				if saved, ok := receipts[req.Report.OperationID]; ok {
					if !reflect.DeepEqual(proposals[req.Report.OperationID], *req.Report) {
						t.Fatal("uncertain native decision/result changed on replay", req.Report)
					}
					return domain.MessageWorkerReply{Message: &saved}, nil
				}
				if req.Report.ExpectedRevision != source.Revision {
					t.Fatal("native result omitted the exact original revision", req)
				}
				source.Request.State = req.Report.State
				source.Revision++
				saved := source
				state := *source.Request
				saved.Request = &state
				receipts[req.Report.OperationID], proposals[req.Report.OperationID] = saved, *req.Report
				states = append(states, req.Report.State)
				if req.Report.State == lostState && lost {
					lost = false
					return domain.MessageWorkerReply{}, io.ErrUnexpectedEOF
				}
				return domain.MessageWorkerReply{Message: &saved}, nil
			}
			before := len(recorder.calls)
			if handled, err := native.MessageStep(context.Background()); !handled || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("lost native acknowledgement was hidden", handled, err)
			}
			if lostState != "completed" && len(recorder.calls) != before {
				t.Fatal("uncertain admission performed native effects")
			}
			var retained nativeMessageProgress
			path := filepath.Join(native.Policy.StateDir, "message-inflight.json")
			if err := config.Read(path, &retained); err != nil || retained.PendingReport == nil || retained.PendingReport.State != lostState {
				t.Fatal("uncertain report was not retained before sending", retained, err)
			}
			after := len(recorder.calls)
			handled, err := native.MessageStep(context.Background())
			if !handled || (err != nil && lostState != "executing") || len(recorder.calls) != after {
				t.Fatal("reconciliation repeated effects or lost the outcome", handled, err)
			}
			want := map[string][]string{
				"accepted":  {"accepted", "failed"},
				"executing": {"accepted", "executing", "failed"},
				"completed": {"accepted", "executing", "completed"},
			}[lostState]
			if !reflect.DeepEqual(states, want) {
				t.Fatal("uncertainty was converted to refusal, duplicate result or completion", states, want)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("acknowledged terminal result retained a blocking report", err)
			}
		})
	}
}

func TestNativeMessageDependencyFailureDefersWithoutNativeEffects(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	source := domain.Message{ID: "message_dependency", RecipientID: native.Policy.TargetID, Revision: 1,
		Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit, Dependencies: []string{"message_parent"}}}}
	native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if req.Report == nil {
			return domain.MessageWorkerReply{Messages: []domain.Message{source}}, nil
		}
		if req.Report.State == "accepted" {
			return domain.MessageWorkerReply{}, domain.Fail(domain.Prerequisite, "an earlier requested maintenance result is not complete")
		}
		if req.Report.State != "deferred" || req.Report.Reason == "" {
			t.Fatal("failed dependency did not get a target-owned defer reply", req)
		}
		source.Request.State, source.Revision = "deferred", source.Revision+1
		return domain.MessageWorkerReply{Message: &source}, nil
	}
	before := len(recorder.calls)
	if handled, err := native.MessageStep(context.Background()); err != nil || !handled || len(recorder.calls) != before {
		t.Fatal("blocked rollout mutated native state or remained silent", handled, err)
	}
}

func TestNativeMessageFailedInstallRetainsPendingResultUntilMailboxReturns(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	recorder.failApply = true
	source := domain.Message{ID: "message_failure_delivery", RecipientID: native.Policy.TargetID, Revision: 1,
		Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit}}}
	unavailable := true
	var pending domain.MessageActionReport
	native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if req.Report == nil {
			if !unavailable {
				t.Fatal("pending failure replay queried a new inbox")
			}
			return domain.MessageWorkerReply{Messages: []domain.Message{source}}, nil
		}
		if req.Report.State == "failed" {
			if unavailable {
				pending, unavailable = *req.Report, false
				return domain.MessageWorkerReply{}, domain.Fail(domain.Unavailable, "identity transport is stopped")
			}
			if !reflect.DeepEqual(pending, *req.Report) {
				t.Fatal("failed installation result changed while delivery was pending")
			}
		}
		source.Request.State, source.Revision = req.Report.State, source.Revision+1
		return domain.MessageWorkerReply{Message: &source}, nil
	}
	if handled, err := native.MessageStep(context.Background()); !handled || err == nil {
		t.Fatal("failed installation or blocked failure delivery was hidden", handled, err)
	}
	var retained nativeMessageProgress
	path := filepath.Join(native.Policy.StateDir, "message-inflight.json")
	if err := config.Read(path, &retained); err != nil || retained.PendingReport == nil || !reflect.DeepEqual(*retained.PendingReport, pending) {
		t.Fatal("failure result was not durable while identity transport was unavailable", retained, err)
	}
	before := len(recorder.calls)
	if handled, err := native.MessageStep(context.Background()); err != nil || !handled || len(recorder.calls) != before {
		t.Fatal("failure delivery retried native installation", handled, err)
	}
	var job nativeJob
	if err := config.Read(filepath.Join(native.Policy.StateDir, permit.Proposal.Challenge.ID, "job.json"), &job); err != nil || job.State == "verified" {
		t.Fatal("uncertain native evidence was erased or converted to success", job.State, err)
	}
}

func TestNativeMessageMissingAcknowledgementIsNotSuccess(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	source := domain.Message{ID: "message_missing_ack", RecipientID: native.Policy.TargetID, Revision: 1,
		Request: &domain.MessageActionState{State: "queued", Action: domain.MessageAction{Kind: "upgrade", Upgrade: &permit}}}
	native.Mailbox = func(_ context.Context, req domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if req.Report == nil {
			return domain.MessageWorkerReply{Messages: []domain.Message{source}}, nil
		}
		return domain.MessageWorkerReply{}, nil
	}
	before := len(recorder.calls)
	if handled, err := native.MessageStep(context.Background()); !handled || domain.AsFault(err).Code != domain.Unavailable || len(recorder.calls) != before {
		t.Fatal("empty acknowledgement was treated as admission", handled, err)
	}
	var retained nativeMessageProgress
	if err := config.Read(filepath.Join(native.Policy.StateDir, "message-inflight.json"), &retained); err != nil || retained.PendingReport == nil {
		t.Fatal("missing acknowledgement discarded the exact decision", err)
	}
}
