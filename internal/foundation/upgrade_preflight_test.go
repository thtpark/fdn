package foundation

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func preflightRequest(f *securityFixture, target domain.AgentIdentity, action string) domain.UpgradePreflightRequest {
	return domain.UpgradePreflightRequest{Command: op(), InstanceID: f.founder.InstanceID,
		TargetID: target.AgentID, Role: "agent", Platform: "linux-amd64",
		Native: domain.UpgradeNativeRequest{Action: action, ID: domain.NewID("native")}}
}

func preflightResult(record domain.UpgradePreflight, now int64) domain.UpgradeNativeReply {
	challenge := &domain.UpgradeChallenge{
		InstanceID: record.Request.InstanceID, TargetID: record.Request.TargetID,
		Role: record.Request.Role, Generation: record.TargetGeneration, CurrentVersion: "0.3.2",
		CurrentCommit: strings.Repeat("a", 40), PolicySHA256: strings.Repeat("b", 64),
	}
	reply := domain.UpgradeNativeReply{State: "observed", Platform: record.Request.Platform,
		Version: challenge.CurrentVersion, Commit: challenge.CurrentCommit, Challenge: challenge}
	if record.Request.Native.Action == "challenge" {
		challenge.ID, challenge.Nonce, challenge.ExpiresAt = record.Request.Native.ID, domain.NewToken(), now+900
		reply.State, reply.Version, reply.Commit = "planned", "", ""
	}
	return reply
}

func TestUpgradePreflightIsFounderOnlyPrivateExactReplayAndDoesNotPauseWork(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Preflight worker", 4101)
	other, _, _ := f.register(t, "Other worker", 4102)
	request := preflightRequest(f, target, "inspect")
	_, err := f.s.RequestUpgradePreflight(ctx, target.AgentID, request)
	assertCode(t, err, domain.Denied)
	before, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	if err != nil || record.State != "queued" || record.Revision != 1 ||
		record.ExpiresAt-record.CreatedAt != 900 || record.Result != nil ||
		record.FounderBindingID != f.founder.AgentID {
		t.Fatal("preflight lost its fixed private target or lifetime", record, err)
	}
	replayed, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	if err != nil || !reflect.DeepEqual(replayed, record) {
		t.Fatal("preflight replay renewed its request or target", err)
	}
	changed := request
	changed.TargetID = other.AgentID
	_, err = f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, changed)
	assertCode(t, err, domain.Conflict)
	inbox, err := f.s.NativeUpgradePreflights(ctx, target.AgentID)
	if err != nil || len(inbox) != 1 || inbox[0].ID != record.ID || inbox[0].Problem != nil {
		t.Fatal("exact target did not receive its private preflight", inbox, err)
	}
	unrelated, err := f.s.NativeUpgradePreflights(ctx, other.AgentID)
	if err != nil || len(unrelated) != 0 {
		t.Fatal("native preflight leaked into another target inbox", unrelated, err)
	}
	_, err = f.s.UpgradePreflight(ctx, other.AgentID, record.ID)
	assertCode(t, err, domain.Denied)
	report := domain.UpgradePreflightReport{Command: op(), ID: record.ID, Result: preflightResult(record, f.now.Unix())}
	report.ExpectedRevision = record.Revision
	_, err = f.s.ReportUpgradePreflight(ctx, other.AgentID, report)
	assertCode(t, err, domain.Denied)
	done, err := f.s.ReportUpgradePreflight(ctx, target.AgentID, report)
	if err != nil || done.State != "completed" || done.Revision != 2 || done.Result == nil {
		t.Fatal("target observation was not recorded transactionally", done, err)
	}
	replayedResult, err := f.s.ReportUpgradePreflight(ctx, target.AgentID, report)
	if err != nil || !reflect.DeepEqual(replayedResult, done) {
		t.Fatal("lost preflight acknowledgement was not exactly replayable", err)
	}
	observed, err := f.s.UpgradePreflight(ctx, f.founder.AgentID, record.ID)
	if err != nil || !reflect.DeepEqual(observed, done) {
		t.Fatal("requesting Founder could not inspect the actual observation", err)
	}
	after, err := f.s.Agent(ctx, f.founder.AgentID, target.AgentID)
	if err != nil || before.Mode != "ai" || after.Mode != before.Mode || after.Revision != before.Revision {
		t.Fatal("read-only native preflight changed automation", after, err)
	}
	var permits int
	if err := f.s.db.QueryRowContext(ctx, "SELECT count(*) FROM settings WHERE key LIKE 'upgrade-permit:%'").Scan(&permits); err != nil || permits != 0 {
		t.Fatal("preflight created native installation authority", permits, err)
	}
}

