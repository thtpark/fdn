package upgrade

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
)

type restartRouteRunner struct {
	applies     int
	inspections int
	statuses    int
	loseApply   bool
}

func (r *restartRouteRunner) Run(_ context.Context, spec process.Spec) error {
	if spec.Path != "ssh" || !strings.Contains(spec.Args[len(spec.Args)-1], "restart-helper --policy") {
		return errors.New("restart left its fixed pinned helper route")
	}
	raw, err := io.ReadAll(spec.Stdin)
	if err != nil {
		return err
	}
	var request domain.AgentRestartNativeRequest
	if err := message.UnmarshalPayload(raw, &request); err != nil {
		return err
	}
	reply := domain.AgentRestartNativeReply{State: "verified", Runtime: &domain.NativeRuntimeObservation{
		Version: "0.3.41", Commit: strings.Repeat("a", 40), IdentityGeneration: 1, ServicesRunning: true}}
	switch request.Action {
	case "inspect":
		r.inspections++
		reply = domain.AgentRestartNativeReply{State: "planned", Challenge: &domain.UpgradeChallenge{
			ID: request.Permit.ID, InstanceID: request.Permit.InstanceID, TargetID: request.Permit.TargetID,
			Generation: 1, Role: "agent", Nonce: strings.Repeat("n", 43), CurrentVersion: "0.3.41",
			CurrentCommit: strings.Repeat("a", 40), PolicySHA256: domain.Hash("fixed-native-policy")}}
	case "apply":
		r.applies++
		if r.loseApply {
			r.loseApply = false
			return domain.Fail(domain.Unavailable, "fixture lost applied restart response")
		}
	case "status":
		r.statuses++
	default:
		return errors.New("unexpected native action")
	}
	data, err := message.MarshalPayload(reply)
	if err != nil {
		return err
	}
	_, err = spec.Stdout.Write(data)
	return err
}

func TestIndependentRestartReceiverUsesStatusAndExactReportAfterLostApplyAndDelivery(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	key, known := filepath.Join(root, "operator-key"), filepath.Join(root, "known-hosts")
	for _, path := range []string{key, known} {
		if err := os.WriteFile(path, []byte("private fixture, never a real credential"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	permit := domain.AgentRestartPermit{ID: "restart_fixture", InstanceID: "instance_fixture",
		RequesterID: "agent_founder", RequesterBindingID: "device_founder", RequesterGeneration: 1,
		TargetID: "agent_target", TargetGeneration: 1, ModeRevision: 2, Reason: "Restore fixed services",
		CreatedAt: now.Unix(), ExpiresAt: now.Unix() + 900, Signature: "fixed-fixture-signature"}
	target := Target{Role: "agent", TargetID: permit.TargetID, Platform: "linux-amd64", Helper: "/usr/bin/agent-service",
		PolicyFile: "/etc/fdn/updater.json", Sudo: true}
	target.SSH.Host, target.SSH.User, target.SSH.Port = "127.0.0.1", "operator", 2222
	target.SSH.IdentityFile, target.SSH.KnownHostsFile = key, known
	runner := &restartRouteRunner{loseApply: true}
	native := &Native{Policy: Policy{Role: "foundation", InstanceID: permit.InstanceID, StateDir: root, RestartTargets: []Target{target}},
		Runner: runner, Now: func() time.Time { return now }}
	finished, loseReport, leases := false, true, 0
	var originalReport *domain.AgentRestartReport
	native.Mailbox = func(_ context.Context, command domain.MessageWorkerCommand) (domain.MessageWorkerReply, error) {
		if command.Restarts {
			if finished {
				return domain.MessageWorkerReply{}, nil
			}
			return domain.MessageWorkerReply{Restarts: []domain.AgentRestartPermit{permit}}, nil
		}
		if command.RestartLease != nil {
			leases++
			if command.RestartLease.ID != permit.ID || command.RestartLease.Nonce != strings.Repeat("n", 43) ||
				command.RestartLease.PermitSHA256 != domain.Hash(permit.Signature) {
				t.Fatal("lease scope changed", command)
			}
			return domain.MessageWorkerReply{RestartLease: &domain.UpgradeLease{Nonce: command.RestartLease.Nonce,
				PermitSHA256: command.RestartLease.PermitSHA256, IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 30}}, nil
		}
		report := command.RestartReport
		if report == nil {
			t.Fatal("restart used an ordinary message or preflight")
		}
		if report.State == "restarted" && loseReport {
			copy := *report
			originalReport = &copy
			loseReport = false
			return domain.MessageWorkerReply{}, domain.Fail(domain.Unavailable, "fixture lost exact report acknowledgement")
		}
		if report.State == "restarted" {
			if report.OperationID != originalReport.OperationID || report.ExpectedRevision != originalReport.ExpectedRevision ||
				report.Result.Code != originalReport.Result.Code {
				t.Fatal("pending restart report was replaced")
			}
			finished = true
		}
		return domain.MessageWorkerReply{Restart: &domain.AgentOperation{
			ID: permit.ID, Kind: "native-restart", State: report.State, Revision: report.ExpectedRevision + 1,
			RequesterID: permit.RequesterID, TargetID: permit.TargetID, CreatedAt: permit.CreatedAt, ExpiresAt: permit.ExpiresAt}}, nil
	}
	if handled, err := native.RestartStep(context.Background()); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if handled, err := native.RestartStep(context.Background()); !handled || err == nil {
		t.Fatal("lost apply became success", handled, err)
	}
	if handled, err := native.RestartStep(context.Background()); !handled || err == nil {
		t.Fatal("lost report became success", handled, err)
	}
	if handled, err := native.RestartStep(context.Background()); !handled || err != nil || !finished {
		t.Fatal(handled, err, finished)
	}
	if runner.applies != 1 || runner.inspections != 1 || runner.statuses != 1 || leases != 1 {
		t.Fatal("uncertainty repeated native work or start authority", runner, leases)
	}
	if _, err := os.Stat(filepath.Join(root, "restart-inflight.json")); !os.IsNotExist(err) {
		t.Fatal("settled delivery retained an active fence", err)
	}
	if handled, err := native.RestartStep(context.Background()); handled || err != nil {
		t.Fatal("completed restart repeated", handled, err)
	}
}
