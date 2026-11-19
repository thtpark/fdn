package upgrade

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
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
	calls       []process.Spec
	newSource   bool
	failApply   bool
	busy        bool
	clearFence  bool
	generation  int64
	release     domain.UpgradeRelease
	afterBackup func()
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
			"pending_invocations": pending, "session_fence": map[string]any{"active": !r.clearFence, "session_id": "session_retained"},
			"work": map[string]any{"mode": "manual", "pending_reports": 0}}})
	case strings.HasSuffix(joined, "doctor"):
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": map[string]string{"tls_and_authentication": "verified"}})
	case spec.Path == "/usr/bin/tar":
		_, err := io.WriteString(spec.Stdout, "cold fixture: original keys, journals, public binding and fence")
		if err == nil && r.afterBackup != nil {
			r.afterBackup()
		}
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

func TestNativeColdBackupDeduplicatesOnlyValidatedCoverage(t *testing.T) {
	native, _, _, _ := nativeFixture(t)
	parent := filepath.Dir(native.Policy.ConfigFile)
	outside, err := os.MkdirTemp(native.Policy.Home, ".fdn-backup-ca-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	native.Policy.CAFile = filepath.Join(outside, "ca.pem")
	if err := os.WriteFile(native.Policy.CAFile, []byte("separately rooted CA"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := native.backupPaths()
	if err != nil {
		t.Fatal(err)
	}
	native.Policy.BackupPaths = append(native.Policy.BackupPaths, parent, parent)
	// Updater evidence must remain outside the selected application root.
	native.Policy.StateDir = filepath.Join(native.Policy.Home, ".fdn-outside-backup-evidence")
	roots, err := native.backupPaths()
	if err != nil || len(roots) != 2 || !slices.Contains(roots, parent) || !slices.Contains(roots, native.Policy.CAFile) {
		t.Fatal("nested mandatory roots were archived more than once", roots, err)
	}
	for _, path := range original {
		if !inside(parent, path) && path != native.Policy.CAFile {
			t.Fatal("mandatory backup coverage was lost", path)
		}
	}
	link := filepath.Join(parent, "linked-state")
	if err := os.Symlink(filepath.Join(parent, "worker"), link); err != nil {
		t.Fatal(err)
	}
	native.Policy.BackupPaths = append(native.Policy.BackupPaths, link)
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("covered symlink bypassed validation")
	}
	native.Policy.BackupPaths[len(native.Policy.BackupPaths)-1] = filepath.Join(parent, "missing")
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("covered missing mandatory root bypassed validation")
	}
	native.Policy.BackupPaths[len(native.Policy.BackupPaths)-1] = parent + "/worker/.."
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("covered noncanonical root bypassed validation")
	}
	native.Policy.BackupPaths[len(native.Policy.BackupPaths)-1] = parent
	native.Policy.StateDir = filepath.Join(parent, "updater")
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("updater evidence overlap bypassed validation")
	}
}