func TestUpgradePreflightRejectsInstallActionsAndFoundationBoundaryOverrides(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Preflight worker", 4101)
	for _, change := range []func(*domain.UpgradePreflightRequest){
		func(r *domain.UpgradePreflightRequest) { r.Native.Action = "prepare" },
		func(r *domain.UpgradePreflightRequest) { r.Native.Action = "apply" },
		func(r *domain.UpgradePreflightRequest) { r.Native.Permit = &domain.UpgradePermit{} },
		func(r *domain.UpgradePreflightRequest) { r.Platform = "windows-amd64" },
		func(r *domain.UpgradePreflightRequest) { r.Native.ID = "../job" },
		func(r *domain.UpgradePreflightRequest) { r.ExpectedRevision = 1 },
	} {
		request := preflightRequest(f, target, "challenge")
		change(&request)
		_, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
		assertCode(t, err, domain.Invalid)
	}
	request := preflightRequest(f, target, "challenge")
	request.Role, request.TargetID = "foundation", f.founder.InstanceID
	record, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	if err != nil {
		t.Fatal(err)
	}
	system, err := f.s.SystemUpgradePreflights(ctx)
	if err != nil || len(system) != 1 || system[0].ID != record.ID || record.TargetGeneration != 1 {
		t.Fatal("Foundation did not retain its deterministic private native inbox", system, err)
	}
	report := domain.UpgradePreflightReport{Command: op(), ID: record.ID, Result: preflightResult(record, f.now.Unix())}
	report.ExpectedRevision = 1
	_, err = f.s.ReportUpgradePreflight(ctx, f.founder.AgentID, report)
	assertCode(t, err, domain.Denied)
	if done, err := f.s.ReportSystemUpgradePreflight(ctx, report); err != nil || done.State != "completed" {
		t.Fatal("root-private Foundation preflight did not report its exact challenge", done, err)
	}
	request = preflightRequest(f, target, "inspect")
	request.InstanceID = "instance_other"
	_, err = f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	assertCode(t, err, domain.Denied)
}

func TestUpgradePreflightExpiryAndFailuresNeverBecomeFreshSuccess(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Preflight worker", 4101)
	request := preflightRequest(f, target, "challenge")
	record, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(20 * time.Minute)
	replay, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, request)
	if err != nil || replay.ExpiresAt != record.ExpiresAt {
		t.Fatal("expired preflight replay renewed its authority", err)
	}
	_, err = f.s.UpgradePreflight(ctx, f.founder.AgentID, record.ID)
	assertCode(t, err, domain.Conflict)
	inbox, err := f.s.NativeUpgradePreflights(ctx, target.AgentID)
	if err != nil || len(inbox) != 1 || inbox[0].Problem == nil || inbox[0].Problem.Code != domain.Conflict {
		t.Fatal("expired preflight did not tell the target to refuse without probing", inbox, err)
	}
	report := domain.UpgradePreflightReport{Command: op(), ID: record.ID, Result: preflightResult(record, f.now.Unix())}
	report.ExpectedRevision = 1
	_, err = f.s.ReportUpgradePreflight(ctx, target.AgentID, report)
	assertCode(t, err, domain.Denied)
	report.Command = op()
	report.ExpectedRevision = 1
	report.Result = domain.UpgradeNativeReply{State: "failed",
		Error: domain.AsFault(domain.Fail(domain.Conflict, "PRIVATE_NATIVE_PATH_AND_ERROR"))}
	failed, err := f.s.ReportUpgradePreflight(ctx, target.AgentID, report)
	if err != nil || failed.State != "failed" || failed.Result == nil ||
		failed.Result.Error == nil || failed.Result.Error.Message != upgrade.PreflightFailureMessage {
		t.Fatal("failure was hidden or exposed private native diagnostics", failed, err)
	}
	var raw string
	if err := f.s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", upgradePreflightPrefix+record.ID).Scan(&raw); err != nil ||
		strings.Contains(raw, "PRIVATE_NATIVE_PATH_AND_ERROR") {
		t.Fatal("private raw error was stored in server preflight evidence", err)
	}
}

func TestUpgradePreflightPinsTheRequestingFounderDeviceAndBoundsPendingControls(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	target, _, _ := f.register(t, "Preflight worker", 4101)
	extra, _ := founderDevice(t, f, "Founder extra", 4102)
	extraContext := WithIdentityFence(ctx, securityCertificate(extra, "device"))
	request := preflightRequest(f, target, "inspect")
	record, err := f.s.RequestUpgradePreflight(extraContext, f.founder.AgentID, request)
	if err != nil || record.FounderBindingID != extra.BindingID() || record.FounderGeneration != extra.Generation {
		t.Fatal("preflight borrowed another Founder computer's generation", record, err)
	}
	_, err = f.s.NativeUpgradePreflights(extraContext, f.founder.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RevokeFounderDevice(ctx, f.founder.AgentID, extra.DeviceID,
		domain.RevokeDeviceRequest{Command: op(), Reason: "Withdraw this fixture computer"})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := f.s.NativeUpgradePreflights(ctx, target.AgentID)
	if err != nil || len(inbox) != 1 || inbox[0].Problem == nil || inbox[0].Problem.Code != domain.Denied {
		t.Fatal("revoked requesting device still admitted native preflight", inbox, err)
	}
	_, err = f.s.UpgradePreflight(ctx, f.founder.AgentID, record.ID)
	assertCode(t, err, domain.Denied)
	for range 31 {
		if _, err := f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, preflightRequest(f, target, "inspect")); err != nil {
			t.Fatal(err)
		}
	}
	_, err = f.s.RequestUpgradePreflight(ctx, f.founder.AgentID, preflightRequest(f, target, "inspect"))
	assertCode(t, err, domain.Prerequisite)
}
