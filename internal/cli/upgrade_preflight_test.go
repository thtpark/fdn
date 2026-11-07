package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/upgrade"
)

type upgradePreflightFixture struct {
	pending   bool
	malformed bool
	records   map[string]domain.UpgradePreflight
	posts     []domain.UpgradePreflightRequest
}

func (f *upgradePreflightFixture) complete(record domain.UpgradePreflight) domain.UpgradePreflight {
	challenge := &domain.UpgradeChallenge{
		InstanceID: record.Request.InstanceID, TargetID: record.Request.TargetID,
		Role: record.Request.Role, Generation: record.TargetGeneration, CurrentVersion: "0.3.2",
		CurrentCommit: strings.Repeat("a", 40), PolicySHA256: strings.Repeat("e", 64),
	}
	reply := domain.UpgradeNativeReply{State: "observed", Version: "0.3.2", Commit: strings.Repeat("a", 40),
		Platform: record.Request.Platform, Challenge: challenge}
	switch record.Request.Native.Action {
	case "challenge":
		reply.State, reply.Version, reply.Commit = "planned", "", ""
		challenge.ID, challenge.Nonce, challenge.ExpiresAt = record.Request.Native.ID, domain.NewToken(), time.Now().Unix()+900
	case "status":
		reply = domain.UpgradeNativeReply{State: "verified", Platform: record.Request.Platform,
			Version: "0.3.3", Commit: strings.Repeat("b", 40), BackupSHA256: strings.Repeat("f", 64)}
	}
	if f.malformed && reply.Challenge != nil {
		reply.Challenge.TargetID = "agent_other"
	}
	record.Result, record.State, record.Revision, record.CompletedAt = &reply, "completed", 2, time.Now().Unix()
	return record
}

func minimalUpgradeTargets(t *testing.T, path string) {
	t.Helper()
	var targets upgrade.Targets
	if err := config.Read(path, &targets); err != nil {
		t.Fatal(err)
	}
	for i, target := range targets.Targets {
		targets.Targets[i] = upgrade.Target{Role: target.Role, TargetID: target.TargetID, Platform: target.Platform}
	}
	if err := config.Write(path, targets); err != nil {
		t.Fatal(err)
	}
}