func TestNativeFoundationColdBackupRequiresPinnedFilesystemContent(t *testing.T) {
	native, _, _, _ := nativeFixture(t)
	native.Policy.Role = "foundation"
	state := filepath.Join(filepath.Dir(native.Policy.ConfigFile), "server")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	server := config.Server{StateDir: state, CertFile: filepath.Join(state, "server.pem"),
		KeyFile: filepath.Join(state, "server-key.pem"), IngressSocket: "/run/fdn/api.sock",
		JoinSocket: "/run/fdn/join.sock"}
	if err := config.Write(native.Policy.ConfigFile, server); err != nil {
		t.Fatal(err)
	}
	var header [100]byte
	copy(header[:], "SQLite format 3\x00")
	binary.BigEndian.PutUint32(header[60:64], 10)
	databasePath := filepath.Join(state, "foundation.sqlite")
	if err := os.WriteFile(databasePath, header[:], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("SQL10 content was silently omitted from a cold backup")
	}
	if err := os.Mkdir(server.FileDirectory(), 0711); err != nil {
		t.Fatal(err)
	}
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("unpinned mutable file_dir was accepted as administrator backup authority")
	}
	native.Policy.BackupPaths = append(native.Policy.BackupPaths, server.FileDirectory())
	paths, err := native.backupPaths()
	if err != nil || !slices.Contains(paths, server.FileDirectory()) {
		t.Fatal("pinned content store was not included in the cold backup", paths, err)
	}
	if err := os.Remove(server.FileDirectory()); err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint32(header[60:64], 9)
	if err := os.WriteFile(databasePath, header[:], 0600); err != nil {
		t.Fatal(err)
	}
	native.Policy.BackupPaths = native.Policy.BackupPaths[:len(native.Policy.BackupPaths)-1]
	if _, err := native.backupPaths(); err != nil {
		t.Fatal("retained SQL9 database-only backup was unnecessarily rejected", err)
	}
	if err := os.WriteFile(databasePath+"-wal", []byte("uncheckpointed schema evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := native.backupPaths(); err == nil {
		t.Fatal("stale legacy header was treated as proof that no external files exist")
	}
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

func TestNativeUpgradeLongColdBackupCompletesOnlyOriginalAdmittedJob(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	now := native.Now()
	native.Now = func() time.Time { return now }
	permit := preparePermit(t, native, recorder, signer, key)
	recorder.afterBackup = func() { now = now.Add(16 * time.Minute) }
	leases := 0
	lease := native.Lease
	native.Lease = func(ctx context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		leases++
		return lease(ctx, request)
	}
	request := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
	prepared, err := native.Handle(context.Background(), request)
	if err != nil || prepared.State != "prepared" || now.Unix() <= permit.ExpiresAt {
		t.Fatal("long cold backup did not cross the fixed admission deadline", prepared, err)
	}
	ca, err := os.ReadFile(native.Policy.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPermit(permit, ca, now); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary expired admission was weakened", err)
	}
	tampered := permit
	tampered.Proposal.Reason = "different authority behind the same signature"
	changed := request
	changed.Permit = &tampered
	before := len(recorder.calls)
	if _, err := native.Handle(context.Background(), changed); domain.AsFault(err).Code != domain.Conflict || len(recorder.calls) != before {
		t.Fatal("another proposal adopted the stopped job", err)
	}
	replay, err := native.Handle(context.Background(), request)
	if err != nil || replay != prepared {
		t.Fatal("exact prepared replay required another lease or backup", replay, err)
	}
	request.Action = "apply"
	result, err := native.Handle(context.Background(), request)
	if err != nil || result.State != "verified" || result.BackupSHA256 != prepared.BackupSHA256 {
		t.Fatal("admitted job could not finish after its long backup", result, err)
	}
	calls := len(recorder.calls)
	if _, err := native.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, call := range recorder.calls[calls:] {
		if call.Path == "/bin/sh" || call.Path == "/usr/bin/tar" || strings.Contains(strings.Join(call.Args, " "), "stop") {
			t.Fatal("completed replay repeated native effects")
		}
	}
	backups, installs := 0, 0
	for _, call := range recorder.calls {
		if call.Path == "/usr/bin/tar" {
			backups++
		}
		if call.Path == "/bin/sh" {
			installs++
		}
	}
	if leases != 1 || backups != 1 || installs != 1 {
		t.Fatal("completion renewed authority or repeated effects", leases, backups, installs)
	}
}

func TestNativeUpgradeLateCompletionRequiresActualColdBackupAndCurrentCA(t *testing.T) {
	for _, kind := range []string{"missing", "tampered", "symlink", "public", "missing-digest", "rotated-ca", "expired-ca"} {
		t.Run(kind, func(t *testing.T) {
			native, recorder, signer, key := nativeFixture(t)
			permit := preparePermit(t, native, recorder, signer, key)
			request := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
			if _, err := native.Handle(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			now := native.Now().Add(16 * time.Minute)
			native.Now = func() time.Time { return now }
			directory := filepath.Join(native.Policy.StateDir, request.ID)
			backup := filepath.Join(directory, "before-upgrade.tar.age")
			switch kind {
			case "missing":
				if err := os.Remove(backup); err != nil {
					t.Fatal(err)
				}
			case "tampered":
				if err := os.WriteFile(backup, []byte("changed encrypted bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(directory, "relocated-backup")
				if err := os.Rename(backup, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, backup); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(backup, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-digest":
				path := filepath.Join(directory, "job.json")
				var job nativeJob
				if err := config.Read(path, &job); err != nil {
					t.Fatal(err)
				}
				job.Backup = ""
				if err := config.Write(path, job); err != nil {
					t.Fatal(err)
				}
			case "rotated-ca":
				other, err := pki.Generate(now)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(native.Policy.CAFile, other.CA, 0600); err != nil {
					t.Fatal(err)
				}
			case "expired-ca":
				now = now.Add(100 * 365 * 24 * time.Hour)
			}
			before := len(recorder.calls)
			request.Action = "apply"
			if _, err := native.Handle(context.Background(), request); err == nil || len(recorder.calls) != before {
				t.Fatal("unproved late completion caused native effects", err)
			}
		})
	}
}

func TestNativeUpgradeExpiredUnadmittedPermitDoesNotStopServices(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	now := native.Now().Add(16 * time.Minute)
	native.Now = func() time.Time { return now }
	before := len(recorder.calls)
	request := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
	if _, err := native.Handle(context.Background(), request); domain.AsFault(err).Code != domain.Denied || len(recorder.calls) != before {
		t.Fatal("expired unadmitted request performed native operations", err)
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
