package software

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/upgrade"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(account.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(base, ".fdn-software-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				return os.Chmod(name, 0700)
			}
			return walkErr
		})
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return directory
}

func writeTestFile(t *testing.T, filename string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, mode); err != nil {
		t.Fatal(err)
	}
}

func distributionKeys(t *testing.T) ([]byte, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
}

type softwareFixture struct {
	Root       string
	Native     *Native
	Now        time.Time
	PrivateKey []byte
	PublicKey  []byte
	CAKey      []byte
	CA         []byte
	Recovery   *age.X25519Identity
	Artifacts  map[string][]byte
	Fetches    int
	Leases     int
	Recorder   *nativeRecorder
}

func nativeFixture(t *testing.T) *softwareFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("native actual-installing-user fixture does not create another OS account")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		t.Fatal(err)
	}
	root := testDirectory(t)
	if !inside(account.HomeDir, root) {
		t.Skip("fixture checkout must be within the existing non-root installing user's HOME")
	}
	now := time.Now().UTC().Truncate(time.Second)
	ca, err := pki.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	private, public := distributionKeys(t)
	recovery, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"native", "data"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "distribution.pem"), public, 0600)
	writeTestFile(t, filepath.Join(root, "ca.pem"), ca.CA, 0600)
	writeTestFile(t, filepath.Join(root, "data", "state.json"), []byte("original user state, keys and journals"), 0600)
	policy := Policy{FormatVersion: 1, InstanceID: "instance_fixture", TargetID: "agent_fixture",
		BindingID: "device_fixture", Generation: 3, ProductID: "product_fixture", Kind: "tool",
		Platform: currentHost().OS + "-" + currentHost().Arch, User: account.Username, UID: uid, Home: account.HomeDir,
		PublicKeyFile: filepath.Join(root, "distribution.pem"), CAFile: filepath.Join(root, "ca.pem"),
		StateDir: filepath.Join(root, "native"), Prefix: filepath.Join(root, "prefix"), Method: "archive", Manager: "none",
		BackupPaths: []string{filepath.Join(root, "data")}, BackupRecipient: recovery.Recipient().String()}
	native := NewNative(policy)
	fixture := &softwareFixture{Root: root, Native: native, Now: now, PrivateKey: private, PublicKey: public,
		CAKey: ca.CAKey, CA: ca.CA, Recovery: recovery, Artifacts: make(map[string][]byte)}
	recorder := &nativeRecorder{fixture: fixture}
	fixture.Recorder, native.Runner = recorder, recorder
	native.Now = func() time.Time { return fixture.Now }
	native.Fetch = func(_ context.Context, artifactID, output string) error {
		fixture.Fetches++
		data, found := fixture.Artifacts[artifactID]
		if !found {
			return errors.New("fixture artifact not found")
		}
		return os.WriteFile(output, data, 0600)
	}
	native.Lease = func(_ context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		fixture.Leases++
		lease := domain.UpgradeLease{PermitSHA256: request.PermitSHA256, Nonce: request.Nonce,
			IssuedAt: fixture.Now.Unix(), ExpiresAt: fixture.Now.Unix() + 30}
		lease.Signature, err = upgrade.SignLease(lease, fixture.CAKey)
		return lease, err
	}
	return fixture
}

