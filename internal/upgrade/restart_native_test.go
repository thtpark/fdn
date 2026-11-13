package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type restartRecorder struct {
	*nativeRecorder
	failStop bool
}

func (r *restartRecorder) Run(ctx context.Context, spec process.Spec) error {
	joined := strings.Join(spec.Args, " ")
	if r.failStop && (strings.Contains(joined, " stop ") || strings.HasPrefix(joined, "bootout ")) {
		r.calls = append(r.calls, spec)
		return errors.New("fixture lost the service-stop response")
	}
	if strings.HasSuffix(joined, " status") && !strings.Contains(joined, "service status") {
		r.calls = append(r.calls, spec)
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]any{
			"agent": domain.Agent{ID: "agent_fixture", Active: true, Mode: "manual"},
			"local": map[string]any{"pending_invocations": []string{}, "session_fence": map[string]bool{"active": false},
				"work": map[string]int{"pending_reports": 0}}}})
	}
	return r.nativeRecorder.Run(ctx, spec)
}

func restartFixture(t *testing.T) (*Native, *restartRecorder, domain.AgentRestartPermit, []byte) {
	t.Helper()
	native, recorder, key, _ := nativeFixture(t)
	native.Policy.AllowNativeRestart = true
	var identity config.IdentityService
	if err := config.Read(native.Policy.IdentityConfig, &identity); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(filepath.Join(identity.StateDir, "connection.json"), config.ConnectionProfile{
		Identity: domain.AgentIdentity{AgentID: native.Policy.TargetID, InstanceID: native.Policy.InstanceID, Generation: 1, State: "provisioned"}}); err != nil {
		t.Fatal(err)
	}
	permit := domain.AgentRestartPermit{ID: "restart_fixture", InstanceID: native.Policy.InstanceID,
		RequesterID: "agent_founder", RequesterBindingID: "device_founder", RequesterGeneration: 1,
		TargetID: native.Policy.TargetID, TargetGeneration: 1, ModeRevision: 2, Reason: "Restart fixed services only",
		CreatedAt: native.Now().Unix(), ExpiresAt: native.Now().Unix() + 900}
	var err error
	permit.Signature, err = SignRestartPermit(permit, key)
	if err != nil {
		t.Fatal(err)
	}
	r := &restartRecorder{nativeRecorder: recorder}
	native.Runner = r
	return native, r, permit, key
}

func TestNativeRestartRequiresExactSignedPermitAndNonceAndNeverReplaysServiceEffects(t *testing.T) {
	native, recorder, permit, key := restartFixture(t)
	ctx := context.Background()
	reply, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "inspect", Permit: permit})
	if err != nil || reply.Challenge == nil || reply.State != "planned" {
		t.Fatal(reply, err)
	}
	lease := domain.UpgradeLease{Nonce: reply.Challenge.Nonce, PermitSHA256: domain.Hash(permit.Signature),
		IssuedAt: native.Now().Unix(), ExpiresAt: native.Now().Unix() + 30}
	lease.Signature, err = SignRestartLease(lease, key)
	if err != nil {
		t.Fatal(err)
	}
	wrong := lease
	wrong.Nonce = domain.NewToken()
	if _, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &wrong}); err == nil {
		t.Fatal("restart accepted another nonce")
	}
	result, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &lease})
	if err != nil || result.State != "verified" || result.Runtime == nil || !result.Runtime.ServicesRunning {
		t.Fatal(result, err)
	}
	before := len(recorder.calls)
	replayed, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &lease})
	if err != nil || replayed.State != "verified" || len(recorder.calls) != before {
		t.Fatal("restart replay repeated native effects", replayed, err)
	}
	native.Now = func() time.Time { return time.Unix(permit.ExpiresAt+1, 0) }
	if _, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &lease}); err == nil {
		t.Fatal("expired permit regained restart authority")
	}
	status, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "status", Permit: permit})
	if err != nil || status.State != "verified" || len(recorder.calls) != before {
		t.Fatal("historical receipt read repeated native actions", status, err)
	}
}

func TestNativeRestartUncertaintyAndPolicyChangesRemainFenced(t *testing.T) {
	native, recorder, permit, key := restartFixture(t)
	ctx := context.Background()
	reply, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "inspect", Permit: permit})
	if err != nil {
		t.Fatal(err)
	}
	lease := domain.UpgradeLease{Nonce: reply.Challenge.Nonce, PermitSHA256: domain.Hash(permit.Signature),
		IssuedAt: native.Now().Unix(), ExpiresAt: native.Now().Unix() + 30}
	lease.Signature, err = SignRestartLease(lease, key)
	if err != nil {
		t.Fatal(err)
	}
	recorder.failStop = true
	if _, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &lease}); err == nil {
		t.Fatal("lost stop response became success")
	}
	before := len(recorder.calls)
	recorder.failStop = false
	if _, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "apply", Permit: permit, Lease: &lease}); err == nil {
		t.Fatal("an uncertain native restart was repeated")
	}
	status, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "status", Permit: permit})
	if err != nil || status.State != "operator-required" || len(recorder.calls) != before {
		t.Fatal(status, err)
	}
	native.Policy.AllowNativeRestart = false
	if _, err := native.RestartHandle(ctx, domain.AgentRestartNativeRequest{Action: "inspect", Permit: permit}); err == nil {
		t.Fatal("administrator-disabled policy still allowed native effects")
	}
}
