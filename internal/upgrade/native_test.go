package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
)

type nativeRecorder struct {
	calls      []process.Spec
	newSource  bool
	failApply  bool
	busy       bool
	generation int64
	release    domain.UpgradeRelease
}

func (r *nativeRecorder) Run(_ context.Context, spec process.Spec) error {
	r.calls = append(r.calls, spec)
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		version, commit := "0.3.2", strings.Repeat("b", 40)
		if r.newSource {
			version, commit = r.release.Version, r.release.Commit
		}
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]string{"version": version, "commit": commit}})
	}
	joined := strings.Join(spec.Args, " ")
	switch {
	case strings.Contains(joined, "agent identity"):
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": domain.AgentIdentity{
			AgentID: "agent_fixture", InstanceID: "instance_fixture", Generation: r.generation, State: "active"}})
	case strings.Contains(joined, "work status"):
		pending := []string{}
		if r.busy {
			pending = []string{"uncertain-fixture"}
		}
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]any{
			"pending_invocations": pending, "session_fence": map[string]any{"active": true, "session_id": "session_retained"},
			"work": map[string]any{"mode": "manual", "pending_reports": 0}}})
	case strings.HasSuffix(joined, "doctor"):
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]string{"tls_and_authentication": "verified"}})
	case spec.Path == "/usr/bin/tar":
		_, err := io.WriteString(spec.Stdout, "cold fixture: original keys, journals, public binding and fence")
		return err
	case spec.Path == "/bin/sh":
		if r.failApply {
			return errors.New("fixture install response lost")
		}
		r.newSource = true
	}
	return nil
}

func nativeFixture(t *testing.T) (*Native, *nativeRecorder, []byte, *age.X25519Identity) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("rootless actual-user file fixture does not create another OS account")
	}
	return nativeFixtureForUser(t)
}

func nativeFixtureForUser(t *testing.T) (*Native, *nativeRecorder, []byte, *age.X25519Identity) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(account.HomeDir, ".fdn-upgrade-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for _, directory := range []string{"prefix", "prefix/bin", "worker", "identity"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	ca, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, ca.CA, 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "worker.json")
	worker := config.DefaultWorker(filepath.Join(root, "worker"))
	worker.Mode = "manual"
	if err := config.Write(configFile, worker); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "worker", "work.lock"), filepath.Join(root, "services.json")} {
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := config.Write(filepath.Join(root, "identity.json"), config.IdentityService{
			StateDir: filepath.Join(root, "identity"), Socket: filepath.Join(root, "identity.sock"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	release, bundle := fixtureBundle(t, runtime.GOOS+"-"+runtime.GOARCH, nil)
	recorder := &nativeRecorder{generation: 1, release: release}
	native := &Native{
		Policy: Policy{Role: "agent", Name: "fixture", InstanceID: "instance_fixture", TargetID: "agent_fixture",
			User: account.Username, Home: account.HomeDir, Method: "archive", Prefix: filepath.Join(root, "prefix"),
			BinaryDir: filepath.Join(root, "prefix", "bin"), CAFile: caFile, StateDir: filepath.Join(root, "updater"),
			ConfigFile: configFile, IdentityConfig: filepath.Join(root, "identity.json"), ServiceOptions: filepath.Join(root, "services.json"),
			BackupPaths: []string{filepath.Join(root, "worker"), filepath.Join(root, "identity"), configFile}},
		Runner: recorder, Catalog: testCatalog(bundle), Now: func() time.Time { return now },
		CheckServices: func(context.Context) error { return nil },
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	native.Lease = func(_ context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		lease := domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce,
			IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 30}
		lease.Signature, err = SignLease(lease, ca.CAKey)
		return lease, err
	}
	return native, recorder, ca.CAKey, key
}

func preparePermit(t *testing.T, native *Native, recorder *nativeRecorder, signer []byte, recipient *age.X25519Identity) domain.UpgradePermit {
	t.Helper()
	reply, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "challenge", ID: "upgrade.fixture.0"})
	if err != nil || reply.Challenge == nil {
		t.Fatal(reply, err)
	}
	permit := domain.UpgradePermit{Proposal: domain.UpgradeProposal{Challenge: *reply.Challenge, Release: recorder.release,
		BackupRecipient: recipient.Recipient().String(), Reason: "Explicit fixture upgrade"},
		ActorID: "agent_founder", ActorGeneration: 1, IssuedAt: native.Now().Unix(), ExpiresAt: native.Now().Unix() + 900}
	permit.Signature, err = SignPermit(permit, signer)
	if err != nil {
		t.Fatal(err)
	}
	return permit
}

func TestNativeUpgradeColdBackupApplyReceiptAndExactReplay(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	req := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
	prepared, err := native.Handle(context.Background(), req)
	if err != nil || prepared.State != "prepared" || !domain.ValidHash(prepared.BackupSHA256) {
		t.Fatal(prepared, err)
	}
	path := filepath.Join(native.Policy.StateDir, req.ID, "before-upgrade.tar.age")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := age.Decrypt(file, key)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(plaintext), "original keys, journals") {
		t.Fatal("cold snapshot is not decryptable with the off-host key", err)
	}
	req.Action = "apply"
	result, err := native.Handle(context.Background(), req)
	if err != nil || result.State != "verified" || result.Commit != recorder.release.Commit || result.BackupSHA256 != prepared.BackupSHA256 {
		t.Fatal(result, err)
	}
	for range 2 {
		before := 0
		for _, call := range recorder.calls {
			if call.Path == "/bin/sh" {
				before++
			}
		}
		replay, err := native.Handle(context.Background(), req)
		if err != nil || replay != result {
			t.Fatal("completed native receipt did not replay", replay, err)
		}
		after := 0
		for _, call := range recorder.calls {
			if call.Path == "/bin/sh" {
				after++
			}
		}
		if before != after || after != 1 {
			t.Fatal("replay repeated the external installer")
		}
	}
	status, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "status", ID: req.ID})
	if err != nil || status != result {
		t.Fatal(status, err)
	}
}

func TestNativeUpgradeRejectsWrongSignatureTargetAndBusyStateBeforeStopping(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	tampered := permit
	tampered.Proposal.Release.Commit = strings.Repeat("e", 40)
	_, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &tampered})
	if domain.AsFault(err).Code != domain.Denied {
		t.Fatal("altered native authorization was accepted", err)
	}
	recorder.busy = true
	_, err = native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit})
	if domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("busy worker entered native upgrade", err)
	}
	for _, call := range recorder.calls {
		if strings.Contains(strings.Join(call.Args, " "), "stop ") || call.Path == "/bin/sh" {
			t.Fatal("unapproved/busy request stopped a service or ran the installer")
		}
	}
}

func TestNativeUpgradeUncertainInstallerNeverBlindlyReruns(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	req := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
	if _, err := native.Handle(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	recorder.failApply = true
	req.Action = "apply"
	if _, err := native.Handle(context.Background(), req); err == nil {
		t.Fatal("uncertain installer failure looked successful")
	}
	calls := len(recorder.calls)
	if _, err := native.Handle(context.Background(), req); domain.AsFault(err).Code != domain.Conflict || len(recorder.calls) != calls {
		t.Fatal("uncertain upgrade repeated native operations", err)
	}
	status, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "status", ID: req.ID})
	if err != nil || status.State != "operator-required" || status.Code != "installing" {
		t.Fatal("uncertainty was erased or projected as successful", status, err)
	}
}
