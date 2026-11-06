package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

func nativePreflightFixture(t *testing.T, action string) (*Native, *nativeRecorder, *domain.UpgradePreflight) {
	t.Helper()
	native, recorder, _, _ := nativeFixture(t)
	record := &domain.UpgradePreflight{
		ID: "preflight_fixture", FounderID: "agent_founder", FounderBindingID: "agent_founder",
		FounderGeneration: 1, TargetGeneration: recorder.generation, State: "queued", Revision: 1,
		CreatedAt: native.Now().Unix(), ExpiresAt: native.Now().Unix() + 900,
		Request: domain.UpgradePreflightRequest{
			Command: domain.Command{OperationID: "request_fixture"}, InstanceID: native.Policy.InstanceID,
			TargetID: native.Policy.TargetID, Role: native.Policy.Role, Platform: runtime.GOOS + "-" + runtime.GOARCH,
			Native: domain.UpgradeNativeRequest{Action: action, ID: "native_preflight_fixture"},
		},
	}
	native.Lease = func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		t.Fatal("read-only preflight requested installation start authority")
		return domain.UpgradeLease{}, nil
	}
	native.Mailbox = func(_ context.Context, request domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if request.Preflight {
			items := []domain.UpgradePreflight{}
			if record.State == "queued" {
				items = append(items, *record)
			}
			return domain.MessageWorkerReply{Preflights: items}, nil
		}
		if request.PreflightReport == nil || request.PreflightReport.ID != record.ID {
			t.Fatal("preflight used an ordinary message or another request", request)
		}
		record.State = "completed"
		if request.PreflightReport.Result.Error != nil {
			record.State = "failed"
		}
		record.Revision = request.PreflightReport.ExpectedRevision + 1
		record.Result = &request.PreflightReport.Result
		record.CompletedAt = native.Now().Unix()
		return domain.MessageWorkerReply{Preflight: record}, nil
	}
	return native, recorder, record
}

func TestNativeUpgradePreflightUsesOnlyTheFixedTargetAndNeverStopsOrInstalls(t *testing.T) {
	for _, action := range []string{"inspect", "challenge"} {
		t.Run(action, func(t *testing.T) {
			native, recorder, record := nativePreflightFixture(t, action)
			before, err := os.ReadFile(native.Policy.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			handled, err := native.PreflightStep(context.Background())
			if err != nil || !handled || record.State != "completed" || record.Result == nil ||
				record.Result.Platform != record.Request.Platform {
				t.Fatal("target did not return its actual native preflight", record, err)
			}
			if err := ValidatePreflightResult(*record, *record.Result, native.Now().Unix()); err != nil {
				t.Fatal(err)
			}
			for _, spec := range recorder.calls {
				if spec.Path == "/bin/sh" || spec.Path == "/usr/bin/tar" ||
					strings.Contains(strings.Join(spec.Args, " "), "stop ") || strings.Contains(spec.Path, "launchctl") {
					t.Fatal("read-only preflight mutated services or installation")
				}
			}
			after, err := os.ReadFile(native.Policy.ConfigFile)
			if err != nil || string(before) != string(after) {
				t.Fatal("preflight changed worker configuration or mode", err)
			}
			calls := len(recorder.calls)
			if handled, err := native.PreflightStep(context.Background()); err != nil || handled || len(recorder.calls) != calls {
				t.Fatal("completed preflight repeated a native probe", err)
			}
		})
	}
}

func TestNativeUpgradePreflightRetainsAndReplaysTheExactLostObservation(t *testing.T) {
	native, recorder, record := nativePreflightFixture(t, "challenge")
	mailbox := native.Mailbox
	lost := true
	var pending domain.UpgradePreflightReport
	native.Mailbox = func(ctx context.Context, request domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if request.PreflightReport != nil {
			if lost {
				pending = *request.PreflightReport
				lost = false
				if _, err := mailbox(ctx, request); err != nil {
					return domain.MessageWorkerReply{}, err
				}
				return domain.MessageWorkerReply{}, domain.Fail(domain.Unavailable, "fixture lost acknowledgement")
			}
			if !reflect.DeepEqual(*request.PreflightReport, pending) {
				t.Fatal("preflight retry changed its exact durable observation")
			}
			return domain.MessageWorkerReply{Preflight: record}, nil
		}
		return mailbox(ctx, request)
	}
	if handled, err := native.PreflightStep(context.Background()); !handled || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("lost acknowledgement was reported as success", handled, err)
	}
	calls := len(recorder.calls)
	nonce := pending.Result.Challenge.Nonce
	if handled, err := native.PreflightStep(context.Background()); err != nil || !handled ||
		len(recorder.calls) != calls || record.Result.Challenge.Nonce != nonce {
		t.Fatal("lost observation acknowledgement repeated native work or rotated its challenge", err)
	}
	if _, err := os.Stat(filepath.Join(native.Policy.StateDir, "preflight-inflight.json")); !os.IsNotExist(err) {
		t.Fatal("acknowledged preflight retained an active delivery fence", err)
	}
}