func TestFounderUpgradePreflightPlansAppliesAndReadsStatusWithoutOperatorSSH(t *testing.T) {
	control := &upgradePreflightFixture{}
	a, r, catalog, targets := upgradeCLIFixture(t, control)
	minimalUpgradeTargets(t, targets)
	output := filepath.Join(filepath.Dir(targets), "foundation-plan.json")
	operator := &upgradeOperatorFixture{}
	if _, err := a.upgradeWith(context.Background(), r, []string{"plan", "--input", targets, "--output", output}, catalog, operator); err != nil {
		t.Fatal(err)
	}
	var plan upgrade.Plan
	if err := config.Read(output, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Version != 2 || plan.State != "planned" || plan.OperationID != a.operation ||
		plan.BackupRecipient == "" || !domain.ValidHash(plan.TargetsSHA256) || len(operator.calls) != 0 ||
		len(control.posts) != 2 || plan.Items[0].Target.Role != "foundation" {
		t.Fatal("Foundation preflight lost source/route/recovery evidence or invoked operator SSH", plan.State, operator.calls)
	}
	for _, item := range plan.Items {
		if !item.Target.FoundationRoute() || item.Target.Helper != "" || item.Target.PolicyFile != "" ||
			item.Proposal.Challenge.ID == "" || item.Proposal.BackupRecipient != plan.BackupRecipient {
			t.Fatal("minimal native target required operator credentials or lost exact proof")
		}
	}
	if _, err := a.upgradeWith(context.Background(), r, []string{"apply", "--input", output}, catalog, operator); err != nil {
		t.Fatal(err)
	}
	result, err := a.upgradeWith(context.Background(), r, []string{"status", "--input", output}, catalog, operator)
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	observations := value["native_results"].([]domain.UpgradeNativeReply)
	if len(observations) != 2 || len(operator.calls) != 0 || observations[0].State != "verified" {
		t.Fatal("Foundation-routed status used SSH or lost target observations", operator.calls, observations)
	}
	for _, request := range control.posts {
		if request.Native.Action != "challenge" && request.Native.Action != "status" || request.Native.Permit != nil {
			t.Fatal("preflight transport sent an installation capability")
		}
	}
}

func TestFounderUpgradePreflightPendingPlanResumesExactOperationAndBackupKey(t *testing.T) {
	control := &upgradePreflightFixture{pending: true}
	a, r, catalog, targets := upgradeCLIFixture(t, control)
	minimalUpgradeTargets(t, targets)
	output := filepath.Join(filepath.Dir(targets), "pending-plan.json")
	operator := &upgradeOperatorFixture{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, err := a.upgradeWith(ctx, r, []string{"plan", "--input", targets, "--output", output}, catalog, operator)
	cancel()
	if domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("pending preflight was reported as ready or unjournaled", err)
	}
	var before upgrade.Plan
	if err := config.Read(output, &before); err != nil || before.State != "planning" || len(control.posts) != 1 {
		t.Fatal("uncertain native preflight did not retain its exact planning state", before.State, err)
	}
	if before.Items[0].PreflightID == "" {
		t.Fatal("pending native preflight lost its inspectable server reference")
	}
	status, err := a.upgradeWith(context.Background(), r, []string{"status", "--input", output}, catalog, operator)
	if err != nil || len(control.posts) != 1 {
		t.Fatal("status dispatched another native action instead of reading the retained preflight", err)
	}
	inspection := status.(map[string]any)["preflight_results"].([]domain.UpgradePreflight)
	if len(inspection) != 1 || inspection[0].State != "queued" || inspection[0].ID != before.Items[0].PreflightID {
		t.Fatal("status did not distinguish queued preflight from actual installation", inspection)
	}
	key, err := os.ReadFile(before.RecoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	original := a.operation
	a.operation = domain.NewID("op")
	if _, err := a.upgradeWith(context.Background(), r, []string{"plan", "--input", targets, "--output", output}, catalog, operator); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("new operation adopted uncertain preflight evidence", err)
	}
	a.operation, control.pending = original, false
	if _, err := a.upgradeWith(context.Background(), r, []string{"plan", "--input", targets, "--output", output}, catalog, operator); err != nil {
		t.Fatal(err)
	}
	var after upgrade.Plan
	if err := config.Read(output, &after); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(after.RecoveryKey)
	if err != nil || string(key) != string(retained) || before.ID != after.ID ||
		before.BackupRecipient != after.BackupRecipient || after.State != "planned" ||
		control.posts[0].Native.ID != control.posts[1].Native.ID || control.posts[0].OperationID != control.posts[1].OperationID ||
		len(operator.calls) != 0 {
		t.Fatal("resumed preflight rotated its backup identity/native operation or used SSH", err)
	}
}

func TestFounderUpgradePreflightInventoryNeverDefaultsQueuedOrForeignProofToHealthy(t *testing.T) {
	for _, pending := range []bool{false, true} {
		control := &upgradePreflightFixture{pending: pending, malformed: !pending}
		a, r, catalog, targets := upgradeCLIFixture(t, control)
		minimalUpgradeTargets(t, targets)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		value, err := a.upgradeWith(ctx, r, []string{"check", "--input", targets, "--platform", "linux-amd64"}, catalog, &upgradeOperatorFixture{})
		cancel()
		if err == nil || value != nil {
			t.Fatal("queued or foreign preflight became an up-to-date inventory result", err)
		}
		expected := domain.Denied
		if pending {
			expected = domain.Unavailable
		}
		if domain.AsFault(err).Code != expected {
			t.Fatal("native inventory hid its actual proof failure", err)
		}
	}
}

func TestFounderUpgradePreflightExplicitRouteDoesNotFallBackToOperator(t *testing.T) {
	control := &upgradePreflightFixture{}
	a, r, catalog, targets := upgradeCLIFixture(t, control)
	operator := &upgradeOperatorFixture{fail: "foundation:inspect"}
	if _, err := a.upgradeWith(context.Background(), r, []string{
		"check", "--input", targets, "--platform", "linux-amd64", "--via-foundation",
	}, catalog, operator); err != nil || len(operator.calls) != 0 || len(control.posts) != 2 {
		t.Fatal("explicit Foundation route still contacted operator SSH", operator.calls, err)
	}
}

func TestNativeUpgradePreflightWorkerAdapterUsesOnlyItsTypedInboxAndResult(t *testing.T) {
	record := domain.UpgradePreflight{ID: "preflight_adapter", State: "queued", Revision: 1}
	_, file, _ := operationCLIFixture(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/upgrades/preflight-requests":
			if request.Method != "GET" || request.ContentLength > 0 {
				t.Error("native inbox accepted a body or wrong method")
			}
			diagnosticsEnvelope(t, w, []domain.UpgradePreflight{record}, nil)
		case "/v1/upgrades/preflight/preflight_adapter/result":
			var report domain.UpgradePreflightReport
			if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
				t.Error(err)
			}
			if report.ID != record.ID || report.ExpectedRevision != record.Revision || report.OperationID == "" {
				t.Error("native result lost its exact request/operation/revision")
			}
			record.State, record.Revision, record.Result = "failed", 2, &report.Result
			diagnosticsEnvelope(t, w, record, nil)
		default:
			t.Error("native preflight adapter escaped its narrow API", request.URL.Path)
		}
	}, false)
	a := &app{configFile: file, operation: domain.NewID("op"), diagnostic: io.Discard}
	r, err := a.load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	for _, command := range []domain.MessageWorkerCommand{
		{Preflight: true},
		{PreflightReport: &domain.UpgradePreflightReport{
			Command: domain.Command{OperationID: "preflight_adapter.result", ExpectedRevision: 1}, ID: record.ID,
			Result: domain.UpgradeNativeReply{State: "failed", Error: domain.AsFault(domain.Fail(domain.Unavailable, upgrade.PreflightFailureMessage))},
		}},
	} {
		raw, err := message.MarshalPayload(command)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		a.in, a.out = bytes.NewReader(raw), &output
		if _, err := a.messageWorker(context.Background(), r, nil); err != nil {
			t.Fatal(err)
		}
		var reply domain.MessageWorkerReply
		if err := message.UnmarshalPayload(output.Bytes(), &reply); err != nil {
			t.Fatal("native adapter emitted captured JSON instead of its bounded protobuf", err)
		}
		if command.Preflight && (len(reply.Preflights) != 1 || reply.Preflights[0].ID != record.ID) ||
			command.PreflightReport != nil && (reply.Preflight == nil || reply.Preflight.State != "failed") {
			t.Fatal("native adapter changed its typed control response")
		}
	}
}