func (f *softwareFixture) release(t *testing.T, revision int64) domain.SoftwareRelease {
	t.Helper()
	root := filepath.Join(f.Root, "source-"+strconv.FormatInt(revision, 10)+"-"+domain.NewID("bytes"))
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "bin", "tool"), []byte("synthetic native tool revision "+strconv.FormatInt(revision, 10)), 0755)
	writeTestFile(t, filepath.Join(root, "share", "notice.txt"), []byte("Fixture notice\n"), 0644)
	outputs := []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}, {Path: "share/notice.txt", Mode: 0644}}
	var bundle bytes.Buffer
	payloads, err := CreateBundle(&bundle, root, outputs, f.Now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "release_" + strconv.FormatInt(revision, 10)
	artifactID := "artifact_" + strconv.FormatInt(revision, 10)
	manifest := domain.SoftwareManifest{FormatVersion: 1, InstanceID: f.Native.Policy.InstanceID,
		ReleaseID: releaseID, ProductID: f.Native.Policy.ProductID, ProjectID: "project_fixture",
		Kind: f.Native.Policy.Kind, Version: "1.0." + strconv.FormatInt(revision, 10), Revision: revision,
		Source: domain.TaskSource{RepositoryID: "repository_fixture", Commit: strings.Repeat("a", 40),
			Ref: "refs/heads/main", ProductID: f.Native.Policy.ProductID}, CreatedAt: f.Now.Unix(),
		Platforms: []domain.SoftwarePlatform{{Platform: f.Native.Policy.Platform, ArtifactID: artifactID,
			SHA256: domain.Hash(bundle.String()), Size: int64(bundle.Len()), Profile: f.Native.Policy.Method, Payloads: payloads}}}
	release := domain.SoftwareRelease{ID: releaseID, Manifest: manifest}
	f.signRelease(t, &release)
	f.Artifacts[artifactID] = append([]byte{}, bundle.Bytes()...)
	return release
}

func (f *softwareFixture) signRelease(t *testing.T, release *domain.SoftwareRelease) {
	t.Helper()
	var err error
	release.KeyID, release.Signature, err = SignRelease(release.Manifest, f.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *softwareFixture) signPermit(t *testing.T, permit *domain.SoftwarePermit) {
	t.Helper()
	var err error
	permit.Signature, err = SignPermit(*permit, f.CAKey)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *softwareFixture) permit(t *testing.T, id string, release domain.SoftwareRelease) domain.SoftwarePermit {
	t.Helper()
	reply, err := f.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: id,
		ProductID: f.Native.Policy.ProductID})
	if err != nil || reply.Challenge == nil {
		t.Fatalf("challenge: %#v %v", reply, err)
	}
	permit := domain.SoftwarePermit{ID: "permit_" + id,
		Proposal: domain.SoftwareProposal{Challenge: *reply.Challenge, Release: release,
			BackupRecipient: f.Recovery.Recipient().String(), Reason: "Explicit fixture install authorization"},
		ActorID: "founder_fixture", ActorBindingID: "founder_device_fixture", ActorGeneration: 2,
		IssuedAt: f.Now.Unix(), ExpiresAt: f.Now.Unix() + AuthorizationTTL}
	f.signPermit(t, &permit)
	return permit
}

func applyPermit(t *testing.T, native *Native, permit domain.SoftwarePermit) domain.SoftwareNativeReply {
	t.Helper()
	reply, err := native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "apply",
		ID: permit.Proposal.Challenge.ID, Permit: &permit})
	if err != nil || reply.State != "installed" || reply.Result == nil {
		t.Fatalf("apply: %#v %v", reply, err)
	}
	return reply
}

func clonePermit(t *testing.T, value domain.SoftwarePermit) domain.SoftwarePermit {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result domain.SoftwarePermit
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type nativeRecorder struct {
	fixture      *softwareFixture
	calls        []process.Spec
	effects      []string
	loaded       bool
	running      bool
	enabled      bool
	pid          int
	activeRoot   string
	failEffect   string
	dropIns      string
	packageState packageState
	packages     map[string]debFixture
}

func (r *nativeRecorder) Run(_ context.Context, spec process.Spec) error {
	r.calls = append(r.calls, spec)
	if r.fixture.Native.Policy.Method == "archive" {
		return errors.New("portable archive unexpectedly invoked a process")
	}
	if spec.Path == "/usr/bin/systemctl" {
		return r.systemd(spec)
	}
	if spec.Path == "/bin/launchctl" {
		return r.launchd(spec)
	}
	return r.debian(spec)
}

func writeRawTar(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	for index, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if index < len(contents) {
			if _, err := writer.Write(contents[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func requireNoPrefix(t *testing.T, fixture *softwareFixture) {
	t.Helper()
	if _, err := os.Lstat(fixture.Native.Policy.Prefix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native prefix was changed: %v", err)
	}
	if len(fixture.Recorder.effects) != 0 || fixture.Leases != 0 {
		t.Fatalf("unexpected native effects/leases: %v/%d", fixture.Recorder.effects, fixture.Leases)
	}
}