func TestNativeUpgradePreflightRefusesExpiryRevocationAndWrongPolicyWithoutProbing(t *testing.T) {
	for _, problem := range []string{"expired", "revoked", "platform", "target"} {
		t.Run(problem, func(t *testing.T) {
			native, recorder, record := nativePreflightFixture(t, "inspect")
			switch problem {
			case "expired":
				record.ExpiresAt = native.Now().Unix()
				record.CreatedAt = record.ExpiresAt - domain.UpgradePreflightTTL
			case "revoked":
				record.Problem = domain.AsFault(domain.Fail(domain.Denied, "issuer changed"))
			case "platform":
				record.Request.Platform = "linux-amd64"
				if runtime.GOOS+"-"+runtime.GOARCH == record.Request.Platform {
					record.Request.Platform = "darwin-arm64"
				}
			case "target":
				record.Request.TargetID = "agent_other"
			}
			handled, err := native.PreflightStep(context.Background())
			if problem == "target" {
				if !handled || domain.AsFault(err).Code != domain.Denied {
					t.Fatal("another target's native policy was adopted", err)
				}
			} else if err != nil || !handled || record.State != "failed" || record.Result.Error == nil {
				t.Fatal("invalid preflight became a healthy default", record, err)
			}
			if len(recorder.calls) != 0 {
				t.Fatal("refused preflight still ran a native version or identity probe")
			}
		})
	}
}

func TestNativeUpgradePreflightRejectsMutatingMailboxAndTracksActivatedSource(t *testing.T) {
	for _, request := range []domain.MessageWorkerCommand{
		{Preflight: true, MessageID: "message_other"},
		{Preflight: true, Report: &domain.MessageActionReport{}},
		{Preflight: true, PreflightReport: &domain.UpgradePreflightReport{}},
		{PreflightReport: &domain.UpgradePreflightReport{ID: "../other"}},
	} {
		if err := ValidateMailboxCommand(request); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("preflight mailbox accepted ambiguous authority", err)
		}
	}
	native, recorder, _ := nativePreflightFixture(t, "inspect")
	changed, err := native.ProcessSourceChanged(context.Background(), "0.3.2", strings.Repeat("b", 40))
	if err != nil || changed {
		t.Fatal("unchanged receiver source requested a restart", err)
	}
	recorder.newSource = true
	changed, err = native.ProcessSourceChanged(context.Background(), "0.3.2", strings.Repeat("b", 40))
	if err != nil || !changed {
		t.Fatal("activated receiver source did not request supervisor handoff", err)
	}
	native.Runner = failingPreflightRunner{}
	if _, err := native.ProcessSourceChanged(context.Background(), "0.3.2", strings.Repeat("b", 40)); err == nil {
		t.Fatal("unobserved installed source became an unchanged-success default")
	}
}

func TestNativeUpgradeControlPrioritizesAnExactPendingMaintenanceReport(t *testing.T) {
	native, recorder, _ := nativePreflightFixture(t, "inspect")
	permit := &domain.UpgradePermit{Proposal: domain.UpgradeProposal{Challenge: domain.UpgradeChallenge{
		TargetID: native.Policy.TargetID, Role: native.Policy.Role, ID: "native_original",
	}}}
	progress := nativeMessageProgress{Message: domain.Message{
		ID: "message_original", RecipientID: native.Policy.TargetID, Revision: 3,
		Request: &domain.MessageActionState{State: "executing", Action: domain.MessageAction{Kind: "upgrade", Upgrade: permit}},
	}, PendingReport: &domain.MessageActionReport{
		Command: domain.Command{OperationID: "original.failed", ExpectedRevision: 3}, State: "failed",
		Reason: "Retain the original interrupted installation for operator inspection.",
	}}
	if err := config.Write(filepath.Join(native.Policy.StateDir, "message-inflight.json"), progress); err != nil {
		t.Fatal(err)
	}
	native.Mailbox = func(_ context.Context, request domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if request.Preflight || request.PreflightReport != nil || request.Report == nil {
			t.Fatal("new native preflight overtook an original maintenance result")
		}
		updated := progress.Message
		updated.Revision++
		updated.Request.State = request.Report.State
		return domain.MessageWorkerReply{Message: &updated}, nil
	}
	if handled, err := native.ControlStep(context.Background()); err != nil || !handled || len(recorder.calls) != 0 {
		t.Fatal("pending result was not reconciled before new native work", handled, err)
	}
}

func TestNativeUpgradePreflightRejectsForgedAuthorityAndEmptyVerifiedStatus(t *testing.T) {
	_, _, record := nativePreflightFixture(t, "status")
	for _, reply := range []domain.UpgradeNativeReply{
		{State: "verified", Platform: record.Request.Platform},
		{State: "verified", Platform: record.Request.Platform, Version: "0.3.3",
			Commit: strings.Repeat("a", 40), BackupSHA256: strings.Repeat("b", 64), Lease: &domain.UpgradeLease{}},
		{State: "failed", Platform: record.Request.Platform, Version: "0.3.3",
			Error: domain.AsFault(domain.Fail(domain.Unavailable, "fixture unavailable"))},
	} {
		if err := ValidatePreflightResult(*record, reply, record.CreatedAt); err == nil {
			t.Fatal("native preflight accepted fabricated verification or start authority")
		}
	}
}

type failingPreflightRunner struct{}

func (failingPreflightRunner) Run(context.Context, process.Spec) error {
	return errors.New("fixture unavailable")
}
